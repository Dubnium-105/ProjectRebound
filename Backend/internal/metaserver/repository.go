package metaserver

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct {
	pool                *pgxpool.Pool
	now                 func() time.Time
	gameServerFreshness time.Duration
	metrics             *MetaMetrics
}

type metaRowQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func (r *Repository) SetMetrics(metrics *MetaMetrics) { r.metrics = metrics }

func NewRepository(pool *pgxpool.Pool, gameServerFreshness time.Duration) *Repository {
	return &Repository{
		pool: pool, now: time.Now, gameServerFreshness: gameServerFreshness,
	}
}

func (r *Repository) IsAuthSessionActive(
	ctx context.Context,
	playerID, sessionID string,
) (bool, error) {
	var active bool
	err := r.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM auth_sessions AS session
			JOIN players AS player ON player.id = session.player_id
			WHERE session.id = $1 AND session.player_id = $2
			  AND session.revoked_at IS NULL AND session.expires_at > $3
			  AND player.account_status = 'ACTIVE'
		)
	`, sessionID, playerID, r.now().UTC()).Scan(&active)
	return active, err
}

func newMetaID(prefix string) string {
	return prefix + strings.ReplaceAll(uuid.NewString(), "-", "")
}

func (r *Repository) GetOrCreateProfile(ctx context.Context, playerID string) (Profile, error) {
	now := r.now().UTC()
	row := r.pool.QueryRow(ctx, `
		INSERT INTO meta_player_profiles (
			player_id, level, experience, currencies, statistics,
			revision, created_at, updated_at
		) VALUES ($1, 1, 0, '{}'::jsonb, '{}'::jsonb, 1, $2, $2)
		ON CONFLICT (player_id) DO UPDATE SET player_id = EXCLUDED.player_id
		RETURNING player_id, level, experience, currencies, statistics,
		          revision, created_at, updated_at
	`, playerID, now)
	var item Profile
	if err := row.Scan(
		&item.PlayerID, &item.Level, &item.Experience, &item.Currencies,
		&item.Statistics, &item.Revision, &item.CreatedAt, &item.UpdatedAt,
	); err != nil {
		return Profile{}, fmt.Errorf("get or create meta profile: %w", err)
	}
	return item, nil
}

func (r *Repository) ListLoadouts(ctx context.Context, playerID string) ([]Loadout, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT player_id, role_id, snapshot, revision, updated_at
		FROM meta_role_loadouts
		WHERE player_id = $1
		ORDER BY role_id
	`, playerID)
	if err != nil {
		return nil, fmt.Errorf("list meta loadouts: %w", err)
	}
	defer rows.Close()
	items := make([]Loadout, 0)
	for rows.Next() {
		item, err := scanLoadout(rows)
		if err != nil {
			return nil, fmt.Errorf("scan meta loadout: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate meta loadouts: %w", err)
	}
	return items, nil
}

func (r *Repository) GetLoadout(ctx context.Context, playerID, roleID string) (Loadout, error) {
	return r.getLoadout(ctx, r.pool, playerID, roleID)
}

func (r *Repository) getLoadout(
	ctx context.Context,
	queries metaRowQuerier,
	playerID, roleID string,
) (Loadout, error) {
	item, err := scanLoadout(queries.QueryRow(ctx, `
		SELECT player_id, role_id, snapshot, revision, updated_at
		FROM meta_role_loadouts
		WHERE player_id = $1 AND role_id = $2
	`, playerID, roleID))
	if err != nil {
		return Loadout{}, normalizeRepositoryError(err, "META_LOADOUT_NOT_FOUND", "Loadout not found.")
	}
	return item, nil
}

func (r *Repository) PutLoadout(
	ctx context.Context,
	playerID, roleID string,
	snapshot json.RawMessage,
	digest []byte,
	expectedRevision int64,
) (Loadout, error) {
	return r.putLoadout(
		ctx, r.pool, playerID, roleID, snapshot, digest, expectedRevision,
	)
}

