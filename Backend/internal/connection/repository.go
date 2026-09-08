package connection

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

// managedConnectionScope is the scope observed by the authenticated room
// authorization read.  It is compared again while the connection write
// transaction owns the authoritative attempt/lobby/room rows.  A nil scope is
// used by the internal room join path, which still derives and validates the
// current scope in that write transaction.
type managedConnectionScope struct {
	LobbyID         string
	AttemptID       string
	RosterRevision  int64
	RouteGeneration int32
}

func (r *Repository) CreateOrGet(ctx context.Context, item Connection, expectedScope *managedConnectionScope) (Connection, bool, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Connection{}, false, fmt.Errorf("begin connection creation: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := r.validateConnectionScope(ctx, tx, item, expectedScope); err != nil {
		return Connection{}, false, fmt.Errorf("validate connection scope: %w", err)
	}
	created, err := scanConnection(tx.QueryRow(ctx, `
		INSERT INTO connections (
			id, room_id, host_player_id, peer_player_id, state,
			expires_at, created_at, updated_at
		) VALUES ($1, $2, $3, $4, 'CREATED', $5, $6, $6)
		ON CONFLICT DO NOTHING
		RETURNING `+connectionColumns,
		item.ID, item.RoomID, item.HostPlayerID, item.PeerPlayerID, item.ExpiresAt, item.CreatedAt,
	))
	wasCreated := true
	if errors.Is(err, pgx.ErrNoRows) {
		wasCreated = false
		created, err = scanConnection(tx.QueryRow(ctx, `
			SELECT `+connectionColumns+`
			FROM connections
			WHERE room_id = $1 AND peer_player_id = $2
			  AND state NOT IN ('FAILED', 'EXPIRED', 'CLOSED')
			FOR UPDATE
		`, item.RoomID, item.PeerPlayerID))
	}
	if err != nil {
		return Connection{}, false, fmt.Errorf("create or get connection: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Connection{}, false, fmt.Errorf("commit connection creation: %w", err)
	}
	return created, wasCreated, nil
}

// validateConnectionScope closes the preflight-to-write race for both the
// public connection endpoint and the internal managed-room join path.  The
// authoritative rows are locked in the same order used by match-lobby
// completion: attempt, lobby, then room.  Any abort, route/revision change,
// room retirement, membership change, or roster change therefore makes this
// transaction fail before it can insert or reuse a connection.
func (r *Repository) validateConnectionScope(
	ctx context.Context,
	tx pgx.Tx,
	item Connection,
	expected *managedConnectionScope,
) error {
	var observedManagedLobbyID, observedHostPlayerID string
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(managed_lobby_id, ''), host_player_id
		FROM p2p_rooms
		WHERE id = $1 AND deleted_at IS NULL
	`, item.RoomID).Scan(&observedManagedLobbyID, &observedHostPlayerID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: room is unavailable", errManagedConnectionScope)
		}
		return err
	}
	if observedHostPlayerID != item.HostPlayerID {
		return fmt.Errorf("%w: room host changed", errManagedConnectionScope)
	}

	if observedManagedLobbyID == "" {
		return fmt.Errorf("%w: managed frozen attempt is required", errManagedConnectionScope)
	}

	var (
		hostPlayerID, roomState, lobbyID, lobbyState, currentAttemptID string
		lobbyRosterRevision, attemptRosterRevision                     int64
		attemptID, attemptState                                        string
		routeGeneration                                                int32
	)
	err := tx.QueryRow(ctx, `
		SELECT room.host_player_id, room.state,
		       lobby.id, lobby.state, COALESCE(lobby.current_attempt_id, ''),
		       lobby.roster_revision,
		       attempt.id, attempt.state, attempt.roster_revision,
		       attempt.route_generation
		FROM p2p_rooms AS room
		JOIN match_lobbies AS lobby
		  ON lobby.id = room.managed_lobby_id
		JOIN match_attempts AS attempt
		  ON attempt.id = lobby.current_attempt_id
		 AND attempt.lobby_id = lobby.id
		WHERE room.id = $1
		  AND room.deleted_at IS NULL
		  AND room.managed_lobby_id = $2
		  AND attempt.state IN ('PROVISIONING', 'CONNECTING', 'RUNNING')
		FOR UPDATE OF attempt, lobby, room
	`, item.RoomID, observedManagedLobbyID).Scan(
		&hostPlayerID, &roomState, &lobbyID, &lobbyState, &currentAttemptID,
		&lobbyRosterRevision, &attemptID, &attemptState, &attemptRosterRevision,
		&routeGeneration,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: no current active attempt", errManagedConnectionScope)
		}
		return err
	}
	if hostPlayerID != item.HostPlayerID ||
		currentAttemptID != attemptID ||
		lobbyID != observedManagedLobbyID ||
		lobbyRosterRevision != attemptRosterRevision ||
		!connectionRoomStateActive(roomState) ||
		!connectionLobbyStateActive(lobbyState) ||
		(attemptState != "PROVISIONING" && attemptState != "CONNECTING" && attemptState != "RUNNING") ||
		attemptRosterRevision <= 0 || routeGeneration <= 0 {
		return fmt.Errorf("%w: managed room state changed", errManagedConnectionScope)
	}
	if expected != nil && (expected.LobbyID != lobbyID || expected.AttemptID != attemptID ||
		expected.RosterRevision != attemptRosterRevision || expected.RouteGeneration != routeGeneration) {
		return fmt.Errorf("%w: attempt route or roster revision changed", errManagedConnectionScope)
	}
	return r.lockConnectionRoomParticipants(ctx, tx, item.RoomID, item.HostPlayerID, item.PeerPlayerID, attemptID)
}

func connectionRoomStateActive(state string) bool {
	switch state {
	case "LOBBY", "CONNECTING", "RUNNING":
		return true
	default:
		return false
	}
}

func connectionLobbyStateActive(state string) bool {
	switch state {
	case "PROVISIONING", "CONNECTING", "RUNNING":
		return true
	default:
		return false
	}
}

func (r *Repository) lockConnectionRoomParticipants(
	ctx context.Context,
	tx pgx.Tx,
	roomID, hostPlayerID, peerPlayerID, attemptID string,
) error {
	for _, participant := range []struct {
		playerID string
		role     string
	}{
		{playerID: hostPlayerID, role: "HOST"},
		{playerID: peerPlayerID, role: "MEMBER"},
	} {
		var role, status string
		if err := tx.QueryRow(ctx, `
			SELECT role, status
			FROM p2p_room_members
			WHERE room_id = $1 AND player_id = $2
			FOR UPDATE
		`, roomID, participant.playerID).Scan(&role, &status); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("%w: room membership is gone", errManagedConnectionScope)
			}
			return err
		}
		if role != participant.role || status != "ACTIVE" {
			return fmt.Errorf("%w: room membership is no longer active", errManagedConnectionScope)
		}
	}
	if attemptID == "" {
		return nil
	}
	for _, participant := range []struct {
		playerID string
		role     string
	}{
		{playerID: hostPlayerID, role: "HOST"},
		{playerID: peerPlayerID, role: "MEMBER"},
	} {
		var role string
		if err := tx.QueryRow(ctx, `
			SELECT room_role
			FROM match_attempt_roster
			WHERE attempt_id = $1 AND player_id = $2
			FOR UPDATE
		`, attemptID, participant.playerID).Scan(&role); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("%w: frozen roster seat is gone", errManagedConnectionScope)
			}
			return err
		}
		if role != participant.role {
			return fmt.Errorf("%w: frozen roster role changed", errManagedConnectionScope)
		}
	}
	return nil
}

func (r *Repository) Get(ctx context.Context, connectionID string) (Connection, error) {
	item, err := scanConnection(r.pool.QueryRow(ctx, `SELECT `+connectionColumns+` FROM connections WHERE id = $1`, connectionID))
	if err != nil {
		return Connection{}, err
	}
	item.Candidates, err = r.ListCandidates(ctx, connectionID)
	return item, err
}

func (r *Repository) GetForUpdate(ctx context.Context, tx pgx.Tx, connectionID string) (Connection, error) {
	return scanConnection(tx.QueryRow(ctx, `SELECT `+connectionColumns+` FROM connections WHERE id = $1 FOR UPDATE`, connectionID))
}

func (r *Repository) ListCandidates(ctx context.Context, connectionID string) ([]Candidate, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+candidateColumns+`
		FROM connection_candidates
		WHERE connection_id = $1
		ORDER BY priority DESC, id
	`, connectionID)
	if err != nil {
		return nil, fmt.Errorf("list connection candidates: %w", err)
	}
	defer rows.Close()
	items := make([]Candidate, 0)
	for rows.Next() {
		item, err := scanCandidate(rows)
		if err != nil {
			return nil, fmt.Errorf("scan connection candidate: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate connection candidates: %w", err)
	}
	return items, nil
}

func (r *Repository) GetCandidateForUpdate(
	ctx context.Context,
	tx pgx.Tx,
	connectionID, playerID, foundation string,
) (Candidate, error) {
	return scanCandidate(tx.QueryRow(ctx, `
		SELECT `+candidateColumns+`
		FROM connection_candidates
		WHERE connection_id = $1 AND player_id = $2 AND foundation = $3
		FOR UPDATE
	`, connectionID, playerID, foundation))
}

func (r *Repository) UpsertCandidate(ctx context.Context, tx pgx.Tx, item Candidate) (Candidate, error) {
	return scanCandidate(tx.QueryRow(ctx, `
		INSERT INTO connection_candidates (
			id, connection_id, player_id, foundation, candidate_type,
			protocol, address, port, priority, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7::inet, $8, $9, $10)
		ON CONFLICT (connection_id, player_id, foundation) DO UPDATE SET
			candidate_type = EXCLUDED.candidate_type,
			protocol = EXCLUDED.protocol,
			address = EXCLUDED.address,
			port = EXCLUDED.port,
			priority = EXCLUDED.priority
		RETURNING `+candidateColumns,
		item.ID, item.ConnectionID, item.PlayerID, item.Foundation, item.CandidateType,
		item.Protocol, item.Address, item.Port, item.Priority, item.CreatedAt,
	))
}

func (r *Repository) CandidateParticipantCount(ctx context.Context, tx pgx.Tx, connectionID string) (int, error) {
	var count int
	err := tx.QueryRow(ctx, `
		SELECT COUNT(DISTINCT player_id)
		FROM connection_candidates
		WHERE connection_id = $1
	`, connectionID).Scan(&count)
	return count, err
}

func (r *Repository) EligibleCandidateTypes(ctx context.Context, tx pgx.Tx, connectionID string) (map[CandidateType]bool, error) {
	rows, err := tx.Query(ctx, `
		SELECT candidate_type
		FROM connection_candidates
		WHERE connection_id = $1
		GROUP BY candidate_type
		HAVING COUNT(DISTINCT player_id) = 2
	`, connectionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[CandidateType]bool)
	for rows.Next() {
		var candidateType CandidateType
		if err := rows.Scan(&candidateType); err != nil {
			return nil, err
		}
		result[candidateType] = true
	}
	return result, rows.Err()
}

func (r *Repository) AttemptedPaths(ctx context.Context, tx pgx.Tx, connectionID string) (map[Path]bool, error) {
	rows, err := tx.Query(ctx, `SELECT path FROM connection_path_checks WHERE connection_id = $1`, connectionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[Path]bool)
	for rows.Next() {
		var path Path
		if err := rows.Scan(&path); err != nil {
			return nil, err
		}
		result[path] = true
	}
	return result, rows.Err()
}

func (r *Repository) RecordPathCheck(ctx context.Context, tx pgx.Tx, connectionID, playerID string, input CheckResultInput, reason string, now time.Time) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO connection_path_checks (
			connection_id, path, reporter_player_id, success, latency_ms, reason, checked_at
		) VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), $7)
		ON CONFLICT (connection_id, path) DO NOTHING
	`, connectionID, input.Path, playerID, input.Success, input.LatencyMS, reason, now)
	return err
}

