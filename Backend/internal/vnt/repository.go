package vnt

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct{ pool *pgxpool.Pool }

func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

func (r *Repository) Begin(ctx context.Context) (pgx.Tx, error) {
	return r.pool.BeginTx(ctx, pgx.TxOptions{})
}

type auditExecutor interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func (r *Repository) InsertSecurityAudit(ctx context.Context, executor auditExecutor, audit SecurityAudit) error {
	audit.RequestID = sanitizeAuditText(strings.TrimSpace(audit.RequestID))
	audit.UserAgent = sanitizeAuditText(strings.TrimSpace(audit.UserAgent))
	audit.IPAddress = strings.TrimSpace(audit.IPAddress)
	if net.ParseIP(audit.IPAddress) == nil {
		audit.IPAddress = ""
	}
	details := sanitizeAuditDetails(audit.Details)
	detailsJSON, err := json.Marshal(details)
	if err != nil {
		return fmt.Errorf("marshal VNT security audit details: %w", err)
	}
	_, err = executor.Exec(ctx, `
		INSERT INTO vnt_security_audit_logs (
			id, event_type, result, actor_type, player_id, admin_id, node_id, room_id,
			request_id, ip_address, user_agent, reason_code, details, created_at
		) VALUES (
			$1,$2,$3,$4,NULLIF($5,''),NULLIF($6,''),NULLIF($7,''),NULLIF($8,''),
			NULLIF($9,''),NULLIF($10,'')::inet,$11,NULLIF($12,''),$13,$14
		)
	`, audit.ID, audit.EventType, audit.Result, audit.ActorType, audit.PlayerID, audit.AdminID,
		audit.NodeID, audit.RoomID, audit.RequestID, audit.IPAddress, audit.UserAgent,
		audit.ReasonCode, detailsJSON, audit.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert VNT security audit: %w", err)
	}
	return nil
}

func (r *Repository) RecordSecurityAudit(ctx context.Context, audit SecurityAudit) error {
	return r.InsertSecurityAudit(ctx, r.pool, audit)
}

func (r *Repository) InsertEnrollment(ctx context.Context, tx pgx.Tx, id, ownerID, label string, hash []byte, expiresAt, now time.Time) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO vnt_node_enrollments
			(id, owner_player_id, label, secret_hash, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, id, ownerID, label, hash, expiresAt, now)
	return err
}