func (r *Repository) putLoadout(
	ctx context.Context,
	queries metaRowQuerier,
	playerID, roleID string,
	snapshot json.RawMessage,
	digest []byte,
	expectedRevision int64,
) (Loadout, error) {
	now := r.now().UTC()
	item, err := scanLoadout(queries.QueryRow(ctx, `
		WITH updated AS (
			UPDATE meta_role_loadouts
			SET snapshot = $3::jsonb,
			    snapshot_sha256 = $4,
			    revision = revision + 1,
			    updated_at = $6
			WHERE player_id = $1 AND role_id = $2
			  AND revision = $5 AND $5 > 0
			RETURNING player_id, role_id, snapshot, revision, updated_at
		),
		inserted AS (
			INSERT INTO meta_role_loadouts (
				player_id, role_id, snapshot, snapshot_sha256,
				revision, created_at, updated_at
			)
			SELECT $1, $2, $3::jsonb, $4, 1, $6, $6
			WHERE $5 = 0
			ON CONFLICT (player_id, role_id) DO NOTHING
			RETURNING player_id, role_id, snapshot, revision, updated_at
		)
		SELECT player_id, role_id, snapshot, revision, updated_at FROM updated
		UNION ALL
		SELECT player_id, role_id, snapshot, revision, updated_at FROM inserted
	`, playerID, roleID, snapshot, digest, expectedRevision, now))
	if errors.Is(err, pgx.ErrNoRows) {
		if r.metrics != nil {
			r.metrics.LoadoutConflict()
		}
		return Loadout{}, conflict("META_LOADOUT_REVISION_CONFLICT", "The loadout was updated by another request.")
	}
	if err != nil {
		return Loadout{}, internalError(fmt.Errorf("put meta loadout: %w", err))
	}
	return item, nil
}

func scanLoadout(row pgx.Row) (Loadout, error) {
	var item Loadout
	err := row.Scan(&item.PlayerID, &item.RoleID, &item.Snapshot, &item.Revision, &item.UpdatedAt)
	return item, err
}

func (r *Repository) UpsertWeaponArchive(
	ctx context.Context,
	playerID, weaponID string,
	raw []byte,
	decoded json.RawMessage,
) error {
	digest := sha256.Sum256(raw)
	now := r.now().UTC()
	_, err := r.pool.Exec(ctx, `
		INSERT INTO meta_weapon_archives (
			id, player_id, weapon_id, raw_protobuf, decoded, protobuf_sha256,
			revision, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5::jsonb, $6, 1, $7, $7)
		ON CONFLICT (player_id, weapon_id) DO UPDATE SET
			raw_protobuf = EXCLUDED.raw_protobuf,
			decoded = EXCLUDED.decoded,
			protobuf_sha256 = EXCLUDED.protobuf_sha256,
			revision = meta_weapon_archives.revision + 1,
			updated_at = EXCLUDED.updated_at
	`, newMetaID("mwa_"), playerID, weaponID, raw, decoded, digest[:], now)
	if err != nil {
		return internalError(fmt.Errorf("upsert meta weapon archive: %w", err))
	}
	return nil
}

