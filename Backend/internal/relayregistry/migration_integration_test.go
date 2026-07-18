package relayregistry

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/projectrebound/matchserver/internal/config"
	"github.com/projectrebound/matchserver/internal/connection"
	"github.com/projectrebound/matchserver/internal/database"
)

type recordedMigrationCoordinator struct {
	migrations []connection.RelayMigration
	bound      [][3]string
}

func (c *recordedMigrationCoordinator) RelayMigrating(_ context.Context, _ string, migration connection.RelayMigration) error {
	c.migrations = append(c.migrations, migration)
	return nil
}

func (c *recordedMigrationCoordinator) RelayBound(_ context.Context, connectionID, allocationID, previousAllocationID string) error {
	c.bound = append(c.bound, [3]string{connectionID, allocationID, previousAllocationID})
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
	playerIDs := []string{fmt.Sprintf("p_migration_host_%d", suffix), fmt.Sprintf("p_migration_peer_%d", suffix)}
	roomID := fmt.Sprintf("room_migration_%d", suffix)
	connectionID := fmt.Sprintf("conn_migration_%d", suffix)
	oldNodeID := fmt.Sprintf("relay_old_%d", suffix)
	newNodeID := fmt.Sprintf("relay_new_%d", suffix)
	oldAllocationID := fmt.Sprintf("alloc_old_%d", suffix)
	nodeIDs := []string{oldNodeID, newNodeID}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM relay_migrations WHERE connection_id = $1", connectionID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM relay_allocations WHERE connection_id = $1", connectionID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM relay_nodes WHERE id = ANY($1)", nodeIDs)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM connections WHERE id = $1", connectionID)
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
			max_players, player_count, state, last_heartbeat_at, created_at, updated_at
		) VALUES ($1, $2, $3, 'Migration Room', 'hk', 'coop', '1.0.0', 2, 2, 'RUNNING', $4, $4, $4)
	`, roomID, playerIDs[0], hashToken("migration-room-token"), now); err != nil {
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
				last_heartbeat_at, created_at, updated_at
			) VALUES ($1, $1, $2, 'hk-1', 'integration', $3, '1.0.0', 1, $4, $5,
			          10, 10000000, $6, $7, $8, $9, $10, $10, $10)
		`, node.id, node.region, node.state, endpoints, []string{"UDP"}, node.active,
			node.fingerprint, now.Add(time.Hour), hashToken("node-token-"+node.id), now); err != nil {
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
	if messages := publisher.messages[oldNodeID]; len(messages) != 1 || messages[0].Type != "RevokeAllocation" {
		t.Fatalf("old relay commands = %#v", messages)
	}
	if planned, dispatched, err := service.MigrateFailedRelays(ctx); err != nil || planned != 0 || dispatched != 0 {
		t.Fatalf("idempotent migration sweep = %d/%d, %v", planned, dispatched, err)
	}
	if err := service.AllocationOpened(ctx, newNodeID, migration.Allocation.AllocationID); err != nil {
		t.Fatal(err)
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
}