func (r *Repository) EnsureOwnerQuota(ctx context.Context, tx pgx.Tx, ownerID string, maximum int) error {
	var lockedPlayerID string
	if err := tx.QueryRow(ctx, `SELECT id FROM players WHERE id = $1 FOR UPDATE`, ownerID).Scan(&lockedPlayerID); err != nil {
		return err
	}
	var count int
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*) FROM vnt_nodes
		WHERE owner_player_id = $1 AND state <> 'RETIRED'
	`, ownerID).Scan(&count); err != nil {
		return err
	}
	if count >= maximum {
		return serviceError(409, "VNT_NODE_QUOTA_EXCEEDED", "The player has reached the VNT node ownership limit.")
	}
	return nil
}

func (r *Repository) ConsumeEnrollment(ctx context.Context, tx pgx.Tx, hash []byte, now time.Time) (string, string, error) {
	var id, ownerID string
	err := tx.QueryRow(ctx, `
		SELECT id, owner_player_id
		FROM vnt_node_enrollments
		WHERE secret_hash = $1 AND consumed_at IS NULL AND expires_at > $2
		FOR UPDATE
	`, hash, now).Scan(&id, &ownerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", serviceError(401, "VNT_ENROLLMENT_INVALID", "Invalid or expired VNT enrollment code.")
	}
	if err != nil {
		return "", "", err
	}
	if _, err := tx.Exec(ctx, `UPDATE vnt_node_enrollments SET consumed_at = $2 WHERE id = $1`, id, now); err != nil {
		return "", "", err
	}
	return id, ownerID, nil
}

func (r *Repository) InsertNode(ctx context.Context, tx pgx.Tx, node Node, credentialID string, credentialHash []byte, credentialExpiresAt time.Time) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO vnt_nodes (
			id, owner_player_id, advertised_host, port, region, location, state,
			vnts_version, wrapper_version, server_key_fingerprint,
			supported_transports, max_rooms, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$13)
	`, node.ID, node.OwnerPlayerID, node.AdvertisedHost, node.Port, node.Region,
		node.Location, node.State, node.VNTSVersion, node.WrapperVersion,
		node.ServerKeyFingerprint, node.SupportedTransports, node.MaxRooms, node.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert VNT node: %w", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO vnt_node_credentials (id, node_id, secret_hash, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5)
	`, credentialID, node.ID, credentialHash, credentialExpiresAt, node.CreatedAt)
	return err
}

func (r *Repository) RecoverNode(
	ctx context.Context,
	tx pgx.Tx,
	ownerID string,
	node Node,
	credentialID string,
	credentialHash []byte,
	credentialExpiresAt, now time.Time,
) (string, bool, error) {
	current, err := r.GetForAllocation(ctx, tx, node.ID, now)
	if err != nil {
		return "", false, err
	}
	if current.OwnerPlayerID != ownerID {
		return "", false, serviceError(404, "VNT_NODE_NOT_FOUND", "VNT node not found.")
	}
	if current.State == StateRevoked || current.State == StateRetired {
		return "", false, serviceError(409, "VNT_NODE_RECOVERY_TERMINAL", "A revoked or retired VNT node cannot be recovered.")
	}
	identityChanged := current.AdvertisedHost != node.AdvertisedHost || current.Port != node.Port ||
		current.ServerKeyFingerprint != node.ServerKeyFingerprint
	if current.ActiveRooms > 0 && identityChanged {
		return "", false, serviceError(409, "VNT_NODE_RECOVERY_ACTIVE_ROOMS", "Endpoint or server-key changes require all active rooms to drain first.")
	}
	if err := r.RevokeCredentials(ctx, tx, node.ID, now); err != nil {
		return "", false, err
	}
	var state string
	err = tx.QueryRow(ctx, `
		UPDATE vnt_nodes SET
			advertised_host = $2, port = $3, region = $4, location = $5,
			state = CASE WHEN state = 'DRAINING' THEN 'DRAINING' ELSE 'REGISTERING' END,
			vnts_version = $6, wrapper_version = $7, server_key_fingerprint = $8,
			supported_transports = $9, max_rooms = $10, reported_sessions = 0,
			last_heartbeat_at = NULL,
			last_reachable_at = CASE WHEN $11 THEN NULL ELSE last_reachable_at END,
			updated_at = $12
		WHERE id = $1
		RETURNING state
	`, node.ID, node.AdvertisedHost, node.Port, node.Region, node.Location,
		node.VNTSVersion, node.WrapperVersion, node.ServerKeyFingerprint,
		node.SupportedTransports, node.MaxRooms, identityChanged, now).Scan(&state)
	if err != nil {
		return "", false, err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO vnt_node_credentials (id, node_id, secret_hash, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5)
	`, credentialID, node.ID, credentialHash, credentialExpiresAt, now)
	return state, identityChanged, err
}

func (r *Repository) List(ctx context.Context, filter ListFilter) ([]Node, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+nodeColumns+`, (
			SELECT COUNT(*) FROM p2p_vnt_sessions session
			JOIN p2p_rooms room ON room.id = session.room_id
			WHERE session.node_id = node.id AND session.state NOT IN ('FAILED','CLOSED')
			  AND room.expires_at > NOW()
		) AS active_rooms
		FROM vnt_nodes node
		WHERE ($1 = '' OR state = $1)
		  AND ($2 = '' OR region = $2)
		  AND ($3 = '' OR id > $3)
		ORDER BY id LIMIT $4
	`, filter.Status, filter.Region, filter.Cursor, filter.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Node, 0, filter.Limit)
	for rows.Next() {
		node, err := scanNode(rows, true)
		if err != nil {
			return nil, err
		}
		result = append(result, node)
	}
	return result, rows.Err()
}

func (r *Repository) GetForAllocation(ctx context.Context, tx pgx.Tx, nodeID string, now time.Time) (Node, error) {
	return scanNode(tx.QueryRow(ctx, `
		SELECT `+nodeColumns+`, (
			SELECT COUNT(*) FROM p2p_vnt_sessions session
			JOIN p2p_rooms room ON room.id = session.room_id
			WHERE session.node_id = node.id AND session.state NOT IN ('FAILED','CLOSED')
			  AND room.expires_at > $2
		) AS active_rooms
		FROM vnt_nodes node WHERE id = $1 FOR UPDATE
	`, nodeID, now), true)
}