func (r *Repository) GetWeaponArchives(
	ctx context.Context,
	playerID string,
	weaponIDs []string,
) (map[string][]byte, error) {
	result := make(map[string][]byte)
	if len(weaponIDs) == 0 {
		return result, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT weapon_id, raw_protobuf
		FROM meta_weapon_archives
		WHERE player_id = $1 AND weapon_id = ANY($2::varchar[])
	`, playerID, weaponIDs)
	if err != nil {
		return nil, internalError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var weaponID string
		var raw []byte
		if err := rows.Scan(&weaponID, &raw); err != nil {
			return nil, internalError(err)
		}
		result[weaponID] = append([]byte(nil), raw...)
	}
	if err := rows.Err(); err != nil {
		return nil, internalError(err)
	}
	return result, nil
}

func (r *Repository) AuthorizeP2PRoomLoadoutRead(
	ctx context.Context,
	requesterPlayerID, roomID, targetPlayerID string,
) error {
	var hostPlayerID, state string
	var expiresAt time.Time
	var memberStatus string
	err := r.pool.QueryRow(ctx, `
		SELECT room.host_player_id, room.state, room.expires_at,
		       COALESCE(member.status, '')
		FROM p2p_rooms AS room
		LEFT JOIN p2p_room_members AS member
		  ON member.room_id = room.id AND member.player_id = $2
		WHERE room.id = $1
	`, roomID, targetPlayerID).Scan(
		&hostPlayerID, &state, &expiresAt, &memberStatus,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return notFound("META_P2P_ROOM_NOT_FOUND", "P2P room not found.")
	}
	if err != nil {
		return internalError(fmt.Errorf("authorize P2P room loadout read: %w", err))
	}
	if hostPlayerID != requesterPlayerID {
		return forbidden(
			"META_P2P_ROOM_HOST_REQUIRED",
			"Only the active P2P room host may read member loadouts.",
		)
	}
	if (state != "CONNECTING" && state != "RUNNING") || !expiresAt.After(r.now().UTC()) {
		return conflict(
			"META_P2P_ROOM_NOT_RUNNING",
			"The P2P room is not active for server-authoritative loadout reads.",
		)
	}
	if memberStatus != "ACTIVE" {
		return forbidden(
			"META_P2P_ROOM_MEMBER_INACTIVE",
			"The requested player is not an active member of this P2P room.",
		)
	}
	return nil
}

func (r *Repository) CreateParty(
	ctx context.Context,
	playerID, mode, region, clientVersion string,
	protocolVersion int,
) (Party, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Party{}, internalError(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	now := r.now().UTC()
	partyID := newMetaID("mp_")
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, playerID); err != nil {
		return Party{}, internalError(err)
	}
	var queued bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM meta_match_tickets
			WHERE player_id = $1 AND state = 'QUEUED'
		)
	`, playerID).Scan(&queued); err != nil {
		return Party{}, internalError(err)
	}
	if queued {
		return Party{}, conflict(
			"META_MATCH_TICKET_EXISTS",
			"The player cannot create a party while a match ticket is active.",
		)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO meta_parties (
			id, leader_player_id, state, mode, region, client_version,
			protocol_version, revision, created_at, updated_at
		) VALUES ($1, $2, 'ACTIVE', $3, $4, $5, $6, 1, $7, $7)
	`, partyID, playerID, mode, region, clientVersion, protocolVersion, now)
	if err == nil {
		_, err = tx.Exec(ctx, `
			INSERT INTO meta_party_members (
				party_id, player_id, role, ready, presence, joined_at, updated_at
			) VALUES ($1, $2, 'LEADER', FALSE, 'ONLINE', $3, $3)
		`, partyID, playerID, now)
	}
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return Party{}, conflict("META_PARTY_ALREADY_JOINED", "The player already belongs to an active party.")
		}
		return Party{}, internalError(fmt.Errorf("create meta party: %w", err))
	}
	if err := tx.Commit(ctx); err != nil {
		return Party{}, internalError(err)
	}
	return r.GetParty(ctx, partyID)
}

func (r *Repository) GetParty(ctx context.Context, partyID string) (Party, error) {
	var item Party
	err := r.pool.QueryRow(ctx, `
		SELECT id, leader_player_id, state, mode, region, client_version,
		       protocol_version, revision, created_at, updated_at
		FROM meta_parties
		WHERE id = $1
	`, partyID).Scan(
		&item.ID, &item.LeaderPlayerID, &item.State, &item.Mode, &item.Region,
		&item.ClientVersion, &item.ProtocolVersion, &item.Revision,
		&item.CreatedAt, &item.UpdatedAt,
	)
	if err != nil {
		return Party{}, normalizeRepositoryError(err, "META_PARTY_NOT_FOUND", "Party not found.")
	}
	rows, err := r.pool.Query(ctx, `
		SELECT player_id, role, ready, presence, joined_at, updated_at
		FROM meta_party_members
		WHERE party_id = $1 AND left_at IS NULL
		ORDER BY joined_at, player_id
	`, partyID)
	if err != nil {
		return Party{}, internalError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var member PartyMember
		if err := rows.Scan(
			&member.PlayerID, &member.Role, &member.Ready, &member.Presence,
			&member.JoinedAt, &member.UpdatedAt,
		); err != nil {
			return Party{}, internalError(err)
		}
		item.Members = append(item.Members, member)
	}
	if err := rows.Err(); err != nil {
		return Party{}, internalError(err)
	}
	return item, nil
}

func (r *Repository) UpdatePartyMember(
	ctx context.Context,
	partyID, playerID string,
	ready *bool,
	presence string,
) (Party, error) {
	now := r.now().UTC()
	tag, err := r.pool.Exec(ctx, `
		UPDATE meta_party_members
		SET ready = CASE WHEN $3::boolean IS NULL THEN ready ELSE $3 END,
		    presence = CASE WHEN $4 = '' THEN presence ELSE $4 END,
		    updated_at = $5
		WHERE party_id = $1 AND player_id = $2 AND left_at IS NULL
	`, partyID, playerID, ready, presence, now)
	if err != nil {
		return Party{}, internalError(err)
	}
	if tag.RowsAffected() == 0 {
		return Party{}, notFound("META_PARTY_MEMBERSHIP_NOT_FOUND", "Active party membership not found.")
	}
	_, err = r.pool.Exec(ctx, `
		UPDATE meta_parties SET revision = revision + 1, updated_at = $2 WHERE id = $1
	`, partyID, now)
	if err != nil {
		return Party{}, internalError(err)
	}
	return r.GetParty(ctx, partyID)
}

func (r *Repository) CreateTicket(
	ctx context.Context,
	playerID, partyID, mode, region, clientVersion string,
	protocolVersion int,
	ttl time.Duration,
) (MatchTicket, error) {
	now := r.now().UTC()
	item := MatchTicket{
		ID: newMetaID("mt_"), PlayerID: playerID, PartyID: partyID,
		Mode: mode, Region: region, ClientVersion: clientVersion,
		ProtocolVersion: protocolVersion, State: "QUEUED",
		ExpiresAt: now.Add(ttl), CreatedAt: now, UpdatedAt: now,
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return MatchTicket{}, internalError(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, playerID); err != nil {
		return MatchTicket{}, internalError(err)
	}

	if partyID == "" {
		var activePartyID string
		err = tx.QueryRow(ctx, `
			SELECT party_id
			FROM meta_party_members
			WHERE player_id = $1 AND left_at IS NULL
			FOR UPDATE
		`, playerID).Scan(&activePartyID)
		if err == nil {
			return MatchTicket{}, conflict(
				"META_PARTY_TICKET_REQUIRED",
				"Players in an active party must queue as the party.",
			)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return MatchTicket{}, internalError(err)
		}
	} else {
		var leaderID, state string
		err = tx.QueryRow(ctx, `
			SELECT leader_player_id, state
			FROM meta_parties
			WHERE id = $1
			FOR UPDATE
		`, partyID).Scan(&leaderID, &state)
		if errors.Is(err, pgx.ErrNoRows) {
			return MatchTicket{}, notFound("META_PARTY_NOT_FOUND", "Party not found.")
		}
		if err != nil {
			return MatchTicket{}, internalError(err)
		}
		if leaderID != playerID || state != "ACTIVE" {
			return MatchTicket{}, conflict(
				"META_PARTY_NOT_QUEUEABLE",
				"The party is not active or the player is not its leader.",
			)
		}
		rows, err := tx.Query(ctx, `
			SELECT player_id
			FROM meta_party_members
			WHERE party_id = $1 AND left_at IS NULL
			ORDER BY player_id
		`, partyID)
		if err != nil {
			return MatchTicket{}, internalError(err)
		}
		memberIDs := make([]string, 0)
		for rows.Next() {
			var memberID string
			if err := rows.Scan(&memberID); err != nil {
				rows.Close()
				return MatchTicket{}, internalError(err)
			}
			memberIDs = append(memberIDs, memberID)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return MatchTicket{}, internalError(err)
		}
		rows.Close()
		for _, memberID := range memberIDs {
			if _, err := tx.Exec(ctx, `
				SELECT pg_advisory_xact_lock(hashtextextended($1, 0))
			`, memberID); err != nil {
				return MatchTicket{}, internalError(err)
			}
		}
		lockedRows, err := tx.Query(ctx, `
			SELECT player_id
			FROM meta_party_members
			WHERE party_id = $1 AND left_at IS NULL
			ORDER BY player_id
			FOR UPDATE
		`, partyID)
		if err != nil {
			return MatchTicket{}, internalError(err)
		}
		lockedMemberCount := 0
		for lockedRows.Next() {
			var ignored string
			if err := lockedRows.Scan(&ignored); err != nil {
				lockedRows.Close()
				return MatchTicket{}, internalError(err)
			}
			lockedMemberCount++
		}
		if err := lockedRows.Err(); err != nil {
			lockedRows.Close()
			return MatchTicket{}, internalError(err)
		}
		lockedRows.Close()
		if lockedMemberCount != len(memberIDs) {
			return MatchTicket{}, conflict(
				"META_PARTY_NOT_QUEUEABLE",
				"The party membership changed while matchmaking started.",
			)
		}
		var queued bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM meta_match_tickets
				WHERE state = 'QUEUED'
				  AND (party_id = $1 OR player_id = ANY($2::varchar[]))
			)
		`, partyID, memberIDs).Scan(&queued); err != nil {
			return MatchTicket{}, internalError(err)
		}
		if queued {
			return MatchTicket{}, conflict(
				"META_MATCH_TICKET_EXISTS",
				"A party member already has an active match ticket.",
			)
		}
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO meta_match_tickets (
			id, player_id, party_id, mode, region, client_version,
			protocol_version, state, expires_at, created_at, updated_at
		) VALUES ($1, $2, NULLIF($3, ''), $4, $5, $6, $7, 'QUEUED', $8, $9, $9)
	`, item.ID, playerID, partyID, mode, region, clientVersion,
		protocolVersion, item.ExpiresAt, now,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return MatchTicket{}, conflict("META_MATCH_TICKET_EXISTS", "The player or party already has an active match ticket.")
		}
		return MatchTicket{}, internalError(fmt.Errorf("create meta match ticket: %w", err))
	}
	if partyID != "" {
		tag, err := tx.Exec(ctx, `
			UPDATE meta_parties
			SET state = 'MATCHMAKING', revision = revision + 1, updated_at = $2
			WHERE id = $1 AND state = 'ACTIVE'
		`, partyID, now)
		if err != nil {
			return MatchTicket{}, internalError(err)
		}
		if tag.RowsAffected() != 1 {
			return MatchTicket{}, conflict("META_PARTY_NOT_QUEUEABLE", "The party is not active.")
		}
	}
	if err := tx.Commit(ctx); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && (pgErr.Code == "23505" || pgErr.Code == "40001") {
			return MatchTicket{}, conflict(
				"META_MATCH_TICKET_EXISTS",
				"The player or party already has an active match ticket.",
			)
		}
		return MatchTicket{}, internalError(err)
	}
	return item, nil
}

func (r *Repository) GetTicket(ctx context.Context, ticketID, playerID string) (MatchTicket, error) {
	var item MatchTicket
	var completedAt *time.Time
	err := r.pool.QueryRow(ctx, `
		SELECT ticket.id, ticket.player_id, COALESCE(ticket.party_id, ''), ticket.mode,
		       ticket.region, ticket.client_version, ticket.protocol_version,
		       ticket.state, COALESCE(ticket.failure_code, ''),
		       COALESCE(ticket.matched_id, ''), ticket.expires_at, ticket.created_at,
		       ticket.updated_at, ticket.completed_at,
		       COALESCE(match.endpoint_host || ':' || match.endpoint_port::text, '')
		FROM meta_match_tickets AS ticket
		LEFT JOIN meta_matches AS match ON match.id = ticket.matched_id
		WHERE ticket.id = $1
		  AND (
		    ticket.player_id = $2 OR EXISTS (
		      SELECT 1
		      FROM meta_party_members AS member
		      WHERE member.party_id = ticket.party_id
		        AND member.player_id = $2
		        AND member.left_at IS NULL
		    )
		  )
	`, ticketID, playerID).Scan(
		&item.ID, &item.PlayerID, &item.PartyID, &item.Mode, &item.Region,
		&item.ClientVersion, &item.ProtocolVersion, &item.State, &item.FailureCode,
		&item.MatchID, &item.ExpiresAt, &item.CreatedAt, &item.UpdatedAt,
		&completedAt, &item.Endpoint,
	)
	item.CompletedAt = completedAt
	if err != nil {
		return MatchTicket{}, normalizeRepositoryError(err, "META_MATCH_TICKET_NOT_FOUND", "Match ticket not found.")
	}
	return item, nil
}

func (r *Repository) CancelTicket(ctx context.Context, ticketID, playerID string) error {
	now := r.now().UTC()
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return internalError(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var partyID string
	err = tx.QueryRow(ctx, `
		SELECT COALESCE(party_id, '')
		FROM meta_match_tickets
		WHERE id = $1 AND player_id = $2 AND state = 'QUEUED'
		FOR UPDATE
	`, ticketID, playerID).Scan(&partyID)
	if errors.Is(err, pgx.ErrNoRows) {
		return conflict("META_MATCH_TICKET_NOT_CANCELLABLE", "The match ticket is not queued.")
	}
	if err != nil {
		return internalError(err)
	}
	tag, err := tx.Exec(ctx, `
		UPDATE meta_match_tickets
		SET state = 'CANCELLED', completed_at = $3, updated_at = $3
		WHERE id = $1 AND player_id = $2 AND state = 'QUEUED'
	`, ticketID, playerID, now)
	if err != nil {
		return internalError(err)
	}
	if tag.RowsAffected() == 0 {
		return conflict("META_MATCH_TICKET_NOT_CANCELLABLE", "The match ticket is not queued.")
	}
	if partyID != "" {
		if _, err := tx.Exec(ctx, `
			UPDATE meta_parties
			SET state = 'ACTIVE', revision = revision + 1, updated_at = $2
			WHERE id = $1 AND state = 'MATCHMAKING'
		`, partyID, now); err != nil {
			return internalError(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return internalError(err)
	}
	return nil
}

func (r *Repository) ListRegions(ctx context.Context, freshness time.Duration) ([]Region, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT region, public_endpoints
		FROM relay_nodes
		WHERE state = 'READY'
		  AND load_state NOT IN ('REJECT_NEW', 'DRAINING')
		  AND last_heartbeat_at > $1
		  AND (lease_expires_at IS NULL OR lease_expires_at > $2)
		ORDER BY region, id
	`, r.now().UTC().Add(-freshness), r.now().UTC())
	if err != nil {
		return nil, fmt.Errorf("list meta regions: %w", err)
	}
	defer rows.Close()
	byRegion := make(map[string]*Region)
	order := make([]string, 0)
	for rows.Next() {
		var region string
		var raw json.RawMessage
		if err := rows.Scan(&region, &raw); err != nil {
			return nil, err
		}
		current := byRegion[region]
		if current == nil {
			current = &Region{ID: region, Name: region, Endpoints: []Endpoint{}}
			byRegion[region] = current
			order = append(order, region)
		}
		var endpoints []Endpoint
		if err := json.Unmarshal(raw, &endpoints); err == nil {
			for _, endpoint := range endpoints {
				if strings.EqualFold(endpoint.Protocol, "UDP") && strings.TrimSpace(endpoint.Host) != "" && endpoint.Port > 0 {
					current.Endpoints = append(current.Endpoints, endpoint)
				}
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	result := make([]Region, 0, len(order))
	for _, region := range order {
		result = append(result, *byRegion[region])
	}
	return result, nil
}

func (r *Repository) IsActivePartyMember(ctx context.Context, partyID, playerID string) (bool, error) {
	var exists bool
	err := r.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM meta_party_members
			WHERE party_id = $1 AND player_id = $2 AND left_at IS NULL
		)
	`, partyID, playerID).Scan(&exists)
	return exists, err
}

func (r *Repository) IsPartyLeader(ctx context.Context, partyID, playerID string) (bool, error) {
	var exists bool
	err := r.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM meta_parties
			WHERE id = $1 AND leader_player_id = $2 AND state = 'ACTIVE'
		)
	`, partyID, playerID).Scan(&exists)
	return exists, err
}

