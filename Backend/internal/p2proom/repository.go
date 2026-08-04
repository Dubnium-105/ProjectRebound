package p2proom

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

func (r *Repository) Create(ctx context.Context, tx pgx.Tx, room Room, vntSession *VNTSession, hostSession *VNTMemberSession) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO p2p_rooms (
			id, host_player_id, host_token_hash, display_name, region, mode,
			version, max_players, player_count, state, last_heartbeat_at,
			created_at, updated_at, transport_kind, expires_at, idempotency_key,
			idempotency_request_hash, host_token_ciphertext, host_token_nonce,
			host_token_key_id
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 1, 'LOBBY', $9, $9, $9, $10, $11,
		          NULLIF($12, ''), $13, $14, $15, NULLIF($16, ''))
	`, room.ID, room.HostPlayerID, room.HostTokenHash, room.DisplayName,
		room.Region, room.Mode, room.Version, room.MaxPlayers, room.CreatedAt,
		room.TransportKind, room.ExpiresAt, room.IdempotencyKey,
		room.IdempotencyRequestHash, room.HostTokenCiphertext, room.HostTokenNonce,
		room.HostTokenKeyID)
	if err != nil {
		return fmt.Errorf("insert P2P room: %w", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO p2p_room_members (room_id, player_id, role, status, joined_at)
		VALUES ($1, $2, 'HOST', 'ACTIVE', $3)
	`, room.ID, room.HostPlayerID, room.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert P2P host member: %w", err)
	}
	if vntSession != nil && hostSession != nil {
		_, err = tx.Exec(ctx, `
			INSERT INTO p2p_vnt_sessions (
				room_id, node_id, generation, state, node_host_snapshot,
				node_port_snapshot, node_region_snapshot, node_location_snapshot,
				node_fingerprint_snapshot, node_transports_snapshot,
				network_token_ciphertext, e2e_password_ciphertext, secret_key_id,
				network_token_nonce, e2e_password_nonce, created_at, updated_at
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$16)
		`, vntSession.RoomID, vntSession.NodeID, vntSession.Generation, vntSession.State,
			vntSession.NodeHost, vntSession.NodePort, vntSession.NodeRegion,
			vntSession.NodeLocation, vntSession.NodeFingerprint, vntSession.NodeTransports,
			vntSession.NetworkTokenCiphertext, vntSession.E2EPasswordCiphertext,
			vntSession.SecretKeyID, vntSession.NetworkTokenNonce,
			vntSession.E2EPasswordNonce, vntSession.CreatedAt)
		if err != nil {
			return fmt.Errorf("insert P2P VNT session: %w", err)
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO p2p_vnt_member_sessions (
				room_id, generation, player_id, device_id, virtual_ip, state, created_at
			) VALUES ($1,$2,$3,$4,$5::inet,'ISSUED',$6)
		`, hostSession.RoomID, hostSession.Generation, hostSession.PlayerID,
			hostSession.DeviceID, hostSession.VirtualIP, hostSession.CreatedAt)
		if err != nil {
			return fmt.Errorf("insert P2P VNT host session: %w", err)
		}
	}
	return nil
}

func (r *Repository) LockIdempotency(ctx context.Context, tx pgx.Tx, playerID, key string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1), hashtext($2))`, playerID, key)
	return err
}

func (r *Repository) FindIdempotent(ctx context.Context, tx pgx.Tx, playerID, key string) (string, []byte, []byte, []byte, error) {
	var roomID string
	var requestHash, ciphertext, nonce []byte
	err := tx.QueryRow(ctx, `
		SELECT id, idempotency_request_hash, host_token_ciphertext, host_token_nonce
		FROM p2p_rooms WHERE host_player_id = $1 AND idempotency_key = $2
	`, playerID, key).Scan(&roomID, &requestHash, &ciphertext, &nonce)
	return roomID, requestHash, ciphertext, nonce, err
}

func (r *Repository) Get(ctx context.Context, roomID string) (Room, error) {
	return scanRoom(r.pool.QueryRow(ctx, `SELECT `+roomColumns+` FROM p2p_rooms WHERE id = $1`, roomID))
}

func (r *Repository) GetForUpdate(ctx context.Context, tx pgx.Tx, roomID string) (Room, error) {
	return scanRoom(tx.QueryRow(ctx, `SELECT `+roomColumns+` FROM p2p_rooms WHERE id = $1 FOR UPDATE`, roomID))
}