func (r *Repository) AuthenticateCredential(ctx context.Context, tx pgx.Tx, nodeID string, hash []byte, now time.Time) error {
	if err := r.lockActiveNode(ctx, tx, nodeID); err != nil {
		return err
	}
	var credentialID string
	err := tx.QueryRow(ctx, `
		SELECT id FROM vnt_node_credentials
		WHERE node_id = $1 AND secret_hash = $2
		  AND (revoked_at IS NULL OR revoked_at > $3) AND expires_at > $3
		FOR UPDATE
	`, nodeID, hash, now).Scan(&credentialID)
	if errors.Is(err, pgx.ErrNoRows) {
		return serviceError(401, "VNT_NODE_UNAUTHORIZED", "Invalid VNT node credential.")
	}
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE vnt_node_credentials SET last_used_at = $2 WHERE id = $1`, credentialID, now)
	return err
}

func (r *Repository) AuthenticateCurrentCredential(ctx context.Context, tx pgx.Tx, nodeID string, hash []byte, now time.Time) error {
	if err := r.lockActiveNode(ctx, tx, nodeID); err != nil {
		return err
	}
	var credentialID string
	err := tx.QueryRow(ctx, `
		SELECT id FROM vnt_node_credentials
		WHERE node_id = $1 AND secret_hash = $2 AND revoked_at IS NULL AND expires_at > $3
		FOR UPDATE
	`, nodeID, hash, now).Scan(&credentialID)
	if errors.Is(err, pgx.ErrNoRows) {
		return serviceError(401, "VNT_NODE_UNAUTHORIZED", "Invalid VNT node credential.")
	}
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE vnt_node_credentials SET last_used_at = $2 WHERE id = $1`, credentialID, now)
	return err
}

