package relayregistry

import (
	"context"
	"database/sql"
	"encoding/json"
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

func (r *Repository) SyncBootstrapCredentials(ctx context.Context, credentials []BootstrapCredential, now time.Time) error {
	for _, credential := range credentials {
		if _, err := r.pool.Exec(ctx, `
			INSERT INTO relay_bootstrap_tokens (id, token_hash, created_at)
			VALUES ($1, $2, $3)
			ON CONFLICT (id) DO NOTHING
		`, credential.ID, credential.Hash, now); err != nil {
			return fmt.Errorf("sync relay bootstrap credential %s: %w", credential.ID, err)
		}
	}
	return nil
}

func (r *Repository) Enroll(ctx context.Context, bootstrapHash []byte, node Node) error {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var bootstrapID string
	if err := tx.QueryRow(ctx, `
		SELECT id FROM relay_bootstrap_tokens
		WHERE token_hash = $1 AND consumed_at IS NULL
		FOR UPDATE
	`, bootstrapHash).Scan(&bootstrapID); err != nil {
		return err
	}
	endpoints, err := json.Marshal(node.PublicEndpoints)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO relay_nodes (
			id, display_name, region, zone, provider, state, software_version,
			protocol_version, public_endpoints, supported_protocols,
			max_allocations, max_egress_bps, certificate_fingerprint,
			certificate_expires_at, node_token_hash, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, 'BOOTSTRAPPING', $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $15)
	`, node.ID, node.DisplayName, node.Region, node.Zone, node.Provider,
		node.SoftwareVersion, node.ProtocolVersion, endpoints, node.SupportedProtocols,
		node.MaxAllocations, node.MaxEgressBPS, node.CertificateFingerprint,
		node.CertificateExpiresAt, node.NodeTokenHash, node.CreatedAt); err != nil {
		return fmt.Errorf("insert relay node: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE relay_bootstrap_tokens
		SET consumed_at = $2, consumed_by_node_id = $3
		WHERE id = $1
	`, bootstrapID, node.CreatedAt, node.ID); err != nil {
		return fmt.Errorf("consume relay bootstrap token: %w", err)
	}
	return tx.Commit(ctx)
}

func (r *Repository) Get(ctx context.Context, nodeID string) (Node, error) {
	return scanNode(r.pool.QueryRow(ctx, `SELECT `+nodeColumns+` FROM relay_nodes WHERE id = $1`, nodeID))
}

func (r *Repository) AuthenticateNodeToken(ctx context.Context, nodeID string, tokenHash []byte) (Node, error) {
	return scanNode(r.pool.QueryRow(ctx, `
		SELECT `+nodeColumns+` FROM relay_nodes
		WHERE id = $1 AND node_token_hash = $2 AND state <> 'REVOKED'
	`, nodeID, tokenHash))
}

func (r *Repository) RenewCertificate(ctx context.Context, nodeID string, tokenHash []byte, fingerprint string, expiresAt, now time.Time) (Node, error) {
	return scanNode(r.pool.QueryRow(ctx, `
		UPDATE relay_nodes
		SET certificate_fingerprint = $3, certificate_expires_at = $4,
		    config_version = config_version + 1, updated_at = $5
		WHERE id = $1 AND node_token_hash = $2 AND state <> 'REVOKED'
		RETURNING `+nodeColumns,
		nodeID, tokenHash, fingerprint, expiresAt, now,
	))
}

func (r *Repository) MarkConnecting(ctx context.Context, nodeID, fingerprint, softwareVersion string, protocolVersion int, now, leaseExpiresAt time.Time) (Node, error) {
	return scanNode(r.pool.QueryRow(ctx, `
		UPDATE relay_nodes
		SET state = 'CONNECTING', software_version = $3, protocol_version = $4,
		    last_heartbeat_at = $5, lease_expires_at = $6, updated_at = $5
		WHERE id = $1 AND certificate_fingerprint = $2
		  AND certificate_expires_at > $5 AND state <> 'REVOKED'
		RETURNING `+nodeColumns,
		nodeID, fingerprint, softwareVersion, protocolVersion, now, leaseExpiresAt,
	))
}