func (r *Repository) List(ctx context.Context, filter ListFilter, hasSlots int) ([]Room, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+roomColumns+`
		FROM p2p_rooms
		WHERE ($1 = '' OR id > $1)
		  AND ($2 = '' OR region = $2)
		  AND ($3 = '' OR mode = $3)
		  AND ($4 = '' OR version = $4)
		  AND ($5 = '' OR state = $5)
		  AND ($6 = -1 OR ($6 = 1 AND player_count < max_players) OR ($6 = 0 AND player_count >= max_players))
		ORDER BY id
		LIMIT $7
	`, filter.Cursor, filter.Region, filter.Mode, filter.Version, filter.State, hasSlots, filter.Limit)
	if err != nil {
		return nil, fmt.Errorf("list P2P rooms: %w", err)
	}
	defer rows.Close()
	items := make([]Room, 0, filter.Limit)
	for rows.Next() {
		item, err := scanRoom(rows)
		if err != nil {
			return nil, fmt.Errorf("scan listed P2P room: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate P2P rooms: %w", err)
	}
	return items, nil
}

func (r *Repository) GetMemberForUpdate(ctx context.Context, tx pgx.Tx, roomID, playerID string) (Member, error) {
	return scanMember(tx.QueryRow(ctx, `
		SELECT room_id, player_id, role, status, joined_at, left_at
		FROM p2p_room_members
		WHERE room_id = $1 AND player_id = $2
		FOR UPDATE
	`, roomID, playerID))
}

func (r *Repository) GetMember(ctx context.Context, roomID, playerID string) (Member, error) {
	return scanMember(r.pool.QueryRow(ctx, `
		SELECT room_id, player_id, role, status, joined_at, left_at
		FROM p2p_room_members
		WHERE room_id = $1 AND player_id = $2
	`, roomID, playerID))
}

func (r *Repository) ActivateMember(ctx context.Context, tx pgx.Tx, roomID, playerID string, now time.Time) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO p2p_room_members (room_id, player_id, role, status, joined_at)
		VALUES ($1, $2, 'MEMBER', 'ACTIVE', $3)
		ON CONFLICT (room_id, player_id) DO UPDATE SET
			status = 'ACTIVE', joined_at = $3, left_at = NULL
	`, roomID, playerID, now)
	if err != nil {
		return fmt.Errorf("activate P2P room member: %w", err)
	}
	return nil
}

func (r *Repository) ActivateVNTMember(ctx context.Context, tx pgx.Tx, session VNTMemberSession) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO p2p_vnt_member_sessions (
			room_id, generation, player_id, device_id, virtual_ip, state, created_at
		) VALUES ($1,$2,$3,$4,$5::inet,'ISSUED',$6)
		ON CONFLICT (room_id, generation, player_id) DO UPDATE SET
			state = 'ISSUED', last_report_at = NULL, failure_reason = NULL
	`, session.RoomID, session.Generation, session.PlayerID, session.DeviceID,
		session.VirtualIP, session.CreatedAt)
	return err
}

func (r *Repository) NextVNTVirtualIP(ctx context.Context, tx pgx.Tx, roomID string, generation int) (string, error) {
	var virtualIP string
	err := tx.QueryRow(ctx, `
		SELECT '10.26.0.' || slot
		FROM generate_series(3, 254) slot
		WHERE NOT EXISTS (
			SELECT 1 FROM p2p_vnt_member_sessions
			WHERE room_id = $1 AND generation = $2
			  AND virtual_ip = ('10.26.0.' || slot)::inet
		)
		ORDER BY slot LIMIT 1
	`, roomID, generation).Scan(&virtualIP)
	return virtualIP, err
}

func (r *Repository) StopVNTMember(ctx context.Context, tx pgx.Tx, roomID, playerID string, now time.Time) error {
	_, err := tx.Exec(ctx, `
		UPDATE p2p_vnt_member_sessions SET state = 'STOPPED', last_report_at = $3
		WHERE room_id = $1 AND player_id = $2 AND state <> 'STOPPED'
	`, roomID, playerID, now)
	return err
}

func (r *Repository) MarkMemberLeft(ctx context.Context, tx pgx.Tx, roomID, playerID string, now time.Time) error {
	_, err := tx.Exec(ctx, `
		UPDATE p2p_room_members
		SET status = 'LEFT', left_at = $3
		WHERE room_id = $1 AND player_id = $2 AND status = 'ACTIVE'
	`, roomID, playerID, now)
	if err != nil {
		return fmt.Errorf("leave P2P room: %w", err)
	}
	return nil
}

func (r *Repository) UpdatePlayerCount(ctx context.Context, tx pgx.Tx, roomID string, delta int, now time.Time) (Room, error) {
	return scanRoom(tx.QueryRow(ctx, `
		UPDATE p2p_rooms
		SET player_count = player_count + $2, updated_at = $3
		WHERE id = $1
		RETURNING `+roomColumns,
		roomID, delta, now,
	))
}

