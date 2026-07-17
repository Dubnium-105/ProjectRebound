package connection

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/projectrebound/matchserver/internal/config"
	"github.com/projectrebound/matchserver/internal/database"
	"github.com/projectrebound/matchserver/internal/p2proom"
	"github.com/projectrebound/matchserver/internal/player"
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
	defer pool.Close()
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
	service := NewService(NewRepository(pool), roomService, hub, config.Defaults.Connection)
	roomService.SetConnectionCreator(service)
	room, err := roomService.Create(ctx, p2proom.Actor{PlayerID: host.PlayerID, AccountStatus: host.AccountStatus}, p2proom.CreateInput{
		DisplayName: "Connection Integration", Region: "hk", Mode: "coop", Version: "1.0.0", MaxPlayers: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := roomService.Join(ctx, p2proom.Actor{PlayerID: peer.PlayerID, AccountStatus: peer.AccountStatus}, room.Room.ID, "1.0.0"); err != nil {
		t.Fatal(err)
	}
	assertEventType(t, hostEvents, "connection.created")
	assertEventType(t, peerEvents, "connection.created")

	connection, err := service.Create(ctx, peer, CreateInput{RoomID: room.Room.ID})
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := service.Create(ctx, peer, CreateInput{RoomID: room.Room.ID})
	if err != nil || repeated.ID != connection.ID {
		t.Fatalf("idempotent create = %#v, %v", repeated, err)
	}
	if _, err := service.Create(ctx, banned, CreateInput{RoomID: room.Room.ID}); connectionErrorCode(err) != "ACCOUNT_NOT_ACTIVE" {
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
	if _, err := service.AddCandidate(ctx, peer, CandidateInput{
		ConnectionID: connection.ID, Foundation: "peer-lan", CandidateType: CandidateLAN,
		Protocol: "UDP", Address: "192.168.10.3", Port: 7777, Priority: 200,
	}); err != nil {
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
	runningRoom, err := roomService.Get(ctx, room.Room.ID)
	if err != nil || runningRoom.State != p2proom.StateRunning {
		t.Fatalf("connected room state = %#v, %v", runningRoom, err)
	}
	closed, err := service.Close(ctx, host, connection.ID)
	if err != nil || closed.State != StateClosed {
		t.Fatalf("close = %#v, %v", closed, err)
	}

	relayFallback, err := service.Create(ctx, peer, CreateInput{RoomID: room.Room.ID})
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
	roomBound, err := service.Create(ctx, peer, CreateInput{RoomID: room.Room.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := roomService.Delete(
		ctx,
		p2proom.Actor{PlayerID: host.PlayerID, AccountStatus: host.AccountStatus},
		room.Room.ID,
		room.HostToken,
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
		INSERT INTO players (id, steam_id, persona_name, account_status, created_at, updated_at)
		VALUES ($1, $2, 'Connection Integration', $3, $4, $4)
	`, id, steamID, status, now); err != nil {
		t.Fatal(err)
	}
	return Actor{PlayerID: id, AccountStatus: status}
}

func assertEventType(t *testing.T, subscription *Subscription, eventType string) {
	t.Helper()
	select {
	case event := <-subscription.Events():
		if event.Type != eventType {
			t.Fatalf("event type = %s, want %s", event.Type, eventType)
		}
	default:
		t.Fatalf("event %s was not delivered", eventType)
	}
}

func connectionErrorCode(err error) string {
	if err == nil {
		return ""
	}
	_, code, _, _ := errorDetails(err)
	return code
}
