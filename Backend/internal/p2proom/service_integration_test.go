package p2proom

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/projectrebound/matchserver/internal/config"
	"github.com/projectrebound/matchserver/internal/database"
	"github.com/projectrebound/matchserver/internal/player"
)

func TestP2PRoomLifecycleAgainstPostgreSQL(t *testing.T) {
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

	service := NewService(NewRepository(pool), config.Defaults.P2PRoom)
	fixedNow := time.Now().UTC().Truncate(time.Second)
	service.now = func() time.Time { return fixedNow }
	suffix := uint64(time.Now().UnixNano()) % 10_000_000_000_000
	actors := []Actor{
		insertTestPlayer(t, ctx, pool, fmt.Sprintf("%017d", 10_000_000_000_000_000+suffix), player.AccountStatusActive),
		insertTestPlayer(t, ctx, pool, fmt.Sprintf("%017d", 20_000_000_000_000_000+suffix), player.AccountStatusActive),
		insertTestPlayer(t, ctx, pool, fmt.Sprintf("%017d", 30_000_000_000_000_000+suffix), player.AccountStatusActive),
		insertTestPlayer(t, ctx, pool, fmt.Sprintf("%017d", 40_000_000_000_000_000+suffix), player.AccountStatusBanned),
	}
	playerIDs := []string{actors[0].PlayerID, actors[1].PlayerID, actors[2].PlayerID, actors[3].PlayerID}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM p2p_room_members WHERE player_id = ANY($1)", playerIDs)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM p2p_rooms WHERE host_player_id = ANY($1)", playerIDs)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM players WHERE id = ANY($1)", playerIDs)
	})

	created, err := service.Create(ctx, actors[0], CreateInput{
		DisplayName: "Integration Room", Region: "hk", Mode: "coop", Version: "1.0.0", MaxPlayers: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(created.Room.HostTokenHash, hashHostToken(created.HostToken)) || bytes.Contains(created.Room.HostTokenHash, []byte(created.HostToken)) {
		t.Fatal("host token was not represented by a one-way SHA-256 hash")
	}
	var storedHash []byte
	if err := pool.QueryRow(ctx, "SELECT host_token_hash FROM p2p_rooms WHERE id = $1", created.Room.ID).Scan(&storedHash); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(storedHash, hashHostToken(created.HostToken)) {
		t.Fatal("stored host token hash does not match")
	}
	hasSlots := true
	listed, err := service.List(ctx, ListFilter{
		Region: "hk", Mode: "coop", Version: "1.0.0", State: StateLobby, HasSlots: &hasSlots, Limit: 10,
	})
	if err != nil || !containsRoom(listed.Items, created.Room.ID) {
		t.Fatalf("filtered room list = %#v, %v", listed, err)
	}

	if _, err := service.Join(ctx, actors[1], created.Room.ID, "2.0.0"); roomErrorCode(err) != "VERSION_MISMATCH" {
		t.Fatalf("version mismatch error = %v", err)
	}
	joined, err := service.Join(ctx, actors[1], created.Room.ID, "1.0.0")
	if err != nil || joined.PlayerCount != 2 {
		t.Fatalf("join = %#v, %v", joined, err)
	}
	repeated, err := service.Join(ctx, actors[1], created.Room.ID, "1.0.0")
	if err != nil || repeated.PlayerCount != 2 {
		t.Fatalf("idempotent join = %#v, %v", repeated, err)
	}
	if _, err := service.Join(ctx, actors[2], created.Room.ID, "1.0.0"); roomErrorCode(err) != "ROOM_FULL" {
		t.Fatalf("full room error = %v", err)
	}
	if _, err := service.Join(ctx, actors[3], created.Room.ID, "1.0.0"); roomErrorCode(err) != "ACCOUNT_NOT_ACTIVE" {
		t.Fatalf("banned join error = %v", err)
	}
	if _, err := service.Create(ctx, actors[3], CreateInput{
		DisplayName: "Forbidden", Region: "hk", Mode: "coop", Version: "1.0.0", MaxPlayers: 2,
	}); roomErrorCode(err) != "ACCOUNT_NOT_ACTIVE" {
		t.Fatalf("banned create error = %v", err)
	}
	hasSlots = false
	fullRooms, err := service.List(ctx, ListFilter{HasSlots: &hasSlots, State: StateLobby, Limit: 100})
	if err != nil || !containsRoom(fullRooms.Items, created.Room.ID) {
		t.Fatalf("full room list = %#v, %v", fullRooms, err)
	}
	if _, err := service.Delete(ctx, actors[1], created.Room.ID, created.HostToken); roomErrorCode(err) != "HOST_UNAUTHORIZED" {
		t.Fatalf("non-host close error = %v", err)
	}

	left, err := service.Leave(ctx, actors[1], created.Room.ID)
	if err != nil || left.PlayerCount != 1 {
		t.Fatalf("leave = %#v, %v", left, err)
	}
	repeatedLeave, err := service.Leave(ctx, actors[1], created.Room.ID)
	if err != nil || repeatedLeave.PlayerCount != 1 {
		t.Fatalf("idempotent leave = %#v, %v", repeatedLeave, err)
	}
	started, err := service.Start(ctx, actors[0], created.Room.ID, created.HostToken)
	if err != nil || started.State != StateConnecting {
		t.Fatalf("start = %#v, %v", started, err)
	}
	if _, err := service.Heartbeat(ctx, actors[0], created.Room.ID, "p2h_invalid"); roomErrorCode(err) != "HOST_UNAUTHORIZED" {
		t.Fatalf("invalid host token error = %v", err)
	}

	expiring, err := service.Create(ctx, actors[0], CreateInput{
		DisplayName: "Expiring Room", Region: "hk", Mode: "coop", Version: "1.0.0", MaxPlayers: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE p2p_rooms SET last_heartbeat_at = $2 WHERE id = $1", expiring.Room.ID, fixedNow.Add(-46*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SweepStale(ctx); err != nil {
		t.Fatal(err)
	}
	stale, err := service.Get(ctx, expiring.Room.ID)
	if err != nil || stale.State != StateStale {
		t.Fatalf("stale sweep = %#v, %v", stale, err)
	}
	recovered, err := service.Heartbeat(ctx, actors[0], expiring.Room.ID, expiring.HostToken)
	if err != nil || recovered.State != StateLobby {
		t.Fatalf("stale heartbeat recovery = %#v, %v", recovered, err)
	}
	if _, err := pool.Exec(ctx, "UPDATE p2p_rooms SET last_heartbeat_at = $2 WHERE id = $1", expiring.Room.ID, fixedNow.Add(-91*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SweepStale(ctx); err != nil {
		t.Fatal(err)
	}
	closed, err := service.Get(ctx, expiring.Room.ID)
	if err != nil || closed.State != StateClosed || closed.ClosedAt == nil {
		t.Fatalf("closed sweep = %#v, %v", closed, err)
	}
}

func insertTestPlayer(t *testing.T, ctx context.Context, pool *pgxpool.Pool, steamID string, status player.AccountStatus) Actor {
	t.Helper()
	id := newID("player_")
	now := time.Now().UTC()
	if _, err := pool.Exec(ctx, `
		INSERT INTO players (id, steam_id, persona_name, account_status, created_at, updated_at)
		VALUES ($1, $2, 'P2P Integration', $3, $4, $4)
	`, id, steamID, status, now); err != nil {
		t.Fatal(err)
	}
	return Actor{PlayerID: id, AccountStatus: status}
}

func roomErrorCode(err error) string {
	if err == nil {
		return ""
	}
	_, code, _, _ := errorDetails(err)
	return code
}

func containsRoom(rooms []Room, roomID string) bool {
	for _, room := range rooms {
		if room.ID == roomID {
			return true
		}
	}
	return false
}