func (r *Repository) Heartbeat(ctx context.Context, tx pgx.Tx, roomID string, now time.Time) (Room, error) {
	return scanRoom(tx.QueryRow(ctx, `
		UPDATE p2p_rooms
		SET state = CASE WHEN state = 'STALE' THEN 'LOBBY' ELSE state END,
		    last_heartbeat_at = $2, updated_at = $2
		WHERE id = $1
		RETURNING `+roomColumns,
		roomID, now,
	))
}

func (r *Repository) Start(ctx context.Context, tx pgx.Tx, roomID string, now time.Time) (Room, error) {
	if _, err := tx.Exec(ctx, `
		UPDATE p2p_vnt_sessions SET state = 'ACTIVE', updated_at = $2
		WHERE room_id = $1 AND state IN ('HOST_READY','READY')
	`, roomID, now); err != nil {
		return Room{}, err
	}
	return scanRoom(tx.QueryRow(ctx, `
		UPDATE p2p_rooms
		SET state = CASE WHEN transport_kind = 'VNT' THEN 'RUNNING' ELSE 'CONNECTING' END,
		    updated_at = $2
		WHERE id = $1
		RETURNING `+roomColumns,
		roomID, now,
	))
}

func (r *Repository) MarkRunning(ctx context.Context, roomID string, now time.Time) (Room, error) {
	return scanRoom(r.pool.QueryRow(ctx, `
		UPDATE p2p_rooms
		SET state = 'RUNNING', updated_at = $2
		WHERE id = $1 AND state IN ('LOBBY', 'CONNECTING')
		RETURNING `+roomColumns,
		roomID, now,
	))
}