func (r *Repository) ListPlaylists(ctx context.Context) ([]Playlist, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, slug, display_name, description, mode, definition, sort_order, updated_at
		FROM meta_playlists WHERE enabled = TRUE ORDER BY sort_order, slug
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]Playlist, 0)
	for rows.Next() {
		var item Playlist
		if err := rows.Scan(
			&item.ID, &item.Slug, &item.DisplayName, &item.Description,
			&item.Mode, &item.Definition, &item.SortOrder, &item.UpdatedAt,
		); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *Repository) ListNotifications(ctx context.Context, locale string) ([]Notification, error) {
	now := r.now().UTC()
	rows, err := r.pool.Query(ctx, `
		SELECT id, title, body, locale, priority, starts_at, ends_at, updated_at
		FROM meta_notifications
		WHERE enabled = TRUE AND locale IN ($1, 'en')
		  AND (starts_at IS NULL OR starts_at <= $2)
		  AND (ends_at IS NULL OR ends_at > $2)
		ORDER BY CASE WHEN locale = $1 THEN 0 ELSE 1 END, priority DESC, created_at DESC
	`, locale, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]Notification, 0)
	for rows.Next() {
		var item Notification
		if err := rows.Scan(
			&item.ID, &item.Title, &item.Body, &item.Locale, &item.Priority,
			&item.StartsAt, &item.EndsAt, &item.UpdatedAt,
		); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

type GameServerPrincipal struct {
	ServerID string
	Scopes   []string
}

func (p GameServerPrincipal) HasScope(scope string) bool {
	for _, candidate := range p.Scopes {
		if candidate == scope {
			return true
		}
	}
	return false
}

func (r *Repository) AuthenticateGameServer(
	ctx context.Context,
	serverID, token string,
) (GameServerPrincipal, error) {
	if serverID == "" || !strings.HasPrefix(token, "gst_") {
		return GameServerPrincipal{}, forbidden("META_GAME_SERVER_UNAUTHORIZED", "Game Server authentication failed.")
	}
	digest := sha256.Sum256([]byte(token))
	var principal GameServerPrincipal
	err := r.pool.QueryRow(ctx, `
		SELECT id, token_scopes
		FROM game_servers
		WHERE id = $1
		  AND (
			server_token_hash = $2 OR
			(previous_server_token_hash = $2 AND previous_token_expires_at > $3)
		  )
		  AND token_revoked_at IS NULL AND token_expires_at > $3
		  AND state IN ('READY', 'RESERVED', 'RUNNING')
		  AND last_heartbeat_at > $4
	`, serverID, digest[:], r.now().UTC(),
		r.now().UTC().Add(-r.gameServerFreshness),
	).Scan(&principal.ServerID, &principal.Scopes)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return GameServerPrincipal{}, forbidden("META_GAME_SERVER_UNAUTHORIZED", "Game Server authentication failed.")
		}
		return GameServerPrincipal{}, internalError(err)
	}
	return principal, nil
}

