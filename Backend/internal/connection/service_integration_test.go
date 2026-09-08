package connection

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/config"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/database"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/matchlobby"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/p2pbattlelog"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/p2proom"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/player"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestConnectionLifecycleAgainstPostgreSQL(t *testing.T) {
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

	suffix := uint64(time.Now().UnixNano()) % 10_000_000_000_000
	host := insertConnectionPlayer(t, ctx, pool, fmt.Sprintf("%017d", 51_000_000_000_000_000+suffix), player.AccountStatusActive)
	peer := insertConnectionPlayer(t, ctx, pool, fmt.Sprintf("%017d", 52_000_000_000_000_000+suffix), player.AccountStatusActive)
	outsider := insertConnectionPlayer(t, ctx, pool, fmt.Sprintf("%017d", 53_000_000_000_000_000+suffix), player.AccountStatusActive)
	banned := insertConnectionPlayer(t, ctx, pool, fmt.Sprintf("%017d", 54_000_000_000_000_000+suffix), player.AccountStatusBanned)
	playerIDs := []string{host.PlayerID, peer.PlayerID, outsider.PlayerID, banned.PlayerID}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM connections WHERE host_player_id = ANY($1) OR peer_player_id = ANY($1)", playerIDs)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM p2p_match_sessions WHERE host_player_id_at_start = ANY($1)", playerIDs)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM match_lobbies WHERE owner_player_id = ANY($1)", playerIDs)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM p2p_room_members WHERE player_id = ANY($1)", playerIDs)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM p2p_rooms WHERE host_player_id = ANY($1)", playerIDs)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM players WHERE id = ANY($1)", playerIDs)
	})

	hub := NewHub(16)
	hostEvents := hub.Subscribe(host.PlayerID)
	defer hostEvents.Close()
	peerEvents := hub.Subscribe(peer.PlayerID)
	defer peerEvents.Close()
	roomService := p2proom.NewService(p2proom.NewRepository(pool), config.Defaults.P2PRoom)
	secretBox, _, err := p2proom.NewSecretBox("", "test")
	if err != nil {
		t.Fatal(err)
	}
	roomService.SetVNT(nil, secretBox)
	battleLogService := p2pbattlelog.NewService(p2pbattlelog.NewRepository(pool), config.Defaults.P2PBattleLog)
	roomService.SetMatchLifecycle(battleLogService)
	matchConfig := config.Defaults.MatchLobby
	matchConfig.AcceptNewLobbies = true
	seed := make([]byte, ed25519.SeedSize)
	for index := range seed {
		seed[index] = byte(index + 1)
	}
	signer, err := matchlobby.NewAdmissionSigner(
		"integration-connection-lifecycle",
		base64.StdEncoding.EncodeToString(seed),
		"test",
	)
	if err != nil {
		t.Fatal(err)
	}
	matchService := matchlobby.NewService(matchlobby.NewRepository(pool), matchConfig, signer, 45*time.Second)
	matchService.SetP2PTransport(roomService)
	matchService.SetP2PMatchProjector(battleLogService)
	service := NewService(NewRepository(pool), roomService, hub, config.Defaults.Connection)
	roomService.SetConnectionCreator(service)
	matchOwner := matchlobby.Actor{
		PlayerID: host.PlayerID, AccountStatus: host.AccountStatus,
		AuthLevel: player.AuthLevelVerified, SteamVerified: true,
	}
	matchMember := matchlobby.Actor{
		PlayerID: peer.PlayerID, AccountStatus: peer.AccountStatus,
		AuthLevel: player.AuthLevelVerified, SteamVerified: true,
	}
	created, err := matchService.Create(ctx, matchOwner, matchlobby.CreateInput{
		DisplayName: "Connection Integration", HostingKind: matchlobby.HostingP2P,
		TransportKind: matchlobby.TransportLegacy, Mode: "TDM", Region: "hk",
		ClientVersion: "1.0.0", ProtocolVersion: 1, TeamOneCapacity: 2,
		TeamTwoCapacity: 2, TeamID: 1,
		IdempotencyKey: "integration-connection-lifecycle",
	})
	if err != nil {
		t.Fatal(err)
	}
	joined, err := matchService.Join(ctx, matchMember, created.Snapshot.LobbyID, 2, created.Snapshot.RosterRevision)
	if err != nil {
		t.Fatal(err)
	}
	readyOwner, err := matchService.SetReady(ctx, matchOwner, joined.LobbyID, true, joined.RosterRevision)
	if err != nil {
		t.Fatal(err)
	}
	readyMember, err := matchService.SetReady(ctx, matchMember, joined.LobbyID, true, readyOwner.RosterRevision)
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := matchService.Start(ctx, matchOwner, joined.LobbyID, readyMember.RosterRevision)
	if err != nil {
		t.Fatal(err)
	}
	if frozen.Attempt == nil || frozen.Attempt.State != matchlobby.AttemptProvisioning {
		t.Fatalf("managed attempt was not provisioned: %+v", frozen.Attempt)
	}
	room, err := roomService.Get(ctx, frozen.P2PRoomID)
	if err != nil {
		t.Fatal(err)
	}
	roomHostToken := created.TransportHostToken

	connection, err := service.Create(ctx, peer, CreateInput{RoomID: room.ID})
	if err != nil {
		t.Fatal(err)
	}
	assertEventType(t, hostEvents, "connection.created")
	assertEventType(t, peerEvents, "connection.created")
	repeated, err := service.Create(ctx, peer, CreateInput{RoomID: room.ID})
	if err != nil || repeated.ID != connection.ID {
		t.Fatalf("idempotent create = %#v, %v", repeated, err)
	}
	if _, err := service.Create(ctx, banned, CreateInput{RoomID: room.ID}); connectionErrorCode(err) != "ACCOUNT_NOT_ACTIVE" {
		t.Fatalf("banned create error = %v", err)
	}
	if _, err := service.Get(ctx, outsider, connection.ID); connectionErrorCode(err) != "CONNECTION_FORBIDDEN" {
		t.Fatalf("outsider read error = %v", err)
	}

	if _, err := service.AddCandidate(ctx, host, CandidateInput{
		ConnectionID: connection.ID, Foundation: "host-lan", CandidateType: CandidateLAN,
		Protocol: "UDP", Address: "192.168.10.2", Port: 7777, Priority: 200,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AddCandidate(ctx, host, CandidateInput{
		ConnectionID: connection.ID, Foundation: "host-public", CandidateType: CandidateSRFLX,
		Protocol: "UDP", Address: "9.9.9.9", Port: 40002, Priority: 100,
	}); err != nil {
		t.Fatal(err)
	}
	peerLANInput := CandidateInput{
		ConnectionID: connection.ID, Foundation: "peer-lan", CandidateType: CandidateLAN,
		Protocol: "UDP", Address: "192.168.10.3", Port: 7777, Priority: 200,
	}
	peerLANCandidate, err := service.AddCandidate(ctx, peer, peerLANInput)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.AddCandidate(ctx, peer, CandidateInput{
		ConnectionID: connection.ID, Foundation: "peer-public", CandidateType: CandidateSRFLX,
		Protocol: "UDP", Address: "8.8.8.8", Port: 40000, Priority: 100,
	}); err != nil {
		t.Fatal(err)
	}
	checking, err := service.Get(ctx, host, connection.ID)
	if err != nil || checking.State != StateCheckingDirect || len(checking.Candidates) != 4 {
		t.Fatalf("candidate exchange = %#v, %v", checking, err)
	}
	connected, err := service.ReportCheck(ctx, peer, CheckResultInput{
		ConnectionID: connection.ID, Success: true, Path: PathLAN, LatencyMS: 4,
	})
	if err != nil || connected.State != StateConnected || connected.SelectedPath != PathLAN {
		t.Fatalf("direct path = %#v, %v", connected, err)
	}
	replayedCandidate, err := service.AddCandidate(ctx, peer, peerLANInput)
	if err != nil || replayedCandidate.ID != peerLANCandidate.ID {
		t.Fatalf("stable candidate replay after path selection = %#v, %v", replayedCandidate, err)
	}
	changedCandidate := peerLANInput
	changedCandidate.Port++
	if _, err := service.AddCandidate(ctx, peer, changedCandidate); connectionErrorCode(err) != "INVALID_CONNECTION_STATE" {
		t.Fatalf("changed candidate after path selection error = %v", err)
	}
	runningRoom, err := roomService.Get(ctx, room.ID)
	if err != nil {
		t.Fatal(err)
	}
	if runningRoom.State == p2proom.StateRunning {
		t.Fatalf("managed connected room was incorrectly promoted to RUNNING: %#v", runningRoom)
	}
	closed, err := service.Close(ctx, host, connection.ID)
	if err != nil || closed.State != StateClosed {
		t.Fatalf("close = %#v, %v", closed, err)
	}

	relayFallback, err := service.Create(ctx, peer, CreateInput{RoomID: room.ID})
	if err != nil || relayFallback.ID == connection.ID {
		t.Fatalf("replacement connection = %#v, %v", relayFallback, err)
	}
	if _, err := service.AddCandidate(ctx, host, CandidateInput{
		ConnectionID: relayFallback.ID, Foundation: "host-lan-2", CandidateType: CandidateLAN,
		Protocol: "UDP", Address: "10.0.0.2", Port: 7777, Priority: 200,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AddCandidate(ctx, host, CandidateInput{
		ConnectionID: relayFallback.ID, Foundation: "host-public-2", CandidateType: CandidateSRFLX,
		Protocol: "UDP", Address: "9.9.9.9", Port: 40002, Priority: 100,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AddCandidate(ctx, peer, CandidateInput{
		ConnectionID: relayFallback.ID, Foundation: "peer-lan-2", CandidateType: CandidateLAN,
		Protocol: "UDP", Address: "10.0.0.3", Port: 7777, Priority: 200,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AddCandidate(ctx, peer, CandidateInput{
		ConnectionID: relayFallback.ID, Foundation: "peer-public-2", CandidateType: CandidateSRFLX,
		Protocol: "UDP", Address: "1.1.1.1", Port: 40001, Priority: 100,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReportCheck(ctx, host, CheckResultInput{
		ConnectionID: relayFallback.ID, Success: false, Path: PathUDPPunch, LatencyMS: 2000,
	}); connectionErrorCode(err) != "PATH_PRIORITY_VIOLATION" {
		t.Fatalf("out-of-order path error = %v", err)
	}
	lanFailed, err := service.ReportCheck(ctx, host, CheckResultInput{
		ConnectionID: relayFallback.ID, Success: false, Path: PathLAN, LatencyMS: 10, Reason: "LAN unavailable",
	})
	if err != nil || lanFailed.State != StateCheckingDirect {
		t.Fatalf("LAN fallback = %#v, %v", lanFailed, err)
	}
	allocating, err := service.ReportCheck(ctx, host, CheckResultInput{
		ConnectionID: relayFallback.ID, Success: false, Path: PathUDPPunch, LatencyMS: 2000, Reason: "hole punch timed out",
	})
	if err != nil || allocating.State != StateAllocatingRelay {
		t.Fatalf("relay fallback = %#v, %v", allocating, err)
	}
	if _, err := pool.Exec(ctx, "UPDATE connections SET state = 'CONNECTED', selected_path = 'UDP_RELAY' WHERE id = $1", allocating.ID); err != nil {
		t.Fatal(err)
	}
	migrationHostEvents := hub.Subscribe(host.PlayerID)
	defer migrationHostEvents.Close()
	migrationPeerEvents := hub.Subscribe(peer.PlayerID)
	defer migrationPeerEvents.Close()
	migration := RelayMigration{
		MigrationID: "migration_replacement", PreviousAllocationID: "alloc_previous",
		PreviousRelayNodeID: "relay_previous", Reason: "RELAY_UNHEALTHY", Attempt: 1,
		Allocation: RelayAllocation{
			AllocationID: "alloc_replacement",
			Endpoint: RelayEndpoint{
				NodeID: "relay_replacement", Protocol: "udp", Host: "relay.example.test", Port: 3480,
			},
			HostToken: "host-token", PeerToken: "peer-token", ExpiresAt: time.Now().UTC().Add(10 * time.Minute),
		},
	}
	if err := service.RelayMigrating(ctx, allocating.ID, migration); err != nil {
		t.Fatal(err)
	}
	hostMigrationEvent := assertEventType(t, migrationHostEvents, "connection.relay_migrating")
	peerMigrationEvent := assertEventType(t, migrationPeerEvents, "connection.relay_migrating")
	hostMigrationPayload, ok := hostMigrationEvent.Payload.(map[string]any)
	if !ok || hostMigrationPayload["migration_id"] != migration.MigrationID || hostMigrationPayload["old_relay_node_id"] != migration.PreviousRelayNodeID {
		t.Fatalf("host migration payload = %#v", hostMigrationEvent.Payload)
	}
	peerMigrationPayload, ok := peerMigrationEvent.Payload.(map[string]any)
	if !ok || peerMigrationPayload["migration_id"] != migration.MigrationID {
		t.Fatalf("peer migration payload = %#v", peerMigrationEvent.Payload)
	}
	hostAllocationEvent := assertEventType(t, migrationHostEvents, "connection.relay_allocated")
	peerAllocationEvent := assertEventType(t, migrationPeerEvents, "connection.relay_allocated")
	hostAllocationPayload, _ := hostAllocationEvent.Payload.(map[string]any)
	peerAllocationPayload, _ := peerAllocationEvent.Payload.(map[string]any)
	if hostAllocationPayload["relay_token"] != "host-token" || peerAllocationPayload["relay_token"] != "peer-token" {
		t.Fatalf("migration allocation payloads = %#v / %#v", hostAllocationPayload, peerAllocationPayload)
	}
	if hostAllocationPayload["relay_token"] == peerAllocationPayload["relay_token"] {
		t.Fatal("relay migration exposed the same participant token to both endpoints")
	}
	migrating, err := service.Get(ctx, peer, allocating.ID)
	if err != nil || migrating.State != StateMigratingRelay {
		t.Fatalf("migrating connection = %#v, %v", migrating, err)
	}
	if err := service.RelayBound(ctx, allocating.ID, migration.Allocation.AllocationID, migration.PreviousAllocationID, migration.MigrationID); err != nil {
		t.Fatal(err)
	}
	assertEventType(t, migrationHostEvents, "connection.relay_migrated")
	assertEventType(t, migrationPeerEvents, "connection.relay_migrated")
	migrated, err := service.Get(ctx, peer, allocating.ID)
	if err != nil || migrated.State != StateConnected || migrated.SelectedPath != PathUDPRelay {
		t.Fatalf("migrated connection = %#v, %v", migrated, err)
	}
	renewalStartedAt := time.Now().UTC()
	oldExpiry := renewalStartedAt.Add(time.Second)
	closedExpiry := renewalStartedAt.Add(-time.Hour).Truncate(time.Microsecond)
	if _, err := pool.Exec(ctx, "UPDATE connections SET expires_at = $2 WHERE id = $1", allocating.ID, oldExpiry); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE connections SET expires_at = $2 WHERE id = $1", connection.ID, closedExpiry); err != nil {
		t.Fatal(err)
	}
	if _, err := roomService.Heartbeat(
		ctx,
		p2proom.Actor{PlayerID: host.PlayerID, AccountStatus: host.AccountStatus},
		room.ID,
		roomHostToken,
	); err != nil {
		t.Fatalf("room heartbeat connection renewal: %v", err)
	}
	var renewedExpiry time.Time
	if err := pool.QueryRow(ctx, "SELECT expires_at FROM connections WHERE id = $1", allocating.ID).Scan(&renewedExpiry); err != nil {
		t.Fatal(err)
	}
	minimumRenewedExpiry := renewalStartedAt.Add(config.Defaults.Connection.SessionTTL()).Add(-time.Second)
	if renewedExpiry.Before(minimumRenewedExpiry) {
		t.Fatalf("renewed expires_at = %v, want at least %v", renewedExpiry, minimumRenewedExpiry)
	}
	var terminalExpiry time.Time
	if err := pool.QueryRow(ctx, "SELECT expires_at FROM connections WHERE id = $1", connection.ID).Scan(&terminalExpiry); err != nil {
		t.Fatal(err)
	}
	if !terminalExpiry.Equal(closedExpiry) {
		t.Fatalf("closed connection expires_at = %v, want unchanged %v", terminalExpiry, closedExpiry)
	}
	originalNow := service.now
	service.now = func() time.Time { return oldExpiry.Add(time.Second) }
	if _, err := service.SweepExpired(ctx); err != nil {
		t.Fatalf("sweep after room heartbeat renewal: %v", err)
	}
	service.now = originalNow
	stillConnected, err := service.Get(ctx, peer, allocating.ID)
	if err != nil || stillConnected.State != StateConnected {
		t.Fatalf("renewed connection after old expiry = %#v, %v", stillConnected, err)
	}
	if _, err := pool.Exec(ctx, "UPDATE connections SET expires_at = $2 WHERE id = $1", allocating.ID, time.Now().UTC().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	if count, err := service.SweepExpired(ctx); err != nil || count < 1 {
		t.Fatalf("expiry sweep = %d, %v", count, err)
	}
	expired, err := service.Get(ctx, peer, allocating.ID)
	if err != nil || expired.State != StateExpired {
		t.Fatalf("expired connection = %#v, %v", expired, err)
	}
	roomBound, err := service.Create(ctx, peer, CreateInput{RoomID: room.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := roomService.DeleteManaged(
		ctx,
		p2proom.Actor{PlayerID: host.PlayerID, AccountStatus: host.AccountStatus},
		room.ID,
		roomHostToken,
	); err != nil {
		t.Fatal(err)
	}
	closedWithRoom, err := service.Get(ctx, peer, roomBound.ID)
	if err != nil || closedWithRoom.State != StateClosed || closedWithRoom.FailureReason != "ROOM_CLOSED" {
		t.Fatalf("room-bound connection closure = %#v, %v", closedWithRoom, err)
	}
}

func insertConnectionPlayer(t *testing.T, ctx context.Context, pool *pgxpool.Pool, steamID string, status player.AccountStatus) Actor {
	t.Helper()
	id := newID("player_")
	now := time.Now().UTC()
	if _, err := pool.Exec(ctx, `
		INSERT INTO players (
			id, steam_id, persona_name, account_status, auth_provider, auth_level,
			created_at, updated_at
		)
		VALUES ($1, $2, 'Connection Integration', $3, 'steam_ticket', 'verified', $4, $4)
	`, id, steamID, status, now); err != nil {
		t.Fatal(err)
	}
	return Actor{PlayerID: id, AccountStatus: status}
}

func assertEventType(t *testing.T, subscription *Subscription, eventType string) Event {
	t.Helper()
	select {
	case event := <-subscription.Events():
		if event.Type != eventType {
			t.Fatalf("event type = %s, want %s", event.Type, eventType)
		}
		return event
	default:
		t.Fatalf("event %s was not delivered", eventType)
	}
	return Event{}
}

func connectionErrorCode(err error) string {
	if err == nil {
		return ""
	}
	_, code, _, _ := errorDetails(err)
	return code
}