func (r *Repository) Close(ctx context.Context, tx pgx.Tx, roomID string, now time.Time) (Room, error) {
	item, err := scanRoom(tx.QueryRow(ctx, `
		UPDATE p2p_rooms
		SET state = 'CLOSED', closed_at = COALESCE(closed_at, $2), updated_at = $2
		WHERE id = $1
		RETURNING `+roomColumns,
		roomID, now,
	))
	if err != nil {
		return Room{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE p2p_room_members
		SET status = 'LEFT', left_at = COALESCE(left_at, $2)
		WHERE room_id = $1 AND status = 'ACTIVE'
	`, roomID, now); err != nil {
		return Room{}, fmt.Errorf("close P2P room members: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE p2p_vnt_sessions SET state = 'CLOSED', updated_at = $2 WHERE room_id = $1 AND state <> 'CLOSED'`, roomID, now); err != nil {
		return Room{}, fmt.Errorf("close P2P VNT session: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE p2p_vnt_member_sessions SET state = 'STOPPED', last_report_at = $2 WHERE room_id = $1 AND state <> 'STOPPED'`, roomID, now); err != nil {
		return Room{}, fmt.Errorf("close P2P VNT member sessions: %w", err)
	}
	return item, nil
}

func (r *Repository) SweepStale(ctx context.Context, now time.Time, staleAfter, closedAfter time.Duration) (int64, []string, error) {
	var updated int64
	var closedRoomIDs []string
	err := r.pool.QueryRow(ctx, `
		WITH changed AS (
			UPDATE p2p_rooms
			SET state = CASE
			        WHEN last_heartbeat_at <= $2 THEN 'CLOSED'
			        WHEN last_heartbeat_at <= $3 THEN 'STALE'
			        ELSE state
			    END,
			    closed_at = CASE WHEN last_heartbeat_at <= $2 THEN COALESCE(closed_at, $1) ELSE closed_at END,
			    updated_at = $1
			WHERE state <> 'CLOSED' AND last_heartbeat_at <= $3
			RETURNING id, state
		), closed_members AS (
			UPDATE p2p_room_members AS member
			SET status = 'LEFT', left_at = COALESCE(member.left_at, $1)
			FROM changed
			WHERE changed.id = member.room_id AND changed.state = 'CLOSED' AND member.status = 'ACTIVE'
			RETURNING member.room_id
		)
		SELECT COUNT(*), COALESCE(array_agg(id) FILTER (WHERE state = 'CLOSED'), ARRAY[]::varchar[])
		FROM changed
	`, now, now.Add(-closedAfter), now.Add(-staleAfter)).Scan(&updated, &closedRoomIDs)
	if err != nil {
		return 0, nil, fmt.Errorf("sweep P2P rooms: %w", err)
	}
	if len(closedRoomIDs) > 0 {
		if _, err := r.pool.Exec(ctx, `UPDATE p2p_vnt_sessions SET state = 'CLOSED', updated_at = $2 WHERE room_id = ANY($1)`, closedRoomIDs, now); err != nil {
			return updated, closedRoomIDs, fmt.Errorf("close expired P2P VNT sessions: %w", err)
		}
		if _, err := r.pool.Exec(ctx, `UPDATE p2p_vnt_member_sessions SET state = 'STOPPED', last_report_at = $2 WHERE room_id = ANY($1) AND state <> 'STOPPED'`, closedRoomIDs, now); err != nil {
			return updated, closedRoomIDs, fmt.Errorf("stop expired P2P VNT member sessions: %w", err)
		}
	}
	return updated, closedRoomIDs, nil
}

func (r *Repository) GetVNTSession(ctx context.Context, tx pgx.Tx, roomID string) (VNTSession, error) {
	var session VNTSession
	var hostVirtualIP sql.NullString
	err := tx.QueryRow(ctx, `
		SELECT room_id, node_id, generation, state, node_host_snapshot,
		       node_port_snapshot, node_region_snapshot, node_location_snapshot,
		       node_fingerprint_snapshot, node_transports_snapshot,
		       network_token_ciphertext, network_token_nonce,
		       e2e_password_ciphertext, e2e_password_nonce, secret_key_id,
		       COALESCE(host(host_virtual_ip), ''), created_at, updated_at
		FROM p2p_vnt_sessions WHERE room_id = $1
	`, roomID).Scan(
		&session.RoomID, &session.NodeID, &session.Generation, &session.State,
		&session.NodeHost, &session.NodePort, &session.NodeRegion, &session.NodeLocation,
		&session.NodeFingerprint, &session.NodeTransports,
		&session.NetworkTokenCiphertext, &session.NetworkTokenNonce,
		&session.E2EPasswordCiphertext, &session.E2EPasswordNonce, &session.SecretKeyID,
		&hostVirtualIP, &session.CreatedAt, &session.UpdatedAt,
	)
	if hostVirtualIP.Valid {
		session.HostVirtualIP = hostVirtualIP.String
	}
	return session, err
}

func (r *Repository) GetVNTMember(ctx context.Context, tx pgx.Tx, roomID string, generation int, playerID string) (VNTMemberSession, error) {
	var session VNTMemberSession
	err := tx.QueryRow(ctx, `
		SELECT room_id, generation, player_id, device_id, host(virtual_ip), state,
		       COALESCE(observed_path, ''), COALESCE(failure_reason, ''), created_at
		FROM p2p_vnt_member_sessions
		WHERE room_id = $1 AND generation = $2 AND player_id = $3
	`, roomID, generation, playerID).Scan(
		&session.RoomID, &session.Generation, &session.PlayerID, &session.DeviceID,
		&session.VirtualIP, &session.State, &session.ObservedPath,
		&session.FailureReason, &session.CreatedAt,
	)
	return session, err
}

func (r *Repository) UpdateVNTPresence(ctx context.Context, tx pgx.Tx, roomID string, input VNTPresenceInput, playerID string, now time.Time) error {
	tag, err := tx.Exec(ctx, `
		UPDATE p2p_vnt_member_sessions
		SET state = $4, observed_path = NULLIF($5, ''), failure_reason = NULLIF($6, ''),
		    last_report_at = $7
		WHERE room_id = $1 AND generation = $2 AND player_id = $3
		  AND virtual_ip = $8::inet
	`, roomID, input.Generation, playerID, input.State, input.ObservedPath,
		input.ReasonCode, now, input.VirtualIP)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	return nil
}

func (r *Repository) MarkVNTHostReady(ctx context.Context, tx pgx.Tx, roomID string, generation int, virtualIP string, now time.Time) error {
	tag, err := tx.Exec(ctx, `
		UPDATE p2p_vnt_sessions
		SET state = 'HOST_READY', host_virtual_ip = $3::inet, updated_at = $4
		WHERE room_id = $1 AND generation = $2
		  AND state IN ('SELECTED','HOST_CONNECTING','HOST_READY')
		  AND (host_virtual_ip IS NULL OR host_virtual_ip = $3::inet)
	`, roomID, generation, virtualIP, now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	_, err = tx.Exec(ctx, `
		UPDATE p2p_vnt_member_sessions SET state = 'CONNECTED', last_report_at = $4
		WHERE room_id = $1 AND generation = $2 AND virtual_ip = $3::inet
	`, roomID, generation, virtualIP, now)
	return err
}

func (r *Repository) RebindVNT(ctx context.Context, tx pgx.Tx, session VNTSession, now time.Time) error {
	tag, err := tx.Exec(ctx, `
		UPDATE p2p_vnt_sessions SET
			node_id = $2, generation = $3, state = 'SELECTED',
			node_host_snapshot = $4, node_port_snapshot = $5,
			node_region_snapshot = $6, node_location_snapshot = $7,
			node_fingerprint_snapshot = $8, node_transports_snapshot = $9,
			network_token_ciphertext = $10, network_token_nonce = $11,
			e2e_password_ciphertext = $12, e2e_password_nonce = $13,
			secret_key_id = $14, host_virtual_ip = NULL, failure_reason = NULL,
			updated_at = $15
		WHERE room_id = $1
	`, session.RoomID, session.NodeID, session.Generation, session.NodeHost,
		session.NodePort, session.NodeRegion, session.NodeLocation, session.NodeFingerprint,
		session.NodeTransports, session.NetworkTokenCiphertext, session.NetworkTokenNonce,
		session.E2EPasswordCiphertext, session.E2EPasswordNonce, session.SecretKeyID, now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	if _, err := tx.Exec(ctx, `
		UPDATE p2p_vnt_member_sessions SET state = 'STOPPED', last_report_at = $3
		WHERE room_id = $1 AND generation < $2 AND state <> 'STOPPED'
	`, session.RoomID, session.Generation, now); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO p2p_vnt_member_sessions (
			room_id, generation, player_id, device_id, virtual_ip, state, created_at
		)
		SELECT $1, $2, member.player_id,
		       'vnd_' || md5(random()::text || member.player_id),
		       ('10.26.0.' || (ROW_NUMBER() OVER (
		           ORDER BY CASE WHEN member.role = 'HOST' THEN 0 ELSE 1 END, member.joined_at, member.player_id
		       ) + 1)::text)::inet,
		       'ISSUED', $3
		FROM p2p_room_members member
		WHERE member.room_id = $1 AND member.status = 'ACTIVE'
	`, session.RoomID, session.Generation, now)
	return err
}

const roomColumns = `
	p2p_rooms.id, p2p_rooms.host_player_id, p2p_rooms.host_token_hash,
	p2p_rooms.display_name, p2p_rooms.region, p2p_rooms.mode, p2p_rooms.version,
	p2p_rooms.max_players, p2p_rooms.player_count, p2p_rooms.state,
	p2p_rooms.last_heartbeat_at, p2p_rooms.created_at, p2p_rooms.updated_at,
	p2p_rooms.closed_at, p2p_rooms.transport_kind, p2p_rooms.expires_at,
	COALESCE((SELECT node_id FROM p2p_vnt_sessions WHERE room_id = p2p_rooms.id), ''),
	COALESCE((SELECT node_host_snapshot FROM p2p_vnt_sessions WHERE room_id = p2p_rooms.id), ''),
	COALESCE((SELECT node_port_snapshot FROM p2p_vnt_sessions WHERE room_id = p2p_rooms.id), 0),
	COALESCE((SELECT node_region_snapshot FROM p2p_vnt_sessions WHERE room_id = p2p_rooms.id), ''),
	COALESCE((SELECT node_location_snapshot FROM p2p_vnt_sessions WHERE room_id = p2p_rooms.id), ''),
	COALESCE((SELECT state FROM p2p_vnt_sessions WHERE room_id = p2p_rooms.id), ''),
	COALESCE((SELECT generation FROM p2p_vnt_sessions WHERE room_id = p2p_rooms.id), 0)
`

func scanRoom(row pgx.Row) (Room, error) {
	var item Room
	var closedAt sql.NullTime
	err := row.Scan(
		&item.ID, &item.HostPlayerID, &item.HostTokenHash, &item.DisplayName,
		&item.Region, &item.Mode, &item.Version, &item.MaxPlayers,
		&item.PlayerCount, &item.State, &item.LastHeartbeatAt,
		&item.CreatedAt, &item.UpdatedAt, &closedAt, &item.TransportKind,
		&item.ExpiresAt, &item.VNTNodeID, &item.VNTHost, &item.VNTPort,
		&item.VNTRegion, &item.VNTLocation, &item.VNTState, &item.VNTGeneration,
	)
	if closedAt.Valid {
		item.ClosedAt = &closedAt.Time
	}
	return item, err
}

func scanMember(row pgx.Row) (Member, error) {
	var item Member
	var leftAt sql.NullTime
	err := row.Scan(&item.RoomID, &item.PlayerID, &item.Role, &item.Status, &item.JoinedAt, &leftAt)
	if leftAt.Valid {
		item.LeftAt = &leftAt.Time
	}
	return item, err
}
