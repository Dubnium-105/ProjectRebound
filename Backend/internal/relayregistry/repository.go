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

var (
	ErrActiveMigration      = errors.New("relay migration is already active")
	ErrNoMigrationTarget    = errors.New("no relay migration target is available")
	ErrMaxMigrationAttempts = errors.New("relay migration attempts exhausted")
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

func (r *Repository) List(ctx context.Context, filter ListFilter) ([]Node, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+nodeColumns+`
		FROM relay_nodes
		WHERE ($1 = '' OR id > $1)
		  AND ($2 = '' OR region = $2)
		  AND ($3 = '' OR zone = $3)
		  AND ($4 = '' OR provider = $4)
		  AND ($5 = '' OR state = $5)
		ORDER BY id
		LIMIT $6
	`, filter.Cursor, filter.Region, filter.Zone, filter.Provider, filter.State, filter.Limit)
	if err != nil {
		return nil, fmt.Errorf("list relay nodes: %w", err)
	}
	defer rows.Close()
	items := make([]Node, 0, filter.Limit)
	for rows.Next() {
		item, err := scanNode(rows)
		if err != nil {
			return nil, fmt.Errorf("scan listed relay node: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate relay nodes: %w", err)
	}
	return items, nil
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
		    current_ingress_bps = $4, load_state = $5, last_heartbeat_at = $6,
		    lease_expires_at = $7, updated_at = $6
		WHERE id = $1 AND state <> 'REVOKED'
		  AND $2 BETWEEN 0 AND max_allocations
		  AND $3 >= 0 AND $4 >= 0
		RETURNING `+nodeColumns,
		nodeID, input.ActiveAllocations, input.CurrentEgressBPS, input.CurrentIngressBPS, input.LoadState, now, leaseExpiresAt,
	))
}

func (r *Repository) ChangeState(ctx context.Context, nodeID string, next State, deadline *time.Time, migrateExisting bool, meta AdminMeta, now time.Time) (Node, error) {
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
		SET state = $2, drain_deadline = $3, drain_migrate_existing = $4,
		    config_version = config_version + 1, updated_at = $5
		WHERE id = $1
		RETURNING `+nodeColumns,
		nodeID, next, deadline, migrateExisting, now,
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
		  AND load_state IN ('NORMAL', 'DEGRADED')
		  AND protocol_version = 2
		  AND certificate_expires_at > NOW() AND lease_expires_at > NOW()
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

func (r *Repository) FailedNodeAllocationIDs(ctx context.Context, limit int) ([]string, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT allocation.id
		FROM relay_allocations AS allocation
		JOIN relay_nodes AS node ON node.id = allocation.relay_node_id
		JOIN connections AS connection ON connection.id = allocation.connection_id
		WHERE allocation.state IN ('ALLOCATED', 'BINDING', 'ACTIVE')
		  AND node.state IN ('UNHEALTHY', 'OFFLINE', 'REVOKED')
		  AND connection.state NOT IN ('FAILED', 'EXPIRED', 'CLOSED')
		  AND NOT EXISTS (
		      SELECT 1 FROM relay_migrations AS migration
		      WHERE migration.connection_id = allocation.connection_id AND migration.state = 'BINDING'
		  )
		ORDER BY allocation.updated_at, allocation.id
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (r *Repository) DrainingNodeAllocationIDs(ctx context.Context, limit int) ([]string, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT allocation.id
		FROM relay_allocations AS allocation
		JOIN relay_nodes AS node ON node.id = allocation.relay_node_id
		JOIN connections AS connection ON connection.id = allocation.connection_id
		WHERE allocation.state IN ('ALLOCATED', 'BINDING', 'ACTIVE')
		  AND node.state = 'DRAINING' AND node.drain_migrate_existing
		  AND connection.state NOT IN ('FAILED', 'EXPIRED', 'CLOSED')
		  AND NOT EXISTS (
		      SELECT 1 FROM relay_migrations AS migration
		      WHERE migration.connection_id = allocation.connection_id AND migration.state = 'BINDING'
		  )
		ORDER BY allocation.updated_at, allocation.id
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (r *Repository) AvailableRegions(ctx context.Context) ([]string, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT DISTINCT region
		FROM relay_nodes
		WHERE state = 'READY'
		  AND load_state IN ('NORMAL', 'DEGRADED')
		  AND protocol_version = 2
		  AND certificate_expires_at > NOW() AND lease_expires_at > NOW()
		  AND active_allocations < max_allocations
		  AND current_egress_bps < max_egress_bps
		ORDER BY region
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var regions []string
	for rows.Next() {
		var region string
		if err := rows.Scan(&region); err != nil {
			return nil, err
		}
		regions = append(regions, region)
	}
	return regions, rows.Err()
}

func (r *Repository) PlanMigration(
	ctx context.Context,
	oldAllocationID, migrationID, newAllocationID, reason string,
	expiresAt, now time.Time,
	threshold int,
	bindTimeout time.Duration,
) (Migration, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Migration{}, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	oldAllocation, oldNode, err := scanAllocationAndNode(tx.QueryRow(ctx, `
		SELECT `+prefixedAllocationColumns("allocation")+`, `+prefixedNodeColumns("node")+`
		FROM relay_allocations AS allocation
		JOIN relay_nodes AS node ON node.id = allocation.relay_node_id
		WHERE allocation.id = $1
		  AND allocation.state IN ('ALLOCATED', 'BINDING', 'ACTIVE')
		  AND (node.state IN ('UNHEALTHY', 'OFFLINE', 'REVOKED')
		       OR ($2 = 'RELAY_DRAIN' AND node.state = 'DRAINING' AND node.drain_migrate_existing))
		FOR UPDATE OF allocation, node
	`, oldAllocationID, reason))
	if err != nil {
		return Migration{}, err
	}
	migration := Migration{
		ID: migrationID, ConnectionID: oldAllocation.ConnectionID, RoomID: oldAllocation.RoomID,
		OldAllocationID: oldAllocation.ID, OldRelayNodeID: oldNode.ID,
		State: "BINDING", Reason: reason, Attempt: 1, CreatedAt: now, UpdatedAt: now,
	}
	var activeMigrationID string
	err = tx.QueryRow(ctx, `
		SELECT id FROM relay_migrations
		WHERE connection_id = $1 AND state = 'BINDING'
	`, oldAllocation.ConnectionID).Scan(&activeMigrationID)
	if err == nil {
		return migration, ErrActiveMigration
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Migration{}, err
	}
	newNode, err := scanNode(tx.QueryRow(ctx, `
		SELECT `+nodeColumns+`
		FROM relay_nodes
		WHERE state = 'READY' AND load_state IN ('NORMAL', 'DEGRADED') AND id <> $1
		  AND protocol_version = 2
		  AND certificate_expires_at > NOW() AND lease_expires_at > NOW()
		  AND active_allocations * 100 < max_allocations * $3
		  AND current_egress_bps * 100 < max_egress_bps * $3
		  AND $4 = ANY(supported_protocols)
		ORDER BY (region = $2) DESC,
		         active_allocations::double precision / max_allocations ASC,
		         current_egress_bps::double precision / max_egress_bps ASC,
		         random()
		FOR UPDATE SKIP LOCKED
		LIMIT 1
	`, oldNode.ID, oldNode.Region, threshold, oldAllocation.Protocol))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return migration, ErrNoMigrationTarget
		}
		return Migration{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE relay_allocations
		SET state = 'FAILED', failure_reason = $3, closed_at = $2, updated_at = $2
		WHERE id = $1 AND $3 <> 'RELAY_DRAIN'
	`, oldAllocation.ID, now, reason); err != nil {
		return Migration{}, err
	}
	if reason != "RELAY_DRAIN" {
		if _, err := tx.Exec(ctx, `
			UPDATE relay_nodes
			SET active_allocations = GREATEST(active_allocations - 1, 0), updated_at = $2
			WHERE id = $1
		`, oldNode.ID, now); err != nil {
			return Migration{}, err
		}
	}
	newAllocation, err := scanAllocation(tx.QueryRow(ctx, `
		INSERT INTO relay_allocations (
			id, connection_id, room_id, relay_node_id, state, protocol,
			max_bps, max_pps, max_total_bytes, expires_at, created_at, updated_at
		) VALUES ($1, $2, $3, $4, 'ALLOCATED', $5, $6, $7, $8, $9, $10, $10)
		RETURNING `+allocationColumns,
		newAllocationID, oldAllocation.ConnectionID, oldAllocation.RoomID, newNode.ID,
		oldAllocation.Protocol, oldAllocation.MaxBPS, oldAllocation.MaxPPS,
		oldAllocation.MaxTotalBytes, expiresAt, now,
	))
	if err != nil {
		return Migration{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE relay_nodes SET active_allocations = active_allocations + 1, updated_at = $2 WHERE id = $1
	`, newNode.ID, now); err != nil {
		return Migration{}, err
	}
	bindDeadline := now.Add(bindTimeout)
	migration.NewAllocationID = newAllocation.ID
	migration.NewRelayNodeID = newNode.ID
	migration.NewAllocation = newAllocation
	migration.NewNode = newNode
	migration.BindDeadline = &bindDeadline
	if _, err := tx.Exec(ctx, `
		INSERT INTO relay_migrations (
			id, connection_id, old_allocation_id, new_allocation_id,
			old_relay_node_id, new_relay_node_id, state, reason, attempt,
			bind_deadline, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, 'BINDING', $7, 1, $8, $9, $9)
	`, migration.ID, migration.ConnectionID, migration.OldAllocationID, migration.NewAllocationID,
		migration.OldRelayNodeID, migration.NewRelayNodeID, migration.Reason, bindDeadline, now); err != nil {
		return Migration{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Migration{}, err
	}
	newNode.ActiveAllocations++
	migration.NewNode = newNode
	return migration, nil
}

func (r *Repository) PendingMigrations(ctx context.Context, limit int) ([]Migration, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, connection_id, old_allocation_id, new_allocation_id,
		       old_relay_node_id, new_relay_node_id, state, reason, attempt,
		       bind_deadline, failure_reason, dispatched_at, completed_at, created_at, updated_at
		FROM relay_migrations
		WHERE state = 'BINDING' AND dispatched_at IS NULL
		ORDER BY created_at, id
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var migrations []Migration
	for rows.Next() {
		migration, err := scanMigration(rows)
		if err != nil {
			return nil, err
		}
		migrations = append(migrations, migration)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for index := range migrations {
		allocation, node, err := scanAllocationAndNode(r.pool.QueryRow(ctx, `
			SELECT `+prefixedAllocationColumns("allocation")+`, `+prefixedNodeColumns("node")+`
			FROM relay_allocations AS allocation
			JOIN relay_nodes AS node ON node.id = allocation.relay_node_id
			WHERE allocation.id = $1
		`, migrations[index].NewAllocationID))
		if err != nil {
			return nil, err
		}
		migrations[index].RoomID = allocation.RoomID
		migrations[index].NewAllocation = allocation
		migrations[index].NewNode = node
	}
	return migrations, nil
}

func (r *Repository) MarkMigrationDispatched(ctx context.Context, migrationID string, now time.Time) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE relay_migrations SET dispatched_at = COALESCE(dispatched_at, $2), updated_at = $2
		WHERE id = $1 AND state = 'BINDING'
	`, migrationID, now)
	return err
}

func (r *Repository) MigrationForNewAllocation(ctx context.Context, allocationID string) (Migration, error) {
	return scanMigration(r.pool.QueryRow(ctx, `
		SELECT id, connection_id, old_allocation_id, new_allocation_id,
		       old_relay_node_id, new_relay_node_id, state, reason, attempt,
		       bind_deadline, failure_reason, dispatched_at, completed_at, created_at, updated_at
		FROM relay_migrations
		WHERE new_allocation_id = $1 AND state = 'BINDING'
	`, allocationID))
}

func (r *Repository) CompleteMigration(ctx context.Context, migrationID string, now time.Time) error {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var connectionID, newAllocationID string
	err = tx.QueryRow(ctx, `
		SELECT connection_id, new_allocation_id
		FROM relay_migrations
		WHERE id = $1 AND state = 'BINDING'
		FOR UPDATE
	`, migrationID).Scan(&connectionID, &newAllocationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return tx.Commit(ctx)
	}
	if err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `
		UPDATE relay_allocations
		SET state = 'CLOSED', failure_reason = 'MIGRATED', closed_at = COALESCE(closed_at, $3), updated_at = $3
		WHERE connection_id = $1 AND id <> $2 AND state IN ('ALLOCATED', 'BINDING', 'ACTIVE')
		RETURNING relay_node_id
	`, connectionID, newAllocationID, now)
	if err != nil {
		return err
	}
	var closedNodeIDs []string
	for rows.Next() {
		var nodeID string
		if err := rows.Scan(&nodeID); err != nil {
			rows.Close()
			return err
		}
		closedNodeIDs = append(closedNodeIDs, nodeID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, nodeID := range closedNodeIDs {
		if _, err := tx.Exec(ctx, `
			UPDATE relay_nodes
			SET active_allocations = GREATEST(active_allocations - 1, 0), updated_at = $2
			WHERE id = $1
		`, nodeID, now); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `
		UPDATE relay_migrations
		SET state = 'COMPLETED', completed_at = $2, updated_at = $2
		WHERE id = $1 AND state = 'BINDING'
	`, migrationID, now)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *Repository) TimedOutMigrations(ctx context.Context, now time.Time, limit int) ([]Migration, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, connection_id, old_allocation_id, new_allocation_id,
		       old_relay_node_id, new_relay_node_id, state, reason, attempt,
		       bind_deadline, failure_reason, dispatched_at, completed_at, created_at, updated_at
		FROM relay_migrations
		WHERE state = 'BINDING' AND bind_deadline IS NOT NULL AND bind_deadline <= $1
		ORDER BY bind_deadline, id
		LIMIT $2
	`, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Migration
	for rows.Next() {
		item, scanErr := scanMigration(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (r *Repository) FailMigrationAttempt(ctx context.Context, migrationID, reason string, now time.Time) (Migration, bool, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Migration{}, false, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	migration, err := scanMigration(tx.QueryRow(ctx, `
		UPDATE relay_migrations
		SET state = 'FAILED', failure_reason = $2, updated_at = $3
		WHERE id = $1 AND state = 'BINDING'
		RETURNING id, connection_id, old_allocation_id, new_allocation_id,
		          old_relay_node_id, new_relay_node_id, state, reason, attempt,
		          bind_deadline, failure_reason, dispatched_at, completed_at, created_at, updated_at
	`, migrationID, reason, now))
	if errors.Is(err, pgx.ErrNoRows) {
		return Migration{}, false, nil
	}
	if err != nil {
		return Migration{}, false, err
	}
	result, err := tx.Exec(ctx, `
		UPDATE relay_allocations
		SET state = 'FAILED', failure_reason = $2, closed_at = COALESCE(closed_at, $3), updated_at = $3
		WHERE id = $1 AND state IN ('ALLOCATED', 'BINDING', 'ACTIVE')
	`, migration.NewAllocationID, reason, now)
	if err != nil {
		return Migration{}, false, err
	}
	if result.RowsAffected() > 0 {
		if _, err := tx.Exec(ctx, `
			UPDATE relay_nodes
			SET active_allocations = GREATEST(active_allocations - 1, 0), updated_at = $2
			WHERE id = $1
		`, migration.NewRelayNodeID, now); err != nil {
			return Migration{}, false, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Migration{}, false, err
	}
	return migration, true, nil
}

func (r *Repository) RetryableFailedMigrations(ctx context.Context, maxAttempts, limit int) ([]Migration, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT migration.id, migration.connection_id, migration.old_allocation_id, migration.new_allocation_id,
		       migration.old_relay_node_id, migration.new_relay_node_id, migration.state,
		       migration.reason, migration.attempt, migration.bind_deadline, migration.failure_reason,
		       migration.dispatched_at, migration.completed_at, migration.created_at, migration.updated_at
		FROM relay_migrations AS migration
		JOIN connections AS connection ON connection.id = migration.connection_id
		WHERE migration.state = 'FAILED' AND migration.failure_reason = 'BIND_TIMEOUT'
		  AND migration.attempt < $1
		  AND connection.state NOT IN ('FAILED', 'EXPIRED', 'CLOSED')
		  AND NOT EXISTS (
		      SELECT 1 FROM relay_migrations AS later
		      WHERE later.connection_id = migration.connection_id
		        AND (later.attempt > migration.attempt OR later.state = 'BINDING')
		  )
		ORDER BY migration.updated_at, migration.id
		LIMIT $2
	`, maxAttempts, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Migration
	for rows.Next() {
		item, scanErr := scanMigration(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (r *Repository) PlanMigrationRetry(
	ctx context.Context,
	previous Migration,
	migrationID, newAllocationID string,
	expiresAt, now time.Time,
	threshold int,
	bindTimeout time.Duration,
	maxAttempts int,
) (Migration, error) {
	if previous.Attempt >= maxAttempts {
		return previous, ErrMaxMigrationAttempts
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Migration{}, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	oldAllocation, oldNode, err := scanAllocationAndNode(tx.QueryRow(ctx, `
		SELECT `+prefixedAllocationColumns("allocation")+`, `+prefixedNodeColumns("node")+`
		FROM relay_allocations AS allocation
		JOIN relay_nodes AS node ON node.id = allocation.relay_node_id
		WHERE allocation.id = $1 AND allocation.state = 'FAILED'
		FOR UPDATE OF allocation, node
	`, previous.NewAllocationID))
	if err != nil {
		return Migration{}, err
	}
	next := Migration{
		ID: migrationID, ConnectionID: oldAllocation.ConnectionID, RoomID: oldAllocation.RoomID,
		OldAllocationID: oldAllocation.ID, OldRelayNodeID: oldNode.ID,
		State: "BINDING", Reason: previous.Reason, Attempt: previous.Attempt + 1,
		CreatedAt: now, UpdatedAt: now,
	}
	var activeID string
	err = tx.QueryRow(ctx, `
		SELECT id FROM relay_migrations WHERE connection_id = $1 AND state = 'BINDING'
	`, previous.ConnectionID).Scan(&activeID)
	if err == nil {
		return next, ErrActiveMigration
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Migration{}, err
	}
	newNode, err := scanNode(tx.QueryRow(ctx, `
		SELECT `+nodeColumns+`
		FROM relay_nodes AS candidate
		WHERE candidate.state = 'READY' AND candidate.load_state IN ('NORMAL', 'DEGRADED')
		  AND candidate.protocol_version = 2
		  AND candidate.certificate_expires_at > NOW() AND candidate.lease_expires_at > NOW()
		  AND candidate.id <> $1
		  AND NOT EXISTS (
		      SELECT 1 FROM relay_migrations AS tried
		      WHERE tried.connection_id = $2 AND tried.new_relay_node_id = candidate.id
		  )
		  AND candidate.active_allocations * 100 < candidate.max_allocations * $4
		  AND candidate.current_egress_bps * 100 < candidate.max_egress_bps * $4
		  AND $5 = ANY(candidate.supported_protocols)
		ORDER BY (candidate.region = $3) DESC,
		         candidate.active_allocations::double precision / candidate.max_allocations ASC,
		         candidate.current_egress_bps::double precision / candidate.max_egress_bps ASC,
		         random()
		FOR UPDATE OF candidate SKIP LOCKED
		LIMIT 1
	`, oldNode.ID, previous.ConnectionID, oldNode.Region, threshold, oldAllocation.Protocol))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return next, ErrNoMigrationTarget
		}
		return Migration{}, err
	}
	newAllocation, err := scanAllocation(tx.QueryRow(ctx, `
		INSERT INTO relay_allocations (
			id, connection_id, room_id, relay_node_id, state, protocol,
			max_bps, max_pps, max_total_bytes, expires_at, created_at, updated_at
		) VALUES ($1, $2, $3, $4, 'ALLOCATED', $5, $6, $7, $8, $9, $10, $10)
		RETURNING `+allocationColumns,
		newAllocationID, oldAllocation.ConnectionID, oldAllocation.RoomID, newNode.ID,
		oldAllocation.Protocol, oldAllocation.MaxBPS, oldAllocation.MaxPPS,
		oldAllocation.MaxTotalBytes, expiresAt, now,
	))
	if err != nil {
		return Migration{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE relay_nodes SET active_allocations = active_allocations + 1, updated_at = $2 WHERE id = $1
	`, newNode.ID, now); err != nil {
		return Migration{}, err
	}
	bindDeadline := now.Add(bindTimeout)
	next.NewAllocationID = newAllocation.ID
	next.NewRelayNodeID = newNode.ID
	next.NewAllocation = newAllocation
	next.NewNode = newNode
	next.BindDeadline = &bindDeadline
	if _, err := tx.Exec(ctx, `
		INSERT INTO relay_migrations (
			id, connection_id, old_allocation_id, new_allocation_id,
			old_relay_node_id, new_relay_node_id, state, reason, attempt,
			bind_deadline, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, 'BINDING', $7, $8, $9, $10, $10)
	`, next.ID, next.ConnectionID, next.OldAllocationID, next.NewAllocationID,
		next.OldRelayNodeID, next.NewRelayNodeID, next.Reason, next.Attempt, bindDeadline, now); err != nil {
		return Migration{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Migration{}, err
	}
	newNode.ActiveAllocations++
	next.NewNode = newNode
	return next, nil
}

func (r *Repository) FailAllocation(ctx context.Context, allocationID, reason string, now time.Time) error {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var nodeID string
	err = tx.QueryRow(ctx, `
		UPDATE relay_allocations
		SET state = 'FAILED', failure_reason = $2, closed_at = COALESCE(closed_at, $3), updated_at = $3
		WHERE id = $1 AND state IN ('ALLOCATED', 'BINDING', 'ACTIVE')
		RETURNING relay_node_id
	`, allocationID, reason, now).Scan(&nodeID)
	if errors.Is(err, pgx.ErrNoRows) {
		return tx.Commit(ctx)
	}
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE relay_nodes
		SET active_allocations = GREATEST(active_allocations - 1, 0), updated_at = $2
		WHERE id = $1
	`, nodeID, now); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (r *Repository) FailConnectionAllocations(ctx context.Context, connectionID, reason string, now time.Time) error {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	rows, err := tx.Query(ctx, `
		UPDATE relay_allocations
		SET state = 'FAILED', failure_reason = $2, closed_at = COALESCE(closed_at, $3), updated_at = $3
		WHERE connection_id = $1 AND state IN ('ALLOCATED', 'BINDING', 'ACTIVE')
		RETURNING relay_node_id
	`, connectionID, reason, now)
	if err != nil {
		return err
	}
	var nodeIDs []string
	for rows.Next() {
		var nodeID string
		if err := rows.Scan(&nodeID); err != nil {
			rows.Close()
			return err
		}
		nodeIDs = append(nodeIDs, nodeID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, nodeID := range nodeIDs {
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

func scanMigration(row pgx.Row) (Migration, error) {
	var migration Migration
	var bindDeadline, dispatchedAt, completedAt sql.NullTime
	var failureReason sql.NullString
	err := row.Scan(
		&migration.ID, &migration.ConnectionID, &migration.OldAllocationID, &migration.NewAllocationID,
		&migration.OldRelayNodeID, &migration.NewRelayNodeID, &migration.State, &migration.Reason, &migration.Attempt,
		&bindDeadline, &failureReason, &dispatchedAt, &completedAt, &migration.CreatedAt, &migration.UpdatedAt,
	)
	if bindDeadline.Valid {
		migration.BindDeadline = &bindDeadline.Time
	}
	if failureReason.Valid {
		migration.FailureReason = failureReason.String
	}
	if dispatchedAt.Valid {
		migration.DispatchedAt = &dispatchedAt.Time
	}
	if completedAt.Valid {
		migration.CompletedAt = &completedAt.Time
	}
	return migration, err
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
	id, display_name, region, zone, provider, state, load_state, software_version,
	protocol_version, public_endpoints, supported_protocols, max_allocations,
	max_egress_bps, active_allocations, current_egress_bps, current_ingress_bps,
	certificate_fingerprint, certificate_expires_at, node_token_hash,
	config_version, last_heartbeat_at, lease_expires_at, drain_deadline, drain_migrate_existing,
	created_at, updated_at
`

const allocationColumns = `
	id, connection_id, room_id, relay_node_id, state, protocol,
	max_bps, max_pps, max_total_bytes, expires_at, created_at, updated_at, closed_at
`

func prefixedNodeColumns(alias string) string {
	return alias + `.id, ` + alias + `.display_name, ` + alias + `.region, ` + alias + `.zone, ` + alias + `.provider, ` + alias + `.state, ` + alias + `.load_state, ` + alias + `.software_version, ` +
		alias + `.protocol_version, ` + alias + `.public_endpoints, ` + alias + `.supported_protocols, ` + alias + `.max_allocations, ` + alias + `.max_egress_bps, ` +
		alias + `.active_allocations, ` + alias + `.current_egress_bps, ` + alias + `.current_ingress_bps, ` + alias + `.certificate_fingerprint, ` + alias + `.certificate_expires_at, ` +
		alias + `.node_token_hash, ` + alias + `.config_version, ` + alias + `.last_heartbeat_at, ` + alias + `.lease_expires_at, ` + alias + `.drain_deadline, ` + alias + `.drain_migrate_existing, ` + alias + `.created_at, ` + alias + `.updated_at`
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
		&item.ID, &item.DisplayName, &item.Region, &item.Zone, &item.Provider, &item.State, &item.LoadState, &item.SoftwareVersion,
		&item.ProtocolVersion, &endpoints, &item.SupportedProtocols, &item.MaxAllocations,
		&item.MaxEgressBPS, &item.ActiveAllocations, &item.CurrentEgressBPS, &item.CurrentIngressBPS,
		&item.CertificateFingerprint, &item.CertificateExpiresAt, &item.NodeTokenHash,
		&item.ConfigVersion, &lastHeartbeat, &leaseExpires, &drainDeadline, &item.DrainMigrateExisting,
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
		&node.ID, &node.DisplayName, &node.Region, &node.Zone, &node.Provider, &node.State, &node.LoadState, &node.SoftwareVersion,
		&node.ProtocolVersion, &endpoints, &node.SupportedProtocols, &node.MaxAllocations,
		&node.MaxEgressBPS, &node.ActiveAllocations, &node.CurrentEgressBPS, &node.CurrentIngressBPS,
		&node.CertificateFingerprint, &node.CertificateExpiresAt, &node.NodeTokenHash,
		&node.ConfigVersion, &lastHeartbeat, &leaseExpires, &drainDeadline, &node.DrainMigrateExisting,
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
