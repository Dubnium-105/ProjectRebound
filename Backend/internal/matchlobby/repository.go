package matchlobby

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

func (r *Repository) LockIdempotency(ctx context.Context, tx pgx.Tx, ownerID, key string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1), hashtext($2))`, ownerID, key)
	return err
}

func (r *Repository) LockPlayerLobby(ctx context.Context, tx pgx.Tx, playerID string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('match-lobby-player'), hashtext($1))`, playerID)
	return err
}

func (r *Repository) ActiveLobbyForPlayer(ctx context.Context, tx pgx.Tx, playerID, exceptLobbyID string) (string, error) {
	var lobbyID string
	err := tx.QueryRow(ctx, `
		SELECT lobby.id
		FROM match_lobby_members AS member
		JOIN match_lobbies AS lobby ON lobby.id = member.lobby_id
		WHERE member.player_id = $1 AND member.membership_state = 'ACTIVE'
		  AND lobby.id <> $2
		  AND lobby.state IN ('OPEN', 'FROZEN', 'PROVISIONING', 'CONNECTING', 'RUNNING')
		ORDER BY lobby.created_at
		LIMIT 1
	`, playerID, exceptLobbyID).Scan(&lobbyID)
	return lobbyID, err
}

func (r *Repository) FindIdempotent(ctx context.Context, tx pgx.Tx, ownerID, key string) (Lobby, error) {
	return scanLobby(tx.QueryRow(ctx, `
		SELECT `+lobbyColumns+`
		FROM match_lobbies
		WHERE owner_player_id = $1 AND idempotency_key = $2
		FOR UPDATE
	`, ownerID, key))
}

func (r *Repository) InsertLobby(ctx context.Context, tx pgx.Tx, lobby Lobby, ownerTeam, ownerSlot int, presenceExpires time.Time) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO match_lobbies (
			id, owner_player_id, display_name, hosting_kind, transport_kind,
			mode, region, client_version, protocol_version,
			team_one_capacity, team_two_capacity, state, roster_revision,
			idempotency_key, idempotency_request_hash, created_at, updated_at
		) VALUES (
			$1, $2, $3, $4, NULLIF($5, ''), $6, $7, $8, $9,
			$10, $11, 'OPEN', 1, NULLIF($12, ''), $13, $14, $14
		)
	`, lobby.ID, lobby.OwnerPlayerID, lobby.DisplayName, lobby.HostingKind,
		lobby.TransportKind, lobby.Mode, lobby.Region, lobby.ClientVersion,
		lobby.ProtocolVersion, lobby.TeamOneCapacity, lobby.TeamTwoCapacity,
		lobby.IdempotencyKey, lobby.IdempotencyHash, lobby.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert match lobby: %w", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO match_lobby_members (
			lobby_id, player_id, role, team_id, team_slot, ready,
			presence_state, presence_expires_at, membership_state,
			joined_at, last_seen_at
		) VALUES ($1, $2, 'OWNER', $3, $4, FALSE, 'ONLINE', $5, 'ACTIVE', $6, $6)
	`, lobby.ID, lobby.OwnerPlayerID, ownerTeam, ownerSlot, presenceExpires, lobby.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert match lobby owner: %w", err)
	}
	return nil
}

func (r *Repository) LinkP2PRoom(ctx context.Context, lobbyID, roomID string, now time.Time) error {
	command, err := r.pool.Exec(ctx, `
		UPDATE match_lobbies
		SET p2p_room_id = $2, updated_at = $3
		WHERE id = $1 AND hosting_kind = 'P2P' AND state = 'OPEN'
	`, lobbyID, roomID, now)
	if err != nil {
		return fmt.Errorf("link managed P2P room: %w", err)
	}
	if command.RowsAffected() != 1 {
		return errors.New("match lobby was no longer open while linking P2P room")
	}
	return nil
}

func (r *Repository) DeleteUnlinkedLobby(ctx context.Context, lobbyID string) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	_, _ = tx.Exec(ctx, `DELETE FROM p2p_rooms WHERE managed_lobby_id = $1`, lobbyID)
	_, _ = tx.Exec(ctx, `DELETE FROM match_lobbies WHERE id = $1 AND p2p_room_id IS NULL AND state = 'OPEN'`, lobbyID)
	_ = tx.Commit(ctx)
}