func (r *Repository) RotateCredential(
	ctx context.Context,
	tx pgx.Tx,
	nodeID string,
	oldHash []byte,
	newID string,
	newHash []byte,
	expiresAt, requestedPreviousValidUntil, now time.Time,
) (time.Time, error) {
	if err := r.lockActiveNode(ctx, tx, nodeID); err != nil {
		return time.Time{}, err
	}
	var oldID string
	var previousValidUntil time.Time
	err := tx.QueryRow(ctx, `
		SELECT id, LEAST(expires_at, $4) FROM vnt_node_credentials
		WHERE node_id = $1 AND secret_hash = $2 AND revoked_at IS NULL AND expires_at > $3
		FOR UPDATE
	`, nodeID, oldHash, now, requestedPreviousValidUntil).Scan(&oldID, &previousValidUntil)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, serviceError(401, "VNT_NODE_UNAUTHORIZED", "Invalid VNT node credential.")
	}
	if err != nil {
		return time.Time{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO vnt_node_credentials (id, node_id, secret_hash, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5)
	`, newID, nodeID, newHash, expiresAt, now); err != nil {
		return time.Time{}, err
	}
	_, err = tx.Exec(ctx, `UPDATE vnt_node_credentials SET revoked_at = $2, last_used_at = $3 WHERE id = $1`, oldID, previousValidUntil, now)
	return previousValidUntil, err
}

func (r *Repository) lockActiveNode(ctx context.Context, tx pgx.Tx, nodeID string) error {
	var id string
	err := tx.QueryRow(ctx, `
		SELECT id FROM vnt_nodes
		WHERE id = $1 AND state NOT IN ('REVOKED','RETIRED')
		FOR UPDATE
	`, nodeID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return serviceError(401, "VNT_NODE_UNAUTHORIZED", "Invalid VNT node credential.")
	}
	return err
}

func (r *Repository) Heartbeat(ctx context.Context, tx pgx.Tx, nodeID string, input HeartbeatInput, state string, now time.Time) error {
	tag, err := tx.Exec(ctx, `
		UPDATE vnt_nodes SET state = CASE
		        WHEN state = 'DRAINING' THEN 'DRAINING'
		        WHEN $2 = 'OFFLINE' THEN 'OFFLINE'
		        WHEN last_reachable_at IS NULL THEN 'REGISTERING'
		        WHEN last_reachable_at > $6 - INTERVAL '90 seconds' THEN 'ONLINE'
		        ELSE 'STALE'
		    END,
		    wrapper_version = $3, vnts_version = $4,
			reported_sessions = $5, last_heartbeat_at = $6, updated_at = $6
		WHERE id = $1 AND state NOT IN ('REVOKED','RETIRED')
	`, nodeID, state, input.WrapperVersion, input.VNTSVersion, input.ReportedSessions, now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return serviceError(409, "VNT_NODE_NOT_ACTIVE", "VNT node is not active.")
	}
	return nil
}

func (r *Repository) Endpoint(ctx context.Context, nodeID string) (string, int, error) {
	var host string
	var port int
	err := r.pool.QueryRow(ctx, `SELECT advertised_host, port FROM vnt_nodes WHERE id = $1`, nodeID).Scan(&host, &port)
	return host, port, err
}

func (r *Repository) MarkReachable(ctx context.Context, nodeID string, now time.Time) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE vnt_nodes SET
			state = CASE WHEN state = 'DRAINING' THEN 'DRAINING' ELSE 'ONLINE' END,
			last_reachable_at = $2, updated_at = $2
		WHERE id = $1 AND state IN ('REGISTERING','ONLINE','STALE','OFFLINE','DRAINING')
	`, nodeID, now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	return nil
}

func (r *Repository) Sweep(ctx context.Context, now time.Time, staleAfter, offlineAfter time.Duration) (int64, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE vnt_nodes SET
			state = CASE
				WHEN COALESCE(last_heartbeat_at, created_at) <= $2 THEN 'OFFLINE'
				ELSE 'STALE'
			END,
			updated_at = $1
		WHERE state IN ('REGISTERING','ONLINE','STALE')
		  AND COALESCE(last_heartbeat_at, created_at) <= $3
	`, now, now.Add(-offlineAfter), now.Add(-staleAfter))
	if err != nil {
		return 0, err
	}
	var drained, revoked int64
	err = r.pool.QueryRow(ctx, `
		WITH retired AS (
			UPDATE vnt_nodes node
			SET state = 'RETIRED', retired_at = COALESCE(retired_at, $1), updated_at = $1
			WHERE state = 'DRAINING'
			  AND NOT EXISTS (
				SELECT 1 FROM p2p_vnt_sessions session
				JOIN p2p_rooms room ON room.id = session.room_id
				WHERE session.node_id = node.id
				  AND session.state NOT IN ('FAILED','CLOSED')
				  AND room.expires_at > $1
			  )
			RETURNING id
		), revoked AS (
			UPDATE vnt_node_credentials credential
			SET revoked_at = $1
			WHERE credential.node_id IN (SELECT id FROM retired)
			  AND (credential.revoked_at IS NULL OR credential.revoked_at > $1)
			RETURNING credential.id
		)
		SELECT (SELECT COUNT(*) FROM retired), (SELECT COUNT(*) FROM revoked)
	`, now).Scan(&drained, &revoked)
	if err != nil {
		return tag.RowsAffected(), err
	}
	return tag.RowsAffected() + drained, nil
}

func (r *Repository) Retire(ctx context.Context, tx pgx.Tx, nodeID string, now time.Time) (string, error) {
	var state string
	err := tx.QueryRow(ctx, `
		UPDATE vnt_nodes node SET
			state = CASE WHEN EXISTS (
				SELECT 1 FROM p2p_vnt_sessions session
				JOIN p2p_rooms room ON room.id = session.room_id
				WHERE session.node_id = node.id AND session.state NOT IN ('FAILED','CLOSED')
				  AND room.expires_at > $2
			) THEN 'DRAINING' ELSE 'RETIRED' END,
			retired_at = CASE WHEN NOT EXISTS (
				SELECT 1 FROM p2p_vnt_sessions session
				JOIN p2p_rooms room ON room.id = session.room_id
				WHERE session.node_id = node.id AND session.state NOT IN ('FAILED','CLOSED')
				  AND room.expires_at > $2
			) THEN $2 ELSE retired_at END,
			updated_at = $2
		WHERE id = $1 AND state NOT IN ('REVOKED','RETIRED') RETURNING state
	`, nodeID, now).Scan(&state)
	return state, err
}

func (r *Repository) RevokeCredentials(ctx context.Context, tx pgx.Tx, nodeID string, now time.Time) error {
	_, err := tx.Exec(ctx, `
		UPDATE vnt_node_credentials
		SET revoked_at = $2
		WHERE node_id = $1 AND (revoked_at IS NULL OR revoked_at > $2)
	`, nodeID, now)
	return err
}

func (r *Repository) AdminList(ctx context.Context, filter AdminListFilter) ([]AdminNode, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+nodeColumns+`,
		       owner.steam_id, owner.persona_name, owner.account_status,
		       (
				SELECT COUNT(*) FROM p2p_vnt_sessions session
				JOIN p2p_rooms room ON room.id = session.room_id
				WHERE session.node_id = node.id AND session.state NOT IN ('FAILED','CLOSED')
				  AND room.expires_at > NOW()
		       ) AS active_rooms,
		       credential.expires_at, credential.last_used_at, credential.revoked_at
		FROM vnt_nodes node
		JOIN players owner ON owner.id = node.owner_player_id
		LEFT JOIN LATERAL (
			SELECT expires_at, last_used_at, revoked_at
			FROM vnt_node_credentials
			WHERE node_id = node.id
			ORDER BY created_at DESC, id DESC
			LIMIT 1
		) credential ON TRUE
		WHERE ($1 = '' OR node.state = $1)
		  AND ($2 = '' OR node.region = $2)
		  AND ($3 = '' OR node.owner_player_id = $3)
		  AND ($4 = '' OR node.id > $4)
		ORDER BY node.id
		LIMIT $5
	`, filter.State, filter.Region, filter.OwnerPlayerID, filter.Cursor, filter.Limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]AdminNode, 0, filter.Limit)
	for rows.Next() {
		item, err := scanAdminNode(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (r *Repository) AdminGet(ctx context.Context, nodeID string) (AdminNode, error) {
	return adminGet(ctx, r.pool, nodeID)
}

func (r *Repository) AdminGetTx(ctx context.Context, tx pgx.Tx, nodeID string) (AdminNode, error) {
	return adminGet(ctx, tx, nodeID)
}

type adminQueryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func adminGet(ctx context.Context, queryer adminQueryer, nodeID string) (AdminNode, error) {
	item, err := scanAdminNode(queryer.QueryRow(ctx, `
		SELECT `+nodeColumns+`,
		       owner.steam_id, owner.persona_name, owner.account_status,
		       (
				SELECT COUNT(*) FROM p2p_vnt_sessions session
				JOIN p2p_rooms room ON room.id = session.room_id
				WHERE session.node_id = node.id AND session.state NOT IN ('FAILED','CLOSED')
				  AND room.expires_at > NOW()
		       ) AS active_rooms,
		       credential.expires_at, credential.last_used_at, credential.revoked_at
		FROM vnt_nodes node
		JOIN players owner ON owner.id = node.owner_player_id
		LEFT JOIN LATERAL (
			SELECT expires_at, last_used_at, revoked_at
			FROM vnt_node_credentials
			WHERE node_id = node.id
			ORDER BY created_at DESC, id DESC
			LIMIT 1
		) credential ON TRUE
		WHERE node.id = $1
	`, nodeID))
	if err != nil {
		return AdminNode{}, err
	}
	rows, err := queryer.Query(ctx, `
		SELECT room.id, room.state, session.state, session.generation,
		       COALESCE(session.failure_reason, ''), room.expires_at, session.updated_at
		FROM p2p_vnt_sessions session
		JOIN p2p_rooms room ON room.id = session.room_id
		WHERE session.node_id = $1
		ORDER BY session.updated_at DESC, room.id
		LIMIT 101
	`, nodeID)
	if err != nil {
		return AdminNode{}, err
	}
	defer rows.Close()
	item.ReferencedRooms = make([]AdminRoomReference, 0)
	for rows.Next() {
		var reference AdminRoomReference
		if err := rows.Scan(
			&reference.RoomID, &reference.RoomState, &reference.SessionState,
			&reference.Generation, &reference.FailureReason, &reference.ExpiresAt,
			&reference.SessionUpdated,
		); err != nil {
			return AdminNode{}, err
		}
		if len(item.ReferencedRooms) == 100 {
			item.ReferencedRoomsTruncated = true
			continue
		}
		item.ReferencedRooms = append(item.ReferencedRooms, reference)
	}
	return item, rows.Err()
}

func (r *Repository) AdminSetState(ctx context.Context, tx pgx.Tx, nodeID, state string, now time.Time) (Node, error) {
	if _, err := tx.Exec(ctx, `
		UPDATE vnt_nodes
		SET state = $2, updated_at = $3
		WHERE id = $1
	`, nodeID, state, now); err != nil {
		return Node{}, err
	}
	return r.GetForAllocation(ctx, tx, nodeID, now)
}

func (r *Repository) AdminRevoke(ctx context.Context, tx pgx.Tx, nodeID string, now time.Time) (Node, int64, error) {
	if _, err := tx.Exec(ctx, `
		UPDATE vnt_nodes
		SET state = 'REVOKED', updated_at = $2
		WHERE id = $1
	`, nodeID, now); err != nil {
		return Node{}, 0, err
	}
	if err := r.RevokeCredentials(ctx, tx, nodeID, now); err != nil {
		return Node{}, 0, err
	}
	var closedRooms int64
	err := tx.QueryRow(ctx, `
		WITH affected AS MATERIALIZED (
			SELECT room_id
			FROM p2p_vnt_sessions
			WHERE node_id = $1 AND state <> 'CLOSED'
		), failed_sessions AS (
			UPDATE p2p_vnt_sessions
			SET state = 'FAILED', failure_reason = 'NODE_REVOKED', updated_at = $2
			WHERE room_id IN (SELECT room_id FROM affected)
		), failed_members AS (
			UPDATE p2p_vnt_member_sessions
			SET state = 'FAILED', failure_reason = 'NODE_REVOKED', last_report_at = $2
			WHERE room_id IN (SELECT room_id FROM affected)
			  AND state NOT IN ('FAILED','STOPPED')
		), left_members AS (
			UPDATE p2p_room_members
			SET status = 'LEFT', left_at = COALESCE(left_at, $2)
			WHERE room_id IN (SELECT room_id FROM affected) AND status = 'ACTIVE'
		), closed_rooms AS (
			UPDATE p2p_rooms
			SET state = 'CLOSED', closed_at = COALESCE(closed_at, $2), updated_at = $2
			WHERE id IN (SELECT room_id FROM affected) AND state <> 'CLOSED'
			RETURNING id
		)
		SELECT COUNT(*) FROM closed_rooms
	`, nodeID, now).Scan(&closedRooms)
	if err != nil {
		return Node{}, 0, err
	}
	item, err := r.GetForAllocation(ctx, tx, nodeID, now)
	return item, closedRooms, err
}

const nodeColumns = `
	node.id, node.owner_player_id, node.advertised_host, node.port, node.region,
	node.location, node.state, node.vnts_version, node.wrapper_version,
	node.server_key_fingerprint, node.supported_transports, node.max_rooms,
	node.reported_sessions, node.last_heartbeat_at, node.last_reachable_at,
	node.created_at, node.updated_at, node.retired_at
`

type scanner interface{ Scan(...any) error }

func scanNode(row scanner, withActiveRooms bool) (Node, error) {
	var node Node
	var lastHeartbeat, lastReachable, retired sql.NullTime
	args := []any{
		&node.ID, &node.OwnerPlayerID, &node.AdvertisedHost, &node.Port,
		&node.Region, &node.Location, &node.State, &node.VNTSVersion,
		&node.WrapperVersion, &node.ServerKeyFingerprint, &node.SupportedTransports,
		&node.MaxRooms, &node.ReportedSessions, &lastHeartbeat, &lastReachable,
		&node.CreatedAt, &node.UpdatedAt, &retired,
	}
	if withActiveRooms {
		args = append(args, &node.ActiveRooms)
	}
	err := row.Scan(args...)
	if lastHeartbeat.Valid {
		node.LastHeartbeatAt = &lastHeartbeat.Time
	}
	if lastReachable.Valid {
		node.LastReachableAt = &lastReachable.Time
	}
	if retired.Valid {
		node.RetiredAt = &retired.Time
	}
	return node, err
}

func scanAdminNode(row scanner) (AdminNode, error) {
	var item AdminNode
	var lastHeartbeat, lastReachable, retired sql.NullTime
	var credentialExpires, credentialLastUsed, credentialRevoked sql.NullTime
	err := row.Scan(
		&item.ID, &item.OwnerPlayerID, &item.AdvertisedHost, &item.Port,
		&item.Region, &item.Location, &item.State, &item.VNTSVersion,
		&item.WrapperVersion, &item.ServerKeyFingerprint, &item.SupportedTransports,
		&item.MaxRooms, &item.ReportedSessions, &lastHeartbeat, &lastReachable,
		&item.CreatedAt, &item.UpdatedAt, &retired,
		&item.OwnerSteamID, &item.OwnerPersonaName, &item.OwnerAccountStatus,
		&item.ActiveRooms, &credentialExpires, &credentialLastUsed, &credentialRevoked,
	)
	if lastHeartbeat.Valid {
		item.LastHeartbeatAt = &lastHeartbeat.Time
	}
	if lastReachable.Valid {
		item.LastReachableAt = &lastReachable.Time
	}
	if retired.Valid {
		item.RetiredAt = &retired.Time
	}
	if credentialExpires.Valid {
		item.CredentialExpiresAt = &credentialExpires.Time
	}
	if credentialLastUsed.Valid {
		item.CredentialLastUsedAt = &credentialLastUsed.Time
	}
	if credentialRevoked.Valid {
		item.CredentialRevokedAt = &credentialRevoked.Time
	}
	return item, err
}