func (r *Repository) UpdateState(
	ctx context.Context,
	tx pgx.Tx,
	connectionID string,
	state State,
	selectedPath Path,
	failureReason string,
	now time.Time,
) (Connection, error) {
	return scanConnection(tx.QueryRow(ctx, `
		UPDATE connections
		SET state = $2::varchar(32),
		    selected_path = NULLIF($3::varchar(32), ''),
		    failure_reason = NULLIF($4::varchar(128), ''),
		    updated_at = $5,
		    closed_at = CASE WHEN $2::varchar(32) IN ('FAILED', 'EXPIRED', 'CLOSED') THEN COALESCE(closed_at, $5) ELSE closed_at END
		WHERE id = $1
		RETURNING `+connectionColumns,
		connectionID, state, selectedPath, failureReason, now,
	))
}

func (r *Repository) SweepExpired(ctx context.Context, now time.Time) ([]Connection, error) {
	rows, err := r.pool.Query(ctx, `
		UPDATE connections
		SET state = 'EXPIRED', failure_reason = 'SESSION_TTL_EXPIRED',
		    closed_at = COALESCE(closed_at, $1), updated_at = $1
		WHERE expires_at <= $1 AND state NOT IN ('FAILED', 'EXPIRED', 'CLOSED')
		RETURNING `+connectionColumns,
		now,
	)
	if err != nil {
		return nil, fmt.Errorf("expire connections: %w", err)
	}
	defer rows.Close()
	items := make([]Connection, 0)
	for rows.Next() {
		item, err := scanConnection(rows)
		if err != nil {
			return nil, fmt.Errorf("scan expired connection: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate expired connections: %w", err)
	}
	return items, nil
}

func (r *Repository) RenewForRoom(
	ctx context.Context,
	tx pgx.Tx,
	roomID string,
	expiresAt time.Time,
	renewBefore time.Time,
	now time.Time,
) error {
	_, err := tx.Exec(ctx, `
		UPDATE connections
		SET expires_at = $2, updated_at = $4
		WHERE room_id = $1
		  AND state NOT IN ('FAILED', 'EXPIRED', 'CLOSED')
		  AND expires_at < $3
	`, roomID, expiresAt, renewBefore, now)
	if err != nil {
		return fmt.Errorf("renew room connections: %w", err)
	}
	return nil
}

func (r *Repository) CloseForRoom(ctx context.Context, roomID, reason string, now time.Time) ([]Connection, error) {
	rows, err := r.pool.Query(ctx, `
		UPDATE connections
		SET state = 'CLOSED', failure_reason = NULLIF($2, ''),
		    closed_at = COALESCE(closed_at, $3), updated_at = $3
		WHERE room_id = $1 AND state NOT IN ('FAILED', 'EXPIRED', 'CLOSED')
		RETURNING `+connectionColumns,
		roomID, reason, now,
	)
	if err != nil {
		return nil, fmt.Errorf("close room connections: %w", err)
	}
	defer rows.Close()
	items := make([]Connection, 0)
	for rows.Next() {
		item, err := scanConnection(rows)
		if err != nil {
			return nil, fmt.Errorf("scan room connection closure: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate room connection closures: %w", err)
	}
	return items, nil
}

func (r *Repository) CloseForRoomMember(
	ctx context.Context,
	roomID, playerID, reason string,
	now time.Time,
) ([]Connection, error) {
	rows, err := r.pool.Query(ctx, `
		UPDATE connections
		SET state = 'CLOSED', failure_reason = NULLIF($3, ''),
		    closed_at = COALESCE(closed_at, $4), updated_at = $4
		WHERE room_id = $1 AND peer_player_id = $2
		  AND state NOT IN ('FAILED', 'EXPIRED', 'CLOSED')
		RETURNING `+connectionColumns,
		roomID, playerID, reason, now,
	)
	if err != nil {
		return nil, fmt.Errorf("close room member connections: %w", err)
	}
	defer rows.Close()
	items := make([]Connection, 0)
	for rows.Next() {
		item, err := scanConnection(rows)
		if err != nil {
			return nil, fmt.Errorf("scan room member connection closure: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate room member connection closures: %w", err)
	}
	return items, nil
}

const connectionColumns = `
	id, room_id, host_player_id, peer_player_id, state,
	selected_path, failure_reason, expires_at, created_at, updated_at, closed_at
`

const candidateColumns = `
	id, connection_id, player_id, foundation, candidate_type,
	protocol, host(address), port, priority, created_at
`

func scanConnection(row pgx.Row) (Connection, error) {
	var item Connection
	var selectedPath sql.NullString
	var failureReason sql.NullString
	var closedAt sql.NullTime
	err := row.Scan(
		&item.ID, &item.RoomID, &item.HostPlayerID, &item.PeerPlayerID, &item.State,
		&selectedPath, &failureReason, &item.ExpiresAt, &item.CreatedAt, &item.UpdatedAt, &closedAt,
	)
	if selectedPath.Valid {
		item.SelectedPath = Path(selectedPath.String)
	}
	if failureReason.Valid {
		item.FailureReason = failureReason.String
	}
	if closedAt.Valid {
		item.ClosedAt = &closedAt.Time
	}
	return item, err
}

func scanCandidate(row pgx.Row) (Candidate, error) {
	var item Candidate
	err := row.Scan(
		&item.ID, &item.ConnectionID, &item.PlayerID, &item.Foundation, &item.CandidateType,
		&item.Protocol, &item.Address, &item.Port, &item.Priority, &item.CreatedAt,
	)
	return item, err
}