func (r *Repository) GetLobby(ctx context.Context, lobbyID string) (Lobby, error) {
	return scanLobby(r.pool.QueryRow(ctx, `SELECT `+lobbyColumns+` FROM match_lobbies WHERE id = $1`, lobbyID))
}

func (r *Repository) GetLobbyForUpdate(ctx context.Context, tx pgx.Tx, lobbyID string) (Lobby, error) {
	return scanLobby(tx.QueryRow(ctx, `SELECT `+lobbyColumns+` FROM match_lobbies WHERE id = $1 FOR UPDATE`, lobbyID))
}

func (r *Repository) NextTeamSlot(ctx context.Context, tx pgx.Tx, lobby Lobby, teamID int) (int, error) {
	capacity := lobby.TeamOneCapacity
	if teamID == 2 {
		capacity = lobby.TeamTwoCapacity
	}
	var slot int
	err := tx.QueryRow(ctx, `
		SELECT candidate
		FROM generate_series(0, $3 - 1) AS candidate
		WHERE NOT EXISTS (
			SELECT 1 FROM match_lobby_members
			WHERE lobby_id = $1 AND team_id = $2 AND team_slot = candidate
			  AND membership_state = 'ACTIVE'
		)
		ORDER BY candidate
		LIMIT 1
	`, lobby.ID, teamID, capacity).Scan(&slot)
	return slot, err
}

func (r *Repository) GetMemberForUpdate(ctx context.Context, tx pgx.Tx, lobbyID, playerID string) (Member, string, error) {
	var item Member
	var membershipState string
	err := tx.QueryRow(ctx, `
		SELECT member.player_id, player.persona_name, member.role, member.team_id,
		       member.team_slot, member.ready, member.presence_state,
		       member.presence_expires_at, member.joined_at, member.membership_state
		FROM match_lobby_members AS member
		JOIN players AS player ON player.id = member.player_id
		WHERE member.lobby_id = $1 AND member.player_id = $2
		FOR UPDATE OF member
	`, lobbyID, playerID).Scan(
		&item.PlayerID, &item.DisplayName, &item.Role, &item.TeamID, &item.TeamSlot,
		&item.Ready, &item.PresenceState, &item.PresenceExpires, &item.JoinedAt,
		&membershipState,
	)
	return item, membershipState, err
}

