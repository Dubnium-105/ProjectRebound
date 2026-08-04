package relayregistry

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/config"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/connection"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/database"
	"github.com/jackc/pgx/v5/pgxpool"
)

type recordedMigrationCoordinator struct {
	migrations []connection.RelayMigration
	bound      [][4]string
	failed     [][3]string
}

func (c *recordedMigrationCoordinator) RelayMigrating(_ context.Context, _ string, migration connection.RelayMigration) error {
	c.migrations = append(c.migrations, migration)
	return nil
}

func (c *recordedMigrationCoordinator) RelayBound(_ context.Context, connectionID, allocationID, previousAllocationID, migrationID string) error {
	c.bound = append(c.bound, [4]string{connectionID, allocationID, previousAllocationID, migrationID})
	return nil
}

func (c *recordedMigrationCoordinator) RelayMigrationFailed(_ context.Context, connectionID, migrationID, reason string) error {
	c.failed = append(c.failed, [3]string{connectionID, migrationID, reason})
	return nil
}

type recordedControlPublisher struct{ messages map[string][]ControlMessage }

func (p *recordedControlPublisher) Publish(nodeID string, message ControlMessage) {
	if p.messages == nil {
		p.messages = make(map[string][]ControlMessage)
	}
	p.messages[nodeID] = append(p.messages[nodeID], message)
}

