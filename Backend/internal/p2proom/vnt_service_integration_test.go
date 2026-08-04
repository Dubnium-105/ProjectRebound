package p2proom

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/config"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/database"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/entitlement"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/player"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/vnt"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type allowRoomRegistration struct {
	capabilities []string
}

func (a *allowRoomRegistration) Has(_ context.Context, _ string, capability string) (bool, error) {
	a.capabilities = append(a.capabilities, capability)
	return capability == entitlement.P2PRoomRegistration, nil
}

func TestVNTRoomLifecycleAgainstPostgreSQL(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := database.NewMigrator(pool).Up(ctx); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}

	fixedNow := time.Now().UTC().Truncate(time.Second)
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	actors := []Actor{
		insertTestPlayer(t, ctx, pool, fmt.Sprintf("%017d", fixedNow.UnixNano()%100_000_000_000_000_000), player.AccountStatusActive),
		insertTestPlayer(t, ctx, pool, fmt.Sprintf("%017d", (fixedNow.UnixNano()+1)%100_000_000_000_000_000), player.AccountStatusActive),
	}
	playerIDs := []string{actors[0].PlayerID, actors[1].PlayerID}
	nodeIDs := []string{"vnt_" + suffix[:30] + "21", "vnt_" + suffix[:30] + "22"}
	for index, nodeID := range nodeIDs {
		if _, err := pool.Exec(ctx, `
			INSERT INTO vnt_nodes (
				id, owner_player_id, advertised_host, port, region, location, state,
				vnts_version, wrapper_version, server_key_fingerprint,
				supported_transports, max_rooms, reported_sessions,
				last_heartbeat_at, last_reachable_at, created_at, updated_at
			) VALUES ($1,$2,'203.0.113.30',$3,'vnt-room-test','Test','ONLINE',
			          '1.0.0','1.0.0',$4,ARRAY['tcp','udp'],10,0,$5,$5,$5,$5)
		`, nodeID, actors[0].PlayerID, 33000+index,
			"sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", fixedNow); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM vnt_security_audit_logs WHERE node_id = ANY($1) OR player_id = ANY($2)", nodeIDs, playerIDs)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM p2p_room_members WHERE player_id = ANY($1)", playerIDs)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM p2p_rooms WHERE host_player_id = $1", actors[0].PlayerID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM vnt_nodes WHERE id = ANY($1)", nodeIDs)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM players WHERE id = ANY($1)", playerIDs)
	})

	secretBox, _, err := NewSecretBox("", "test")
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(NewRepository(pool), config.Defaults.P2PRoom)
	service.now = func() time.Time { return fixedNow }
	entitlements := &allowRoomRegistration{}
	service.SetEntitlementChecker(entitlements)
	service.SetVNT(vnt.NewRepository(pool), secretBox)
	service.SetVNTEnabled(true)
	service.SetVNTVersionPolicy(vnt.NewVersionPolicy([]string{"1.0.0"}, []string{"1.0.0"}))
	auditCtx := vnt.WithRequestMeta(ctx, vnt.RequestMeta{
		RequestID: "req_vnt_room_" + suffix[:16], IPAddress: "192.0.2.81", UserAgent: "vnt-room-integration-test",
	})
	request := CreateInput{
		DisplayName: "VNT Integration", Region: "hk", Mode: "coop", Version: "1.0.0",
		MaxPlayers: 4, TransportKind: TransportVNT, VNTNodeID: nodeIDs[0],
		IdempotencyKey: "vnt-integration-" + suffix,
	}
	if _, err := pool.Exec(ctx, "UPDATE vnt_nodes SET wrapper_version = '9.9.9' WHERE id = $1", nodeIDs[0]); err != nil {
		t.Fatal(err)
	}
	incompatibleRequest := request
	incompatibleRequest.IdempotencyKey += "-incompatible"
	if _, err := service.Create(ctx, actors[0], incompatibleRequest); err == nil {
		t.Fatal("room creation accepted a version-incompatible VNT node")
	}
	if _, err := pool.Exec(ctx, "UPDATE vnt_nodes SET wrapper_version = '1.0.0' WHERE id = $1", nodeIDs[0]); err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(ctx, actors[0], request)
	if err != nil {
		t.Fatal(err)
	}
	if created.Room.TransportKind != TransportVNT || created.Room.VNTNodeID != nodeIDs[0] ||
		created.Room.VNTGeneration != 1 || created.HostToken == "" {
		t.Fatalf("created VNT room = %#v", created)
	}
	if len(entitlements.capabilities) != 1 || entitlements.capabilities[0] != entitlement.P2PRoomRegistration {
		t.Fatalf("checked capabilities = %#v", entitlements.capabilities)
	}
	repeated, err := service.Create(ctx, actors[0], request)
	if err != nil || repeated.Room.ID != created.Room.ID || repeated.HostToken != created.HostToken {
		t.Fatalf("idempotent VNT create = %#v, %v", repeated, err)
	}

	firstBootstrap, err := service.VNTBootstrap(ctx, actors[0], created.Room.ID)
	if err != nil {
		t.Fatal(err)
	}
	if firstBootstrap.Generation != 1 || firstBootstrap.VirtualIP != "10.26.0.2" ||
		firstBootstrap.HostVirtualIP != nil || firstBootstrap.NetworkToken == "" || firstBootstrap.E2EPassword == "" {
		t.Fatalf("first host bootstrap = %#v", firstBootstrap)
	}
	if _, err := pool.Exec(ctx, "UPDATE vnt_nodes SET vnts_version = '9.9.9' WHERE id = $1", nodeIDs[1]); err != nil {
		t.Fatal(err)
	}
	if _, err := service.VNTRebind(auditCtx, actors[0], created.Room.ID, created.HostToken, nodeIDs[1]); err == nil {
		t.Fatal("room rebind accepted a version-incompatible VNT node")
	}
	if _, err := pool.Exec(ctx, "UPDATE vnt_nodes SET vnts_version = '1.0.0' WHERE id = $1", nodeIDs[1]); err != nil {
		t.Fatal(err)
	}
	rebound, err := service.VNTRebind(auditCtx, actors[0], created.Room.ID, created.HostToken, nodeIDs[1])
	if err != nil {
		t.Fatal(err)
	}
	if rebound.VNTGeneration != 2 || rebound.VNTNodeID != nodeIDs[1] || rebound.VNTState != "SELECTED" {
		t.Fatalf("rebound VNT room = %#v", rebound)
	}
	secondBootstrap, err := service.VNTBootstrap(ctx, actors[0], created.Room.ID)
	if err != nil {
		t.Fatal(err)
	}
	if secondBootstrap.Generation != 2 || secondBootstrap.VirtualIP != "10.26.0.2" ||
		secondBootstrap.NetworkToken == firstBootstrap.NetworkToken || secondBootstrap.E2EPassword == firstBootstrap.E2EPassword {
		t.Fatalf("second host bootstrap did not rotate generation secrets: %#v", secondBootstrap)
	}
	var originalNetworkCiphertext []byte
	if err := pool.QueryRow(ctx, `
		SELECT network_token_ciphertext FROM p2p_vnt_sessions WHERE room_id = $1
	`, created.Room.ID).Scan(&originalNetworkCiphertext); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE p2p_vnt_sessions SET network_token_ciphertext = $2 WHERE room_id = $1
	`, created.Room.ID, []byte("tampered-ciphertext")); err != nil {
		t.Fatal(err)
	}
	if _, err := service.VNTBootstrap(auditCtx, actors[0], created.Room.ID); err == nil {
		t.Fatal("tampered VNT room secret was accepted")
	} else if status, _, _, _ := errorDetails(err); status != 500 {
		t.Fatalf("tampered VNT room secret bootstrap = %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE p2p_vnt_sessions SET network_token_ciphertext = $2 WHERE room_id = $1
	`, created.Room.ID, originalNetworkCiphertext); err != nil {
		t.Fatal(err)
	}

	joined, err := service.Join(ctx, actors[1], created.Room.ID, "1.0.0")
	if err != nil || joined.PlayerCount != 2 {
		t.Fatalf("VNT join = %#v, %v", joined, err)
	}
	if _, err := service.VNTBootstrap(ctx, actors[1], created.Room.ID); roomErrorCode(err) != "VNT_HOST_NOT_READY" {
		t.Fatalf("bootstrap before host readiness = %v", err)
	}
	ready, err := service.VNTHostReady(ctx, actors[0], created.Room.ID, created.HostToken, 2, "10.26.0.2")
	if err != nil || ready.VNTState != "HOST_READY" {
		t.Fatalf("VNT host ready = %#v, %v", ready, err)
	}
	memberBootstrap, err := service.VNTBootstrap(ctx, actors[1], created.Room.ID)
	if err != nil {
		t.Fatal(err)
	}
	if memberBootstrap.Generation != 2 || memberBootstrap.VirtualIP != "10.26.0.3" ||
		memberBootstrap.HostVirtualIP == nil || *memberBootstrap.HostVirtualIP != "10.26.0.2" ||
		memberBootstrap.NetworkToken != secondBootstrap.NetworkToken || memberBootstrap.E2EPassword != secondBootstrap.E2EPassword {
		t.Fatalf("member bootstrap = %#v", memberBootstrap)
	}
	if _, err := service.UpdateVNTPresence(ctx, actors[1], created.Room.ID, VNTPresenceInput{
		Generation: 2, State: "CONNECTED", VirtualIP: "10.26.0.99", ObservedPath: "P2P",
	}); roomErrorCode(err) != "VNT_PRESENCE_MISMATCH" {
		t.Fatalf("mismatched VNT presence = %v", err)
	}
	if _, err := service.UpdateVNTPresence(ctx, actors[1], created.Room.ID, VNTPresenceInput{
		Generation: 2, State: "CONNECTED", VirtualIP: memberBootstrap.VirtualIP, ObservedPath: "P2P",
	}); err != nil {
		t.Fatal(err)
	}
	started, err := service.Start(ctx, actors[0], created.Room.ID, created.HostToken)
	if err != nil || started.State != StateRunning {
		t.Fatalf("VNT start = %#v, %v", started, err)
	}
	if _, err := service.VNTRebind(auditCtx, actors[0], created.Room.ID, created.HostToken, nodeIDs[0]); roomErrorCode(err) != "VNT_REBIND_NOT_ALLOWED" {
		t.Fatalf("rebind after start = %v", err)
	}
	left, err := service.Leave(ctx, actors[1], created.Room.ID)
	if err != nil || left.PlayerCount != 1 {
		t.Fatalf("VNT leave = %#v, %v", left, err)
	}
	closed, err := service.Delete(ctx, actors[0], created.Room.ID, created.HostToken)
	if err != nil || closed.State != StateClosed {
		t.Fatalf("VNT close = %#v, %v", closed, err)
	}

	var sessionState string
	var networkCiphertext, passwordCiphertext []byte
	if err := pool.QueryRow(ctx, `
		SELECT state, network_token_ciphertext, e2e_password_ciphertext
		FROM p2p_vnt_sessions WHERE room_id = $1
	`, created.Room.ID).Scan(&sessionState, &networkCiphertext, &passwordCiphertext); err != nil {
		t.Fatal(err)
	}
	if sessionState != "CLOSED" || bytes.Contains(networkCiphertext, []byte(secondBootstrap.NetworkToken)) ||
		bytes.Contains(passwordCiphertext, []byte(secondBootstrap.E2EPassword)) {
		t.Fatal("VNT session did not close cleanly or stored plaintext room secrets")
	}
	var reboundSucceeded, reboundDenied, decryptFailed int
	var auditDetails string
	if err := pool.QueryRow(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE event_type = 'VNT_ROOM_REBOUND' AND result = 'SUCCEEDED'),
			COUNT(*) FILTER (WHERE event_type = 'VNT_ROOM_REBIND_REJECTED' AND result = 'DENIED'),
			COUNT(*) FILTER (WHERE event_type = 'VNT_ROOM_SECRET_DECRYPTION_FAILED' AND result = 'FAILED'),
			COALESCE(string_agg(details::text, ' '), '')
		FROM vnt_security_audit_logs WHERE room_id = $1
	`, created.Room.ID).Scan(&reboundSucceeded, &reboundDenied, &decryptFailed, &auditDetails); err != nil {
		t.Fatal(err)
	}
	if reboundSucceeded != 1 || reboundDenied != 2 || decryptFailed != 1 {
		t.Fatalf("VNT room audit counts = success %d denied %d decrypt %d", reboundSucceeded, reboundDenied, decryptFailed)
	}
	for _, secret := range []string{firstBootstrap.NetworkToken, firstBootstrap.E2EPassword, secondBootstrap.NetworkToken, secondBootstrap.E2EPassword} {
		if strings.Contains(auditDetails, secret) {
			t.Fatal("VNT security audit persisted a plaintext room secret")
		}
	}
}