func (r *Repository) UpsertMember(ctx context.Context, tx pgx.Tx, lobbyID, playerID string, teamID, teamSlot int, now, presenceExpires time.Time) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO match_lobby_members (
			lobby_id, player_id, role, team_id, team_slot, ready,
			presence_state, presence_expires_at, membership_state,
			joined_at, last_seen_at, left_at
		) VALUES ($1, $2, 'MEMBER', $3, $4, FALSE, 'ONLINE', $5, 'ACTIVE', $6, $6, NULL)
		ON CONFLICT (lobby_id, player_id) DO UPDATE SET
			role = 'MEMBER', team_id = EXCLUDED.team_id, team_slot = EXCLUDED.team_slot,
			ready = FALSE, presence_state = 'ONLINE',
			presence_expires_at = EXCLUDED.presence_expires_at,
			membership_state = 'ACTIVE', joined_at = EXCLUDED.joined_at,
			last_seen_at = EXCLUDED.last_seen_at, left_at = NULL
	`, lobbyID, playerID, teamID, teamSlot, presenceExpires, now)
	if err != nil {
		return fmt.Errorf("upsert match lobby member: %w", err)
	}
	return nil
}

func (r *Repository) Snapshot(ctx context.Context, lobbyID, viewerPlayerID string, now time.Time) (Snapshot, error) {
	lobby, err := r.GetLobby(ctx, lobbyID)
	if err != nil {
		return Snapshot{}, err
	}
	rows, err := r.pool.Query(ctx, `
		SELECT member.player_id, player.persona_name, member.role, member.team_id,
		       member.team_slot, member.ready,
		       CASE WHEN member.presence_state = 'ONLINE' AND member.presence_expires_at > $2
		            THEN 'ONLINE' ELSE 'OFFLINE' END,
		       member.presence_expires_at, member.joined_at
		FROM match_lobby_members AS member
		JOIN players AS player ON player.id = member.player_id
		WHERE member.lobby_id = $1 AND member.membership_state = 'ACTIVE'
		ORDER BY member.team_id, member.team_slot, member.player_id
	`, lobbyID, now)
	if err != nil {
		return Snapshot{}, fmt.Errorf("list match lobby members: %w", err)
	}
	defer rows.Close()
	teams := []TeamView{{TeamID: 1, Capacity: lobby.TeamOneCapacity, Members: []Member{}}, {TeamID: 2, Capacity: lobby.TeamTwoCapacity, Members: []Member{}}}
	local := LocalCapabilities{}
	allReadyOnline := true
	for rows.Next() {
		var item Member
		if err := rows.Scan(
			&item.PlayerID, &item.DisplayName, &item.Role, &item.TeamID, &item.TeamSlot,
			&item.Ready, &item.PresenceState, &item.PresenceExpires, &item.JoinedAt,
		); err != nil {
			return Snapshot{}, fmt.Errorf("scan match lobby member: %w", err)
		}
		teams[item.TeamID-1].Members = append(teams[item.TeamID-1].Members, item)
		if !item.Ready || item.PresenceState != "ONLINE" {
			allReadyOnline = false
		}
		if item.PlayerID == viewerPlayerID {
			local.IsMember = true
			local.IsOwner = item.Role == "OWNER"
		}
	}
	if err := rows.Err(); err != nil {
		return Snapshot{}, fmt.Errorf("iterate match lobby members: %w", err)
	}
	if viewerPlayerID != "" {
		local.CanJoin = lobby.State == StateOpen && !local.IsMember &&
			(len(teams[0].Members) < teams[0].Capacity || len(teams[1].Members) < teams[1].Capacity)
		local.CanSwitchTeam = lobby.State == StateOpen && local.IsMember
		local.CanSetReady = lobby.State == StateOpen && local.IsMember
		local.CanLeave = lobby.State == StateOpen && local.IsMember
		local.CanStart = lobby.State == StateOpen && local.IsOwner && allReadyOnline &&
			len(teams[0].Members) > 0 && len(teams[1].Members) > 0
		local.CanRetry = local.IsMember &&
			!(lobby.HostingKind == HostingP2P && local.IsOwner) &&
			(lobby.State == StateConnecting || lobby.State == StateRunning)
	}
	snapshot := Snapshot{
		LobbyID: lobby.ID, OwnerPlayerID: lobby.OwnerPlayerID, P2PRoomID: lobby.P2PRoomID,
		DisplayName: lobby.DisplayName, HostingKind: lobby.HostingKind,
		TransportKind: lobby.TransportKind, Mode: lobby.Mode, Region: lobby.Region,
		ClientVersion: lobby.ClientVersion, ProtocolVersion: lobby.ProtocolVersion,
		State: lobby.State, RosterRevision: lobby.RosterRevision, Teams: teams,
		Local: local, CreatedAt: lobby.CreatedAt, UpdatedAt: lobby.UpdatedAt,
	}
	if lobby.CurrentAttemptID != "" {
		attempt, err := r.getAttempt(ctx, lobby.CurrentAttemptID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return Snapshot{}, err
		}
		if err == nil {
			snapshot.Attempt = &attempt
		}
	}
	return snapshot, nil
}

func (r *Repository) List(ctx context.Context, filter ListFilter) (ListResult, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT lobby.id, lobby.owner_player_id, lobby.display_name, lobby.hosting_kind,
		       COALESCE(lobby.transport_kind, ''), lobby.mode, lobby.region,
		       lobby.client_version, lobby.state, COUNT(member.player_id)::integer,
		       lobby.team_one_capacity + lobby.team_two_capacity,
		       lobby.roster_revision, lobby.created_at
		FROM match_lobbies AS lobby
		LEFT JOIN match_lobby_members AS member
		  ON member.lobby_id = lobby.id AND member.membership_state = 'ACTIVE'
		WHERE lobby.state = 'OPEN'
		  AND (lobby.hosting_kind <> 'P2P' OR lobby.p2p_room_id IS NOT NULL)
		  AND ($1 = '' OR lobby.id > $1)
		  AND ($2 = '' OR lobby.hosting_kind = $2)
		  AND ($3 = '' OR lobby.region = $3)
		  AND ($4 = '' OR lobby.mode = $4)
		  AND ($5 = '' OR lobby.client_version = $5)
		GROUP BY lobby.id
		HAVING COUNT(member.player_id) < lobby.team_one_capacity + lobby.team_two_capacity
		ORDER BY lobby.id
		LIMIT $6
	`, filter.Cursor, filter.HostingKind, filter.Region, filter.Mode, filter.ClientVersion, filter.Limit+1)
	if err != nil {
		return ListResult{}, fmt.Errorf("list match lobbies: %w", err)
	}
	defer rows.Close()
	items := make([]Summary, 0, filter.Limit+1)
	for rows.Next() {
		var item Summary
		if err := rows.Scan(
			&item.LobbyID, &item.OwnerPlayerID, &item.DisplayName, &item.HostingKind,
			&item.TransportKind, &item.Mode, &item.Region, &item.ClientVersion,
			&item.State, &item.PlayerCount, &item.Capacity, &item.RosterRevision,
			&item.CreatedAt,
		); err != nil {
			return ListResult{}, fmt.Errorf("scan match lobby summary: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return ListResult{}, fmt.Errorf("iterate match lobbies: %w", err)
	}
	result := ListResult{Items: items}
	if len(result.Items) > filter.Limit {
		result.NextCursor = result.Items[filter.Limit-1].LobbyID
		result.Items = result.Items[:filter.Limit]
	}
	return result, nil
}

func (r *Repository) getAttempt(ctx context.Context, attemptID string) (AttemptView, error) {
	var item AttemptView
	var deadline sql.NullTime
	err := r.pool.QueryRow(ctx, `
		SELECT id, attempt_number, state, roster_revision,
		       route_generation, COALESCE(endpoint_host, ''), COALESCE(endpoint_port, 0),
		       COALESCE(payload_installed_at IS NOT NULL
		         AND payload_route_generation = route_generation, FALSE),
		       connection_deadline, COALESCE(failure_code, '')
		FROM match_attempts WHERE id = $1
	`, attemptID).Scan(
		&item.AttemptID, &item.AttemptNumber, &item.State, &item.RosterRevision,
		&item.RouteGeneration, &item.EndpointHost, &item.EndpointPort,
		&item.PayloadInstalled, &deadline, &item.FailureCode,
	)
	if deadline.Valid {
		item.ConnectionDeadline = &deadline.Time
	}
	return item, err
}

func (r *Repository) FrozenRoster(ctx context.Context, executor interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, attemptID string) ([]FrozenRosterMember, error) {
	rows, err := executor.Query(ctx, `
		SELECT player_id, platform_id, display_name, room_role, team_id,
		       team_slot, logical_slot, connection_generation
		FROM match_attempt_roster
		WHERE attempt_id = $1
		ORDER BY logical_slot
	`, attemptID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]FrozenRosterMember, 0)
	for rows.Next() {
		var item FrozenRosterMember
		if err := rows.Scan(
			&item.PlayerID, &item.PlatformID, &item.DisplayName, &item.RoomRole,
			&item.TeamID, &item.TeamSlot, &item.LogicalSlot, &item.ConnectionGeneration,
		); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

const lobbyColumns = `
	id, owner_player_id, COALESCE(p2p_room_id, ''), display_name,
	hosting_kind, COALESCE(transport_kind, ''), mode, region, client_version,
	protocol_version, team_one_capacity, team_two_capacity, state,
	roster_revision, COALESCE(current_attempt_id, ''), COALESCE(idempotency_key, ''),
	COALESCE(idempotency_request_hash, ''::bytea), created_at, updated_at, closed_at
`

func scanLobby(row pgx.Row) (Lobby, error) {
	var item Lobby
	var closedAt sql.NullTime
	err := row.Scan(
		&item.ID, &item.OwnerPlayerID, &item.P2PRoomID, &item.DisplayName,
		&item.HostingKind, &item.TransportKind, &item.Mode, &item.Region,
		&item.ClientVersion, &item.ProtocolVersion, &item.TeamOneCapacity,
		&item.TeamTwoCapacity, &item.State, &item.RosterRevision,
		&item.CurrentAttemptID, &item.IdempotencyKey, &item.IdempotencyHash,
		&item.CreatedAt, &item.UpdatedAt, &closedAt,
	)
	if closedAt.Valid {
		item.ClosedAt = &closedAt.Time
	}
	return item, err
}