func TestRelayMigrationLifecycleAgainstPostgreSQL(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := database.NewMigrator(pool).Up(ctx); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}

	suffix := time.Now().UnixNano()
	now := time.Now().UTC().Truncate(time.Second)
	playerIDs := []string{
		fmt.Sprintf("p_migration_host_%d", suffix),
		fmt.Sprintf("p_migration_peer_%d", suffix),
		fmt.Sprintf("p_migration_timeout_peer_%d", suffix),
	}
	roomID := fmt.Sprintf("room_migration_%d", suffix)
	connectionID := fmt.Sprintf("conn_migration_%d", suffix)
	timeoutConnectionID := fmt.Sprintf("conn_migration_timeout_%d", suffix)
	oldNodeID := fmt.Sprintf("relay_old_%d", suffix)
	newNodeID := fmt.Sprintf("relay_new_%d", suffix)
	oldAllocationID := fmt.Sprintf("alloc_old_%d", suffix)
	nodeIDs := []string{oldNodeID, newNodeID}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		connectionIDs := []string{connectionID, timeoutConnectionID}
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM relay_migrations WHERE connection_id = ANY($1)", connectionIDs)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM relay_allocations WHERE connection_id = ANY($1)", connectionIDs)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM relay_nodes WHERE id = ANY($1)", nodeIDs)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM connections WHERE id = ANY($1)", connectionIDs)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM p2p_rooms WHERE id = $1", roomID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM players WHERE id = ANY($1)", playerIDs)
	})

	for index, playerID := range playerIDs {
		steamID := fmt.Sprintf("%017d", 80_000_000_000_000_000+(suffix%1_000_000_000_000)+int64(index))
		if _, err := pool.Exec(ctx, `
			INSERT INTO players (id, steam_id, persona_name, created_at, updated_at)
			VALUES ($1, $2, 'Migration Integration', $3, $3)
		`, playerID, steamID, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO p2p_rooms (
			id, host_player_id, host_token_hash, display_name, region, mode, version,
			max_players, player_count, state, last_heartbeat_at, created_at, updated_at, expires_at
		) VALUES ($1, $2, $3, 'Migration Room', 'hk', 'coop', '1.0.0', 2, 2, 'RUNNING', $4, $4, $4, $5)
	`, roomID, playerIDs[0], hashToken("migration-room-token"), now, now.Add(8*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO connections (
			id, room_id, host_player_id, peer_player_id, state, selected_path,
			expires_at, created_at, updated_at
		) VALUES ($1, $2, $3, $4, 'CONNECTED', 'UDP_RELAY', $5, $6, $6)
	`, connectionID, roomID, playerIDs[0], playerIDs[1], now.Add(10*time.Minute), now); err != nil {
		t.Fatal(err)
	}
	endpoints, _ := json.Marshal([]Endpoint{{Protocol: "UDP", Host: "8.8.8.8", Port: 443}})
	for _, node := range []struct {
		id, state, region, fingerprint string
		active                         int
	}{
		{id: oldNodeID, state: "UNHEALTHY", region: "hk", fingerprint: fmt.Sprintf("%064x", suffix), active: 1},
		{id: newNodeID, state: "READY", region: "hk", fingerprint: fmt.Sprintf("%064x", suffix+1)},
	} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO relay_nodes (
				id, display_name, region, zone, provider, state, software_version,
				protocol_version, public_endpoints, supported_protocols,
				max_allocations, max_egress_bps, active_allocations,
				certificate_fingerprint, certificate_expires_at, node_token_hash,
				last_heartbeat_at, lease_expires_at, created_at, updated_at
			) VALUES ($1, $1, $2, 'hk-1', 'integration', $3, '1.0.0', 2, $4, $5,
			          10, 10000000, $6, $7, $8, $9, $10, $11, $10, $10)
		`, node.id, node.region, node.state, endpoints, []string{"UDP"}, node.active,
			node.fingerprint, now.Add(time.Hour), hashToken("node-token-"+node.id), now, now.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO relay_allocations (
			id, connection_id, room_id, relay_node_id, state, protocol,
			max_bps, max_pps, max_total_bytes, expires_at, created_at, updated_at
		) VALUES ($1, $2, $3, $4, 'ACTIVE', 'UDP', 256000, 200, 268435456, $5, $6, $6)
	`, oldAllocationID, connectionID, roomID, oldNodeID, now.Add(20*time.Minute), now); err != nil {
		t.Fatal(err)
	}

	cfg := config.Defaults.RelayRegistry
	cfg.MigrationMaxAttempts = 2
	authority, err := NewAuthority(cfg, "development")
	if err != nil {
		t.Fatal(err)
	}
	tokenManager, err := NewRelayTokenManager(cfg, "development")
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(NewRepository(pool), authority, tokenManager, fixedRoomDirectory{region: "hk"}, cfg)
	service.now = func() time.Time { return now }
	coordinator := &recordedMigrationCoordinator{}
	publisher := &recordedControlPublisher{}
	service.SetConnectionCoordinator(coordinator)
	service.SetControlPublisher(publisher)

	planned, dispatched, err := service.MigrateFailedRelays(ctx)
	if err != nil || planned != 1 || dispatched != 1 {
		t.Fatalf("migration sweep = %d/%d, %v", planned, dispatched, err)
	}
	if len(coordinator.migrations) != 1 {
		t.Fatalf("coordinator migrations = %#v", coordinator.migrations)
	}
	migration := coordinator.migrations[0]
	if migration.PreviousAllocationID != oldAllocationID || migration.Allocation.Endpoint.NodeID != newNodeID ||
		migration.Allocation.HostToken == migration.Allocation.PeerToken {
		t.Fatalf("migration assignment = %#v", migration)
	}
	if _, err := tokenManager.Verify(migration.Allocation.HostToken, newNodeID); err != nil {
		t.Fatalf("new host token: %v", err)
	}
	if messages := publisher.messages[oldNodeID]; len(messages) != 0 {
		t.Fatalf("old allocation was revoked before replacement BIND completed: %#v", messages)
	}
	if planned, dispatched, err := service.MigrateFailedRelays(ctx); err != nil || planned != 0 || dispatched != 0 {
		t.Fatalf("idempotent migration sweep = %d/%d, %v", planned, dispatched, err)
	}
	if err := service.AllocationOpened(ctx, newNodeID, migration.Allocation.AllocationID); err != nil {
		t.Fatal(err)
	}
	if messages := publisher.messages[oldNodeID]; len(messages) != 1 || messages[0].Type != "RevokeAllocation" {
		t.Fatalf("old relay commands after replacement BIND = %#v", messages)
	}
	if len(coordinator.bound) != 1 || coordinator.bound[0][2] != oldAllocationID {
		t.Fatalf("completed migration binding = %#v", coordinator.bound)
	}
	var oldState, newState, migrationState string
	if err := pool.QueryRow(ctx, "SELECT state FROM relay_allocations WHERE id = $1", oldAllocationID).Scan(&oldState); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, "SELECT state FROM relay_allocations WHERE id = $1", migration.Allocation.AllocationID).Scan(&newState); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, "SELECT state FROM relay_migrations WHERE new_allocation_id = $1", migration.Allocation.AllocationID).Scan(&migrationState); err != nil {
		t.Fatal(err)
	}
	if oldState != "FAILED" || newState != "ACTIVE" || migrationState != "COMPLETED" {
		t.Fatalf("migration states = old:%s new:%s migration:%s", oldState, newState, migrationState)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO connections (
			id, room_id, host_player_id, peer_player_id, state, selected_path,
			expires_at, created_at, updated_at
		) VALUES ($1, $2, $3, $4, 'CONNECTED', 'UDP_RELAY', $5, $6, $6)
	`, timeoutConnectionID, roomID, playerIDs[0], playerIDs[2], now.Add(10*time.Minute), now); err != nil {
		t.Fatal(err)
	}
	timeoutAllocationID := fmt.Sprintf("alloc_timeout_%d", suffix)
	if _, err := pool.Exec(ctx, `
		INSERT INTO relay_allocations (
			id, connection_id, room_id, relay_node_id, state, protocol,
			max_bps, max_pps, max_total_bytes, expires_at, created_at, updated_at
		) VALUES ($1, $2, $3, $4, 'ACTIVE', 'UDP', 256000, 200, 268435456, $5, $6, $6)
	`, timeoutAllocationID, timeoutConnectionID, roomID, oldNodeID, now.Add(20*time.Minute), now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE relay_nodes SET active_allocations = active_allocations + 1 WHERE id = $1", oldNodeID); err != nil {
		t.Fatal(err)
	}
	if planned, dispatched, err := service.MigrateFailedRelays(ctx); err != nil || planned != 1 || dispatched != 1 {
		t.Fatalf("timeout migration initial sweep = %d/%d, %v", planned, dispatched, err)
	}
	if len(coordinator.migrations) != 2 || coordinator.migrations[1].Attempt != 1 {
		t.Fatalf("initial timeout migration = %#v", coordinator.migrations)
	}

	now = now.Add(time.Duration(cfg.MigrationTimeoutSeconds+1) * time.Second)
	thirdNodeID := fmt.Sprintf("relay_third_%d", suffix)
	nodeIDs = append(nodeIDs, thirdNodeID)
	if _, err := pool.Exec(ctx, `
		INSERT INTO relay_nodes (
			id, display_name, region, zone, provider, state, software_version,
			protocol_version, public_endpoints, supported_protocols,
			max_allocations, max_egress_bps, active_allocations,
			certificate_fingerprint, certificate_expires_at, node_token_hash,
			last_heartbeat_at, lease_expires_at, created_at, updated_at
		) VALUES ($1, $1, 'hk', 'hk-2', 'integration', 'READY', '1.0.0', 2, $2, $3,
		          10, 10000000, 0, $4, $5, $6, $7, $8, $7, $7)
	`, thirdNodeID, endpoints, []string{"UDP"}, fmt.Sprintf("%064x", suffix+2),
		now.Add(time.Hour), hashToken("node-token-"+thirdNodeID), now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if planned, dispatched, err := service.MigrateFailedRelays(ctx); err != nil || planned != 1 || dispatched != 1 {
		t.Fatalf("timeout migration retry sweep = %d/%d, %v", planned, dispatched, err)
	}
	if len(coordinator.migrations) != 3 || coordinator.migrations[2].Attempt != 2 ||
		coordinator.migrations[2].Allocation.Endpoint.NodeID != thirdNodeID {
		t.Fatalf("retry migration = %#v", coordinator.migrations)
	}

	now = now.Add(time.Duration(cfg.MigrationTimeoutSeconds+1) * time.Second)
	if planned, dispatched, err := service.MigrateFailedRelays(ctx); err != nil || planned != 0 || dispatched != 0 {
		t.Fatalf("exhausted migration sweep = %d/%d, %v", planned, dispatched, err)
	}
	if len(coordinator.failed) != 1 || coordinator.failed[0][0] != timeoutConnectionID ||
		coordinator.failed[0][2] != "MIGRATION_ATTEMPTS_EXHAUSTED" {
		t.Fatalf("migration failures = %#v", coordinator.failed)
	}

	if _, err := pool.Exec(ctx, `
		UPDATE relay_nodes
		SET state = 'DRAINING', drain_migrate_existing = TRUE, drain_deadline = $2
		WHERE id = $1
	`, newNodeID, now.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if planned, dispatched, err := service.MigrateDrainRelays(ctx); err != nil || planned != 1 || dispatched != 1 {
		t.Fatalf("drain migration sweep = %d/%d, %v", planned, dispatched, err)
	}
	last := coordinator.migrations[len(coordinator.migrations)-1]
	if last.Reason != "RELAY_DRAIN" || last.PreviousRelayNodeID != newNodeID || last.Allocation.Endpoint.NodeID != thirdNodeID {
		t.Fatalf("drain migration = %#v", last)
	}
}