func (r *Repository) Heartbeat(ctx context.Context, nodeID string, input HeartbeatInput, now, leaseExpiresAt time.Time) (Node, error) {
	return scanNode(r.pool.QueryRow(ctx, `
		UPDATE relay_nodes
		SET state = CASE WHEN state = 'DRAINING' THEN 'DRAINING' ELSE 'READY' END,
		    active_allocations = $2, current_egress_bps = $3,
		    current_ingress_bps = $4, last_heartbeat_at = $5,
		    lease_expires_at = $6, updated_at = $5
		WHERE id = $1 AND state <> 'REVOKED'
		  AND $2 BETWEEN 0 AND max_allocations
		  AND $3 >= 0 AND $4 >= 0
		RETURNING `+nodeColumns,
		nodeID, input.ActiveAllocations, input.CurrentEgressBPS, input.CurrentIngressBPS, now, leaseExpiresAt,
	))
}

func (r *Repository) ChangeState(ctx context.Context, nodeID string, next State, deadline *time.Time, meta AdminMeta, now time.Time) (Node, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Node{}, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	current, err := scanNode(tx.QueryRow(ctx, `SELECT `+nodeColumns+` FROM relay_nodes WHERE id = $1 FOR UPDATE`, nodeID))
	if err != nil {
		return Node{}, err
	}
	updated, err := scanNode(tx.QueryRow(ctx, `
		UPDATE relay_nodes
		SET state = $2, drain_deadline = $3, config_version = config_version + 1, updated_at = $4
		WHERE id = $1
		RETURNING `+nodeColumns,
		nodeID, next, deadline, now,
	))
	if err != nil {
		return Node{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO relay_node_audit_logs (
			id, node_id, actor_id, action, old_state, new_state,
			request_id, ip_address, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, NULLIF($8, '')::inet, $9)
	`, newID("rna_"), nodeID, meta.ActorID, string(next), current.State, updated.State, meta.RequestID, meta.IPAddress, now); err != nil {
		return Node{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Node{}, err
	}
	return updated, nil
}

func (r *Repository) SweepNodes(ctx context.Context, now time.Time, unhealthyAfter, offlineAfter time.Duration) (int64, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE relay_nodes
		SET state = CASE
		        WHEN COALESCE(last_heartbeat_at, created_at) <= $2 OR certificate_expires_at <= $1 THEN 'OFFLINE'
		        WHEN COALESCE(last_heartbeat_at, created_at) <= $3 THEN 'UNHEALTHY'
		        ELSE state
		    END,
		    updated_at = $1
		WHERE state NOT IN ('OFFLINE', 'REVOKED')
		  AND (COALESCE(last_heartbeat_at, created_at) <= $3 OR certificate_expires_at <= $1)
	`, now, now.Add(-offlineAfter), now.Add(-unhealthyAfter))
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func (r *Repository) AllocationOpened(ctx context.Context, nodeID, allocationID string, now time.Time) (Allocation, error) {
	allocation, err := scanAllocation(r.pool.QueryRow(ctx, `
		UPDATE relay_allocations
		SET state = 'ACTIVE', updated_at = $3
		WHERE id = $1 AND relay_node_id = $2 AND state IN ('ALLOCATED', 'BINDING')
		RETURNING `+allocationColumns,
		allocationID, nodeID, now,
	))
	if err != nil {
		return Allocation{}, err
	}
	return allocation, nil
}

func (r *Repository) AllocationClosed(ctx context.Context, nodeID, allocationID string, now time.Time) error {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var state string
	if err := tx.QueryRow(ctx, `
		SELECT state FROM relay_allocations
		WHERE id = $1 AND relay_node_id = $2
		FOR UPDATE
	`, allocationID, nodeID).Scan(&state); err != nil {
		return err
	}
	if state != "CLOSED" && state != "FAILED" {
		if _, err := tx.Exec(ctx, `
			UPDATE relay_allocations
			SET state = 'CLOSED', closed_at = $3, updated_at = $3
			WHERE id = $1 AND relay_node_id = $2
		`, allocationID, nodeID, now); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE relay_nodes
			SET active_allocations = GREATEST(active_allocations - 1, 0), updated_at = $2
			WHERE id = $1
		`, nodeID, now); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (r *Repository) Schedule(ctx context.Context, allocation Allocation, region string, threshold int) (Allocation, Node, bool, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Allocation{}, Node{}, false, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	existing, node, err := r.getActiveAllocation(ctx, tx, allocation.ConnectionID)
	if err == nil {
		if err := tx.Commit(ctx); err != nil {
			return Allocation{}, Node{}, false, err
		}
		return existing, node, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Allocation{}, Node{}, false, err
	}
	node, err = scanNode(tx.QueryRow(ctx, `
		SELECT `+nodeColumns+`
		FROM relay_nodes
		WHERE state = 'READY'
		  AND active_allocations * 100 < max_allocations * $2
		  AND current_egress_bps * 100 < max_egress_bps * $2
		  AND $3 = ANY(supported_protocols)
		ORDER BY (region = $1) DESC,
		         active_allocations::double precision / max_allocations ASC,
		         current_egress_bps::double precision / max_egress_bps ASC,
		         random()
		FOR UPDATE SKIP LOCKED
		LIMIT 1
	`, region, threshold, allocation.Protocol))
	if err != nil {
		return Allocation{}, Node{}, false, err
	}
	created, err := scanAllocation(tx.QueryRow(ctx, `
		INSERT INTO relay_allocations (
			id, connection_id, room_id, relay_node_id, state, protocol,
			max_bps, max_pps, max_total_bytes, expires_at, created_at, updated_at
		) VALUES ($1, $2, $3, $4, 'ALLOCATED', $5, $6, $7, $8, $9, $10, $10)
		RETURNING `+allocationColumns,
		allocation.ID, allocation.ConnectionID, allocation.RoomID, node.ID,
		allocation.Protocol, allocation.MaxBPS, allocation.MaxPPS,
		allocation.MaxTotalBytes, allocation.ExpiresAt, allocation.CreatedAt,
	))
	if err != nil {
		return Allocation{}, Node{}, false, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE relay_nodes SET active_allocations = active_allocations + 1, updated_at = $2 WHERE id = $1
	`, node.ID, allocation.CreatedAt); err != nil {
		return Allocation{}, Node{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Allocation{}, Node{}, false, err
	}
	node.ActiveAllocations++
	return created, node, true, nil
}

func (r *Repository) getActiveAllocation(ctx context.Context, tx pgx.Tx, connectionID string) (Allocation, Node, error) {
	row := tx.QueryRow(ctx, `
		SELECT `+prefixedAllocationColumns("allocation")+`, `+prefixedNodeColumns("node")+`
		FROM relay_allocations AS allocation
		JOIN relay_nodes AS node ON node.id = allocation.relay_node_id
		WHERE allocation.connection_id = $1 AND allocation.state IN ('ALLOCATED', 'BINDING', 'ACTIVE')
		FOR UPDATE OF allocation, node
	`, connectionID)
	return scanAllocationAndNode(row)
}

const nodeColumns = `
	id, display_name, region, zone, provider, state, software_version,
	protocol_version, public_endpoints, supported_protocols, max_allocations,
	max_egress_bps, active_allocations, current_egress_bps, current_ingress_bps,
	certificate_fingerprint, certificate_expires_at, node_token_hash,
	config_version, last_heartbeat_at, lease_expires_at, drain_deadline,
	created_at, updated_at
`

const allocationColumns = `
	id, connection_id, room_id, relay_node_id, state, protocol,
	max_bps, max_pps, max_total_bytes, expires_at, created_at, updated_at, closed_at
`

func prefixedNodeColumns(alias string) string {
	return alias + `.id, ` + alias + `.display_name, ` + alias + `.region, ` + alias + `.zone, ` + alias + `.provider, ` + alias + `.state, ` + alias + `.software_version, ` +
		alias + `.protocol_version, ` + alias + `.public_endpoints, ` + alias + `.supported_protocols, ` + alias + `.max_allocations, ` + alias + `.max_egress_bps, ` +
		alias + `.active_allocations, ` + alias + `.current_egress_bps, ` + alias + `.current_ingress_bps, ` + alias + `.certificate_fingerprint, ` + alias + `.certificate_expires_at, ` +
		alias + `.node_token_hash, ` + alias + `.config_version, ` + alias + `.last_heartbeat_at, ` + alias + `.lease_expires_at, ` + alias + `.drain_deadline, ` + alias + `.created_at, ` + alias + `.updated_at`
}

func prefixedAllocationColumns(alias string) string {
	return alias + `.id, ` + alias + `.connection_id, ` + alias + `.room_id, ` + alias + `.relay_node_id, ` + alias + `.state, ` + alias + `.protocol, ` +
		alias + `.max_bps, ` + alias + `.max_pps, ` + alias + `.max_total_bytes, ` + alias + `.expires_at, ` + alias + `.created_at, ` + alias + `.updated_at, ` + alias + `.closed_at`
}

func scanNode(row pgx.Row) (Node, error) {
	var item Node
	var endpoints []byte
	var lastHeartbeat sql.NullTime
	var leaseExpires sql.NullTime
	var drainDeadline sql.NullTime
	err := row.Scan(
		&item.ID, &item.DisplayName, &item.Region, &item.Zone, &item.Provider, &item.State, &item.SoftwareVersion,
		&item.ProtocolVersion, &endpoints, &item.SupportedProtocols, &item.MaxAllocations,
		&item.MaxEgressBPS, &item.ActiveAllocations, &item.CurrentEgressBPS, &item.CurrentIngressBPS,
		&item.CertificateFingerprint, &item.CertificateExpiresAt, &item.NodeTokenHash,
		&item.ConfigVersion, &lastHeartbeat, &leaseExpires, &drainDeadline,
		&item.CreatedAt, &item.UpdatedAt,
	)
	if err == nil {
		err = json.Unmarshal(endpoints, &item.PublicEndpoints)
	}
	if lastHeartbeat.Valid {
		item.LastHeartbeatAt = &lastHeartbeat.Time
	}
	if leaseExpires.Valid {
		item.LeaseExpiresAt = &leaseExpires.Time
	}
	if drainDeadline.Valid {
		item.DrainDeadline = &drainDeadline.Time
	}
	return item, err
}

func scanAllocation(row pgx.Row) (Allocation, error) {
	var item Allocation
	var closedAt sql.NullTime
	err := row.Scan(
		&item.ID, &item.ConnectionID, &item.RoomID, &item.RelayNodeID, &item.State, &item.Protocol,
		&item.MaxBPS, &item.MaxPPS, &item.MaxTotalBytes, &item.ExpiresAt,
		&item.CreatedAt, &item.UpdatedAt, &closedAt,
	)
	if closedAt.Valid {
		item.ClosedAt = &closedAt.Time
	}
	return item, err
}

func scanAllocationAndNode(row pgx.Row) (Allocation, Node, error) {
	var allocation Allocation
	var node Node
	var closedAt sql.NullTime
	var endpoints []byte
	var lastHeartbeat sql.NullTime
	var leaseExpires sql.NullTime
	var drainDeadline sql.NullTime
	err := row.Scan(
		&allocation.ID, &allocation.ConnectionID, &allocation.RoomID, &allocation.RelayNodeID, &allocation.State, &allocation.Protocol,
		&allocation.MaxBPS, &allocation.MaxPPS, &allocation.MaxTotalBytes, &allocation.ExpiresAt,
		&allocation.CreatedAt, &allocation.UpdatedAt, &closedAt,
		&node.ID, &node.DisplayName, &node.Region, &node.Zone, &node.Provider, &node.State, &node.SoftwareVersion,
		&node.ProtocolVersion, &endpoints, &node.SupportedProtocols, &node.MaxAllocations,
		&node.MaxEgressBPS, &node.ActiveAllocations, &node.CurrentEgressBPS, &node.CurrentIngressBPS,
		&node.CertificateFingerprint, &node.CertificateExpiresAt, &node.NodeTokenHash,
		&node.ConfigVersion, &lastHeartbeat, &leaseExpires, &drainDeadline,
		&node.CreatedAt, &node.UpdatedAt,
	)
	if err == nil {
		err = json.Unmarshal(endpoints, &node.PublicEndpoints)
	}
	if closedAt.Valid {
		allocation.ClosedAt = &closedAt.Time
	}
	if lastHeartbeat.Valid {
		node.LastHeartbeatAt = &lastHeartbeat.Time
	}
	if leaseExpires.Valid {
		node.LeaseExpiresAt = &leaseExpires.Time
	}
	if drainDeadline.Valid {
		node.DrainDeadline = &drainDeadline.Time
	}
	return allocation, node, err
}
