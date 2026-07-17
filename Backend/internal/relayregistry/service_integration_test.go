package relayregistry

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/projectrebound/matchserver/internal/config"
	"github.com/projectrebound/matchserver/internal/connection"
	"github.com/projectrebound/matchserver/internal/database"
)

type fixedRoomDirectory struct{ region string }

func (d fixedRoomDirectory) RelayRegion(context.Context, string) (string, error) {
	return d.region, nil
}

func TestRelayRegistryLifecycleAgainstPostgreSQL(t *testing.T) {
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
	defer pool.Close()
	if err := database.NewMigrator(pool).Up(ctx); err != nil {
		t.Fatalf("migrate test database: %v", err)
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
	repository := NewRepository(pool)
	service := NewService(repository, authority, tokenManager, fixedRoomDirectory{region: "hk"}, cfg)
	fixedNow := time.Now().UTC().Truncate(time.Second)
	service.now = func() time.Time { return fixedNow }

	suffix := time.Now().UnixNano()
	bootstrapID := fmt.Sprintf("integration-relay-%d", suffix)
	bootstrapToken := fmt.Sprintf("integration-bootstrap-token-%048d", suffix)
	credentials, err := ParseBootstrapCredentials(bootstrapID + "=" + bootstrapToken)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Initialize(ctx, credentials); err != nil {
		t.Fatal(err)
	}

	var nodeIDs []string
	var allocationIDs []string
	roomID := fmt.Sprintf("room_relay_%d", suffix)
	connectionIDs := []string{
		fmt.Sprintf("conn_relay_a_%d", suffix),
		fmt.Sprintf("conn_relay_b_%d", suffix),
		fmt.Sprintf("conn_relay_c_%d", suffix),
	}
	playerIDs := []string{
		fmt.Sprintf("p_relay_host_%d", suffix),
		fmt.Sprintf("p_relay_peer1_%d", suffix),
		fmt.Sprintf("p_relay_peer2_%d", suffix),
		fmt.Sprintf("p_relay_peer3_%d", suffix),
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM relay_node_audit_logs WHERE node_id = ANY($1)", nodeIDs)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM relay_allocations WHERE id = ANY($1)", allocationIDs)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM relay_bootstrap_tokens WHERE id = $1", bootstrapID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM relay_nodes WHERE id = ANY($1)", nodeIDs)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM connections WHERE id = ANY($1)", connectionIDs)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM p2p_rooms WHERE id = $1", roomID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM players WHERE id = ANY($1)", playerIDs)
	})

	enrollInput := EnrollInput{
		DisplayName: "relay-hk-a", Region: "hk", Zone: "hk-1", Provider: "integration",
		SoftwareVersion: "1.0.0", ProtocolVersion: 1,
		PublicEndpoints:    []Endpoint{{Protocol: "UDP", Host: "8.8.8.8", Port: 443}},
		SupportedProtocols: []string{"UDP"}, MaxAllocations: 10, MaxEgressBPS: 10_000_000,
		CSRPEM: testCSR(t),
	}
	enrolled, err := service.Enroll(ctx, bootstrapToken, enrollInput)
	if err != nil {
		t.Fatal(err)
	}
	nodeIDs = append(nodeIDs, enrolled.Node.ID)
	if enrolled.NodeToken == "" || enrolled.CertificatePEM == "" || enrolled.CACertificatePEM == "" {
		t.Fatalf("incomplete enrollment result: %#v", enrolled)
	}
	enrollInput.CSRPEM = testCSR(t)
	if _, err := service.Enroll(ctx, bootstrapToken, enrollInput); relayErrorCode(err) != "BOOTSTRAP_UNAUTHORIZED" {
		t.Fatalf("consumed bootstrap token error = %v", err)
	}
	var storedNodeTokenHash []byte
	if err := pool.QueryRow(ctx, "SELECT node_token_hash FROM relay_nodes WHERE id = $1", enrolled.Node.ID).Scan(&storedNodeTokenHash); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(storedNodeTokenHash, hashToken(enrolled.NodeToken)) || bytes.Contains(storedNodeTokenHash, []byte(enrolled.NodeToken)) {
		t.Fatal("relay node credential was not stored as a one-way hash")
	}

	renewed, err := service.RenewCertificate(ctx, enrolled.Node.ID, enrolled.NodeToken, testCSR(t))
	if err != nil {
		t.Fatal(err)
	}
	if renewed.Node.CertificateFingerprint == enrolled.Node.CertificateFingerprint || renewed.NodeToken != "" {
		t.Fatal("certificate renewal did not rotate the certificate safely")
	}
	connecting, err := service.MarkConnecting(ctx, enrolled.Node.ID, renewed.Node.CertificateFingerprint, "1.0.1", 1)
	if err != nil || connecting.State != StateConnecting {
		t.Fatalf("connecting node = %#v, %v", connecting, err)
	}
	ready, err := service.Heartbeat(ctx, enrolled.Node.ID, HeartbeatInput{})
	if err != nil || ready.State != StateReady {
		t.Fatalf("ready node = %#v, %v", ready, err)
	}

	insertRelayConnectionFixtures(t, ctx, pool, suffix, roomID, playerIDs, connectionIDs, fixedNow)
	first, err := service.AllocateRelay(ctx, connection.RelayAllocationRequest{ConnectionID: connectionIDs[0], RoomID: roomID})
	if err != nil {
		t.Fatal(err)
	}
	allocationIDs = append(allocationIDs, first.AllocationID)
	if first.Endpoint.NodeID != enrolled.Node.ID || first.HostToken == first.PeerToken {
		t.Fatalf("invalid scheduled allocation: %#v", first)
	}
	hostClaims, err := tokenManager.Verify(first.HostToken, enrolled.Node.ID)
	if err != nil || hostClaims.EndpointRole != "HOST" {
		t.Fatalf("host claims = %#v, %v", hostClaims, err)
	}
	peerClaims, err := tokenManager.Verify(first.PeerToken, enrolled.Node.ID)
	if err != nil || peerClaims.EndpointRole != "PEER" || peerClaims.TokenID == hostClaims.TokenID {
		t.Fatalf("peer claims = %#v, %v", peerClaims, err)
	}
	if err := service.AllocationOpened(ctx, enrolled.Node.ID, first.AllocationID); err != nil {
		t.Fatal(err)
	}

	meta := AdminMeta{ActorID: "integration-admin", RequestID: "req-integration", IPAddress: "127.0.0.1"}
	draining, err := service.Drain(ctx, enrolled.Node.ID, meta)
	if err != nil || draining.State != StateDraining {
		t.Fatalf("draining node = %#v, %v", draining, err)
	}
	if _, err := service.AllocateRelay(ctx, connection.RelayAllocationRequest{ConnectionID: connectionIDs[1], RoomID: roomID}); relayErrorCode(err) != "RELAY_UNAVAILABLE" {
		t.Fatalf("draining node accepted allocation: %v", err)
	}
	if _, err := service.Resume(ctx, enrolled.Node.ID, meta); err != nil {
		t.Fatal(err)
	}
	second, err := service.AllocateRelay(ctx, connection.RelayAllocationRequest{ConnectionID: connectionIDs[1], RoomID: roomID})
	if err != nil {
		t.Fatal(err)
	}
	allocationIDs = append(allocationIDs, second.AllocationID)

	if _, err := pool.Exec(ctx, "UPDATE relay_nodes SET state = 'READY', last_heartbeat_at = $2 WHERE id = $1", enrolled.Node.ID, fixedNow.Add(-46*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SweepNodes(ctx); err != nil {
		t.Fatal(err)
	}
	unhealthy, err := service.Get(ctx, enrolled.Node.ID)
	if err != nil || unhealthy.State != StateUnhealthy {
		t.Fatalf("unhealthy node = %#v, %v", unhealthy, err)
	}
	if _, err := service.AllocateRelay(ctx, connection.RelayAllocationRequest{ConnectionID: connectionIDs[2], RoomID: roomID}); relayErrorCode(err) != "RELAY_UNAVAILABLE" {
		t.Fatalf("unhealthy node accepted allocation: %v", err)
	}
	if _, err := service.Heartbeat(ctx, enrolled.Node.ID, HeartbeatInput{ActiveAllocations: 2}); err != nil {
		t.Fatal(err)
	}
	third, err := service.AllocateRelay(ctx, connection.RelayAllocationRequest{ConnectionID: connectionIDs[2], RoomID: roomID})
	if err != nil {
		t.Fatal(err)
	}
	allocationIDs = append(allocationIDs, third.AllocationID)

	if _, err := pool.Exec(ctx, "UPDATE relay_nodes SET last_heartbeat_at = $2 WHERE id = $1", enrolled.Node.ID, fixedNow.Add(-91*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SweepNodes(ctx); err != nil {
		t.Fatal(err)
	}
	offline, err := service.Get(ctx, enrolled.Node.ID)
	if err != nil || offline.State != StateOffline {
		t.Fatalf("offline node = %#v, %v", offline, err)
	}
	if _, err := service.Heartbeat(ctx, enrolled.Node.ID, HeartbeatInput{ActiveAllocations: 3}); err != nil {
		t.Fatalf("recover node: %v", err)
	}
	revoked, err := service.Revoke(ctx, enrolled.Node.ID, meta)
	if err != nil || revoked.State != StateRevoked {
		t.Fatalf("revoked node = %#v, %v", revoked, err)
	}
	if _, err := service.MarkConnecting(ctx, enrolled.Node.ID, renewed.Node.CertificateFingerprint, "1.0.1", 1); relayErrorCode(err) != "RELAY_NODE_UNAUTHORIZED" {
		t.Fatalf("revoked certificate was accepted: %v", err)
	}
	var auditCount int
	if err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM relay_node_audit_logs WHERE node_id = $1", enrolled.Node.ID).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount < 3 {
		t.Fatalf("relay state audit count = %d", auditCount)
	}
}

func insertRelayConnectionFixtures(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	suffix int64,
	roomID string,
	playerIDs, connectionIDs []string,
	now time.Time,
) {
	t.Helper()
	for index, playerID := range playerIDs {
		steamID := fmt.Sprintf("%017d", 70_000_000_000_000_000+(suffix%1_000_000_000_000)+int64(index))
		if _, err := pool.Exec(ctx, `
			INSERT INTO players (id, steam_id, persona_name, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $4)
		`, playerID, steamID, fmt.Sprintf("Relay Player %d", index), now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO p2p_rooms (
			id, host_player_id, host_token_hash, display_name, region, mode, version,
			max_players, player_count, state, last_heartbeat_at, created_at, updated_at
		) VALUES ($1, $2, $3, 'Relay Integration', 'hk', 'coop', '1.0.0', 4, 4, 'CONNECTING', $4, $4, $4)
	`, roomID, playerIDs[0], hashToken(fmt.Sprintf("room-token-%d", suffix)), now); err != nil {
		t.Fatal(err)
	}
	for index, connectionID := range connectionIDs {
		if _, err := pool.Exec(ctx, `
			INSERT INTO connections (
				id, room_id, host_player_id, peer_player_id, state, expires_at, created_at, updated_at
			) VALUES ($1, $2, $3, $4, 'ALLOCATING_RELAY', $5, $6, $6)
		`, connectionID, roomID, playerIDs[0], playerIDs[index+1], now.Add(10*time.Minute), now); err != nil {
			t.Fatal(err)
		}
	}
}

func relayErrorCode(err error) string {
	if err == nil {
		return ""
	}
	_, code, _, _ := errorDetails(err)
	return code
}