func (r *Repository) GetMatchPlayerLoadout(
	ctx context.Context,
	principal GameServerPrincipal,
	matchID, playerID string,
) (MatchPlayerLoadout, error) {
	if !principal.HasScope("meta.loadouts.read") {
		return MatchPlayerLoadout{}, forbidden("META_GAME_SERVER_SCOPE_REQUIRED", "Game Server token scope is required.")
	}
	var allowed bool
	err := r.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM meta_matches AS match
			JOIN meta_match_players AS member ON member.match_id = match.id
			WHERE match.id = $1 AND member.player_id = $2
			  AND match.game_server_id = $3
			  AND match.state IN ('RESERVED', 'RUNNING')
		)
	`, matchID, playerID, principal.ServerID).Scan(&allowed)
	if err != nil {
		return MatchPlayerLoadout{}, internalError(err)
	}
	if !allowed {
		return MatchPlayerLoadout{}, forbidden("META_MATCH_PLAYER_FORBIDDEN", "The player is not assigned to this Game Server match.")
	}
	loadouts, err := r.ListLoadouts(ctx, playerID)
	if err != nil {
		return MatchPlayerLoadout{}, internalError(err)
	}
	return MatchPlayerLoadout{MatchID: matchID, PlayerID: playerID, Loadouts: loadouts}, nil
}

func (r *Repository) MarkMatchPlayerConnected(
	ctx context.Context,
	principal GameServerPrincipal,
	matchID, playerID string,
) error {
	if !principal.HasScope("meta.matches.connect") {
		return forbidden("META_GAME_SERVER_SCOPE_REQUIRED", "Game Server token scope is required.")
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return internalError(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	now := r.now().UTC()
	tag, err := tx.Exec(ctx, `
		UPDATE meta_match_players AS member
		SET connected_at = COALESCE(connected_at, $4)
		FROM meta_matches AS match
		WHERE member.match_id = $1 AND member.player_id = $2
		  AND match.id = member.match_id AND match.game_server_id = $3
		  AND match.state IN ('RESERVED', 'RUNNING')
		  AND match.match_attempt_id IS NULL
	`, matchID, playerID, principal.ServerID, now)
	if err != nil {
		return internalError(err)
	}
	if tag.RowsAffected() == 0 {
		return forbidden("META_MATCH_PLAYER_FORBIDDEN", "The player is not assigned to this Game Server match.")
	}
	_, err = tx.Exec(ctx, `
		UPDATE meta_matches
		SET state = 'RUNNING', started_at = COALESCE(started_at, $3), updated_at = $3
		WHERE id = $1 AND game_server_id = $2 AND state IN ('RESERVED', 'RUNNING')
		  AND match_attempt_id IS NULL
	`, matchID, principal.ServerID, now)
	if err == nil {
		_, err = tx.Exec(ctx, `
			UPDATE game_servers SET state = 'RUNNING', updated_at = $2
			WHERE id = $1 AND state = 'RESERVED'
		`, principal.ServerID, now)
	}
	if err != nil {
		return internalError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return internalError(err)
	}
	return nil
}

func (r *Repository) CompleteMatch(
	ctx context.Context,
	principal GameServerPrincipal,
	matchID string,
	result json.RawMessage,
) error {
	if !principal.HasScope("meta.matches.complete") {
		return forbidden("META_GAME_SERVER_SCOPE_REQUIRED", "Game Server token scope is required.")
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return internalError(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	now := r.now().UTC()
	tag, err := tx.Exec(ctx, `
		UPDATE meta_matches
		SET state = 'COMPLETED', completed_at = $3, updated_at = $3
		WHERE id = $1 AND game_server_id = $2 AND state IN ('RESERVED', 'RUNNING')
		  AND match_attempt_id IS NULL
	`, matchID, principal.ServerID, now)
	if err != nil {
		return internalError(err)
	}
	if tag.RowsAffected() == 0 {
		return forbidden("META_MATCH_FORBIDDEN", "The match is not assigned to this Game Server.")
	}
	if len(result) > 0 {
		_, err = tx.Exec(ctx, `
			UPDATE meta_match_players SET result = $2::jsonb WHERE match_id = $1
		`, matchID, result)
	}
	if err == nil {
		_, err = tx.Exec(ctx, `
			UPDATE game_servers
			SET state = 'READY', updated_at = $2
			WHERE id = $1 AND state IN ('RESERVED', 'RUNNING')
		`, principal.ServerID, now)
	}
	if err == nil {
		_, err = tx.Exec(ctx, `
			UPDATE meta_parties
			SET state = 'ACTIVE', revision = revision + 1, updated_at = $2
			WHERE id IN (
				SELECT ticket.party_id
				FROM meta_match_tickets AS ticket
				JOIN meta_matches AS match ON match.ticket_id = ticket.id
				WHERE match.id = $1 AND ticket.party_id IS NOT NULL
			)
		`, matchID, now)
	}
	if err != nil {
		return internalError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return internalError(err)
	}
	return nil
}
