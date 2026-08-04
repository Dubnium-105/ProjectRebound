package vnt

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct{ pool *pgxpool.Pool }

func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

func (r *Repository) Begin(ctx context.Context) (pgx.Tx, error) {
	return r.pool.BeginTx(ctx, pgx.TxOptions{})
}

func (r *Repository) InsertEnrollment(ctx context.Context, tx pgx.Tx, id, ownerID, label string, hash []byte, expiresAt, now time.Time) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO vnt_node_enrollments
			(id, owner_player_id, label, secret_hash, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, id, ownerID, label, hash, expiresAt, now)
	return err
}

func (r *Repository) ConsumeEnrollment(ctx context.Context, tx pgx.Tx, hash []byte, now time.Time) (string, error) {
	var id, ownerID string
	err := tx.QueryRow(ctx, `
		SELECT id, owner_player_id
		FROM vnt_node_enrollments
		WHERE secret_hash = $1 AND consumed_at IS NULL AND expires_at > $2
		FOR UPDATE
	`, hash, now).Scan(&id, &ownerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", serviceError(401, "VNT_ENROLLMENT_INVALID", "Invalid or expired VNT enrollment code.")
	}
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `UPDATE vnt_node_enrollments SET consumed_at = $2 WHERE id = $1`, id, now); err != nil {
		return "", err
	}
	return ownerID, nil
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

func (r *Repository) List(ctx context.Context, filter ListFilter) ([]Node, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+nodeColumns+`, (
			SELECT COUNT(*) FROM p2p_vnt_sessions session
			JOIN p2p_rooms room ON room.id = session.room_id
			WHERE session.node_id = node.id AND session.state NOT IN ('FAILED','CLOSED')
			  AND room.expires_at > NOW()
		) AS active_rooms
		FROM vnt_nodes node
		WHERE ($1 = '' OR state = $1) AND ($2 = '' OR region = $2)
		ORDER BY id LIMIT $3
	`, filter.Status, filter.Region, filter.Limit)
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

func (r *Repository) RotateCredential(ctx context.Context, tx pgx.Tx, nodeID string, oldHash []byte, newID string, newHash []byte, expiresAt, now time.Time) error {
	var oldID string
	err := tx.QueryRow(ctx, `
		SELECT id FROM vnt_node_credentials
		WHERE node_id = $1 AND secret_hash = $2 AND revoked_at IS NULL AND expires_at > $3
		FOR UPDATE
	`, nodeID, oldHash, now).Scan(&oldID)
	if errors.Is(err, pgx.ErrNoRows) {
		return serviceError(401, "VNT_NODE_UNAUTHORIZED", "Invalid VNT node credential.")
	}
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO vnt_node_credentials (id, node_id, secret_hash, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5)
	`, newID, nodeID, newHash, expiresAt, now); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE vnt_node_credentials SET revoked_at = $2, last_used_at = $2 WHERE id = $1`, oldID, now)
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
		UPDATE vnt_nodes SET state = 'ONLINE', last_reachable_at = $2, updated_at = $2
		WHERE id = $1 AND state IN ('REGISTERING','ONLINE','STALE','OFFLINE')
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
	drained, err := r.pool.Exec(ctx, `
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
	`, now)
	if err != nil {
		return tag.RowsAffected(), err
	}
	return tag.RowsAffected() + drained.RowsAffected(), nil
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
		WHERE id = $1 RETURNING state
	`, nodeID, now).Scan(&state)
	return state, err
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
