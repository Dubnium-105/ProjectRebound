package connection

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/database"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/player"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRepositoryCleanupRetriesTerminalConnectionsWithLiveRelayAllocation(t *testing.T) {
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
	hostID := insertRepositoryCleanupPlayer(t, ctx, pool, fmt.Sprintf("%017d", 61_000_000_000_000_000+suffix%1_000_000_000_000_000))
	peerID := insertRepositoryCleanupPlayer(t, ctx, pool, fmt.Sprintf("%017d", 62_000_000_000_000_000+suffix%1_000_000_000_000_000))
	roomID := newID("room_cleanup_")
	nodeID := newID("relay_cleanup_")
	connectionIDs := []string{newID("conn_cleanup_"), newID("conn_cleanup_")}
	allocationIDs := []string{newID("alloc_cleanup_"), newID("alloc_cleanup_")}
	now := time.Now().UTC()

	if _, err := pool.Exec(ctx, `
		INSERT INTO p2p_rooms (
			id, host_player_id, host_token_hash, display_name, region, mode, version,
			max_players, player_count, state, last_heartbeat_at, created_at, updated_at, expires_at
		) VALUES ($1, $2, $3, 'repository cleanup', 'test', 'TDM', 'test', 2, 2,
		          'LOBBY', $4, $4, $4, $5)
	`, roomID, hostID, []byte(roomID), now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO relay_nodes (
			id, display_name, region, zone, provider, state, software_version,
			protocol_version, public_endpoints, supported_protocols, max_allocations,
			max_egress_bps, certificate_fingerprint, certificate_expires_at,
			node_token_hash, created_at, updated_at
		) VALUES ($1, 'repository cleanup relay', 'test', 'test-1', 'test', 'READY', 'test',
		          2, '{}'::jsonb, ARRAY['UDP']::text[], 10, 1000000, $2, $3, $4, $5, $5)
	`, nodeID, fmt.Sprintf("%064x", suffix), now.Add(time.Hour), []byte(nodeID), now); err != nil {
		t.Fatal(err)
	}
	for index := range connectionIDs {
		if _, err := pool.Exec(ctx, `
			INSERT INTO connections (
				id, room_id, host_player_id, peer_player_id, state, expires_at,
				created_at, updated_at, closed_at
			) VALUES ($1, $2, $3, $4, 'CLOSED', $5, $6, $6, $7)
		`, connectionIDs[index], roomID, hostID, peerID, now.Add(-time.Minute), now.Add(-2*time.Minute), now.Add(-time.Minute)); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO relay_allocations (
				id, connection_id, room_id, relay_node_id, state, protocol,
				max_bps, max_pps, max_total_bytes, expires_at, created_at, updated_at
			) VALUES ($1, $2, $3, $4, 'ACTIVE', 'UDP', 1000, 100, 1000000, $5, $6, $6)
		`, allocationIDs[index], connectionIDs[index], roomID, nodeID, now.Add(time.Hour), now.Add(-time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM connections WHERE room_id = $1", roomID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM p2p_rooms WHERE id = $1", roomID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM relay_nodes WHERE id = $1", nodeID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM players WHERE id = ANY($1)", []string{hostID, peerID})
	})

	repository := NewRepository(pool)
	closed, err := repository.CloseForRoom(ctx, roomID, "RETRY", now)
	if err != nil {
		t.Fatalf("CloseForRoom() error = %v", err)
	}
	if len(closed) != len(connectionIDs) {
		t.Fatalf("CloseForRoom() returned %d connections, want %d", len(closed), len(connectionIDs))
	}
	memberClosed, err := repository.CloseForRoomMember(ctx, roomID, peerID, "RETRY_MEMBER", now.Add(time.Second))
	if err != nil {
		t.Fatalf("CloseForRoomMember() error = %v", err)
	}
	if len(memberClosed) != len(connectionIDs) {
		t.Fatalf("CloseForRoomMember() returned %d connections, want %d", len(memberClosed), len(connectionIDs))
	}

	expired, err := repository.SweepExpired(ctx, now.Add(2*time.Second))
	if err != nil {
		t.Fatalf("SweepExpired() error = %v", err)
	}
	matched := 0
	for _, item := range expired {
		if item.RoomID != roomID {
			continue
		}
		matched++
		if item.State != StateClosed {
			t.Fatalf("SweepExpired() changed terminal state for %s to %s", item.ID, item.State)
		}
	}
	if matched != len(connectionIDs) {
		t.Fatalf("SweepExpired() returned %d fixture connections, want %d", matched, len(connectionIDs))
	}
	for _, allocationID := range allocationIDs {
		var state string
		var revokeRequestedAt *time.Time
		if err := pool.QueryRow(ctx, `
			SELECT state, revoke_requested_at FROM relay_allocations WHERE id = $1
		`, allocationID).Scan(&state, &revokeRequestedAt); err != nil {
			t.Fatal(err)
		}
		if state != "ACTIVE" || revokeRequestedAt != nil {
			t.Fatalf("allocation %s changed unexpectedly: state=%s revoke_requested_at=%v", allocationID, state, revokeRequestedAt)
		}
	}
}

func insertRepositoryCleanupPlayer(t *testing.T, ctx context.Context, pool *pgxpool.Pool, steamID string) string {
	t.Helper()
	id := newID("player_cleanup_")
	now := time.Now().UTC()
	if _, err := pool.Exec(ctx, `
		INSERT INTO players (
			id, steam_id, persona_name, account_status, auth_provider, auth_level,
			created_at, updated_at
		) VALUES ($1, $2, 'Repository Cleanup', $3, 'steam_ticket', 'verified', $4, $4)
	`, id, steamID, player.AccountStatusActive, now); err != nil {
		t.Fatal(err)
	}
	return id
}
