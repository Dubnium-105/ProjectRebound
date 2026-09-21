package matchlobby

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/config"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/database"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/p2pbattlelog"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/p2proom"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/vnt"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestTransportVNTBootstrapRejectsEndingButKeepsPresenceAndHeartbeatAgainstPostgreSQL(t *testing.T) {
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

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	owner := insertStrictRosterPlayer(t, ctx, pool, fmt.Sprintf("%017d", 83_000_000_000_000_000+time.Now().UnixNano()%10_000_000_000_000))
	member := insertStrictRosterPlayer(t, ctx, pool, fmt.Sprintf("%017d", 84_000_000_000_000_000+time.Now().UnixNano()%10_000_000_000_000))
	playerIDs := []string{owner.PlayerID, member.PlayerID}
	nodeID := "vnt_ending_" + suffix
	now := time.Now().UTC().Truncate(time.Second)
	if _, err := pool.Exec(ctx, `
		INSERT INTO vnt_nodes (
			id, owner_player_id, advertised_host, port, region, location, state,
			vnts_version, wrapper_version, server_key_fingerprint,
			supported_transports, max_rooms, reported_sessions,
			last_heartbeat_at, last_reachable_at, created_at, updated_at
		) VALUES ($1, $2, '203.0.113.31', 33100, 'vnt-ending', 'Test', 'ONLINE',
		          '1.0.0', '1.0.0',
		          'sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef',
		          ARRAY['tcp','udp'], 10, 0, $3, $3, $3, $3)
	`, nodeID, owner.PlayerID, now); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM vnt_security_audit_logs WHERE node_id = $1 OR player_id = ANY($2)", nodeID, playerIDs)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM match_lobbies WHERE owner_player_id = ANY($1)", playerIDs)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM p2p_vnt_member_sessions WHERE player_id = ANY($1)", playerIDs)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM p2p_room_members WHERE player_id = ANY($1)", playerIDs)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM p2p_rooms WHERE host_player_id = ANY($1)", playerIDs)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM vnt_nodes WHERE id = $1", nodeID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM players WHERE id = ANY($1)", playerIDs)
	})

	p2pService := p2proom.NewService(p2proom.NewRepository(pool), config.Defaults.P2PRoom)
	secretBox, _, err := p2proom.NewSecretBox("", "test")
	if err != nil {
		t.Fatal(err)
	}
	p2pService.SetVNT(vnt.NewRepository(pool), secretBox)
	p2pService.SetVNTEnabled(true)
	p2pService.SetVNTVersionPolicy(vnt.NewVersionPolicy([]string{"1.0.0"}, []string{"1.0.0"}))
	battleLogService := p2pbattlelog.NewService(p2pbattlelog.NewRepository(pool), config.Defaults.P2PBattleLog)
	p2pService.SetMatchLifecycle(battleLogService)
	matchConfig := config.Defaults.MatchLobby
	matchConfig.AcceptNewLobbies = true
	signer, err := NewAdmissionSigner("integration-transport-ending", testAdmissionPrivateKey(), "test")
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(NewRepository(pool), matchConfig, signer, 45*time.Second)
	service.SetP2PTransport(p2pService)
	service.SetP2PMatchProjector(battleLogService)

	created, err := service.Create(ctx, owner, CreateInput{
		DisplayName: "Transport ending", HostingKind: HostingP2P, TransportKind: TransportVNT,
		VNTNodeID: nodeID, Mode: "TDM", Region: "hk", ClientVersion: "1.0.0", ProtocolVersion: 1,
		TeamOneCapacity: 2, TeamTwoCapacity: 2, TeamID: 1,
		IdempotencyKey: "integration-transport-ending-" + suffix,
	})
	if err != nil {
		t.Fatal(err)
	}
	joined, err := service.Join(ctx, member, created.Snapshot.LobbyID, 2, created.Snapshot.RosterRevision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetReady(ctx, owner, joined.LobbyID, true, joined.RosterRevision); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetReady(ctx, member, joined.LobbyID, true, joined.RosterRevision); err != nil {
		t.Fatal(err)
	}
	frozen, err := service.Start(ctx, owner, joined.LobbyID, joined.RosterRevision)
	if err != nil || frozen.Attempt == nil {
		t.Fatalf("start VNT lobby = %+v, %v", frozen, err)
	}
	var authoritySession string
	if err := pool.QueryRow(ctx, `SELECT authority_session_id FROM match_attempts WHERE id = $1`, frozen.Attempt.AttemptID).Scan(&authoritySession); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE match_attempts
		SET state = 'ENDING', lifecycle_phase = 'RESULT_CONFIRMED',
		    result_confirmed_at = $2, ending_deadline = $2::timestamptz + interval '2 minutes', updated_at = $2
		WHERE id = $1
	`, frozen.Attempt.AttemptID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE match_lobbies SET state = 'ENDING', updated_at = $2 WHERE id = $1`, frozen.LobbyID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE p2p_vnt_sessions
		SET state = 'HOST_READY', host_virtual_ip = '10.26.0.2'::inet, updated_at = $2
		WHERE room_id = $1
	`, frozen.P2PRoomID, now); err != nil {
		t.Fatal(err)
	}

	scope := TransportScopeRequest{
		AttemptID: frozen.Attempt.AttemptID, RosterRevision: frozen.Attempt.RosterRevision,
		RouteGeneration: frozen.Attempt.RouteGeneration,
	}
	if _, err := service.TransportVNTBootstrap(ctx, member, scope); errorCode(err) != "MATCH_TRANSPORT_ENDING" {
		t.Fatalf("ENDING VNT bootstrap = %v, want MATCH_TRANSPORT_ENDING", err)
	}
	var memberVirtualIP string
	if err := pool.QueryRow(ctx, `
		SELECT host(virtual_ip) FROM p2p_vnt_member_sessions
		WHERE room_id = $1 AND player_id = $2
	`, frozen.P2PRoomID, member.PlayerID).Scan(&memberVirtualIP); err != nil {
		t.Fatal(err)
	}
	if _, err := service.TransportVNTPresence(ctx, member, scope, p2proom.VNTPresenceInput{
		Generation: 1, State: "CONNECTED", VirtualIP: memberVirtualIP, ObservedPath: "UNKNOWN",
	}); err != nil {
		t.Fatalf("ENDING VNT presence was rejected: %v", err)
	}
	if err := service.P2PAuthorityHeartbeat(ctx, owner, authoritySession, frozen.Attempt.AttemptID); err != nil {
		t.Fatalf("ENDING P2P authority heartbeat was rejected: %v", err)
	}
}
