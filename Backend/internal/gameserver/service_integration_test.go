package gameserver

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/projectrebound/matchserver/internal/config"
	"github.com/projectrebound/matchserver/internal/database"
)

func TestGameServerRegistryAgainstPostgreSQL(t *testing.T) {
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
		t.Fatal(err)
	}
	repository := NewRepository(pool)
	service := NewService(repository, config.Defaults.GameServer)
	fixedNow := time.Now().UTC().Truncate(time.Second)
	service.now = func() time.Time { return fixedNow }

	suffix := time.Now().UnixNano()
	firstInstance := fmt.Sprintf("integration-primary-%d", suffix)
	secondInstance := fmt.Sprintf("integration-secondary-%d", suffix)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM game_servers WHERE instance_id = ANY($1)", []string{firstInstance, secondInstance})
	})
	input := RegistrationInput{
		InstanceID: firstInstance, DisplayName: "Integration Server", Region: "us-west",
		Mode: "tdm", Version: "1.0.0", PublicHost: "8.8.8.8", PublicPort: 7777, MaxPlayers: 12,
	}
	first, err := service.Register(ctx, input, "integration-pool")
	if err != nil {
		t.Fatal(err)
	}
	secondRegistration, err := service.Register(ctx, input, "integration-pool")
	if err != nil {
		t.Fatal(err)
	}
	if secondRegistration.Server.ID != first.Server.ID || secondRegistration.ServerToken == first.ServerToken {
		t.Fatalf("idempotent registration did not preserve ID and rotate token: %#v %#v", first, secondRegistration)
	}
	if _, err := service.Heartbeat(ctx, first.Server.ID, first.ServerToken, HeartbeatInput{State: StateReady}); errorStatus(err) != 401 {
		t.Fatalf("old rotated token heartbeat error = %v", err)
	}
	ready, err := service.Heartbeat(ctx, first.Server.ID, secondRegistration.ServerToken, HeartbeatInput{State: StateReady, PlayerCount: 2})
	if err != nil || ready.State != StateReady || ready.PlayerCount != 2 {
		t.Fatalf("heartbeat = %#v, %v", ready, err)
	}

	secondServer, err := service.Register(ctx, RegistrationInput{
		InstanceID: secondInstance, DisplayName: "Other", Region: "eu-west", Mode: "tdm",
		Version: "1.0.0", PublicHost: "1.1.1.1", PublicPort: 7778, MaxPlayers: 8,
	}, "integration-pool")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Heartbeat(ctx, secondServer.Server.ID, secondRegistration.ServerToken, HeartbeatInput{State: StateReady}); errorStatus(err) != 401 {
		t.Fatalf("cross-server token was accepted: %v", err)
	}

	if _, err := pool.Exec(ctx, "UPDATE game_servers SET last_heartbeat_at = $2, state = 'READY' WHERE id = $1", first.Server.ID, fixedNow.Add(-46*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SweepStale(ctx); err != nil {
		t.Fatal(err)
	}
	unhealthy, err := service.Get(ctx, first.Server.ID)
	if err != nil || unhealthy.State != StateUnhealthy {
		t.Fatalf("45-second sweep = %#v, %v", unhealthy, err)
	}
	if _, err := pool.Exec(ctx, "UPDATE game_servers SET last_heartbeat_at = $2 WHERE id = $1", first.Server.ID, fixedNow.Add(-91*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SweepStale(ctx); err != nil {
		t.Fatal(err)
	}
	offline, err := service.Get(ctx, first.Server.ID)
	if err != nil || offline.State != StateOffline {
		t.Fatalf("90-second sweep = %#v, %v", offline, err)
	}

	if err := service.Deregister(ctx, secondServer.Server.ID, secondServer.ServerToken); err != nil {
		t.Fatal(err)
	}
	deregistered, err := service.Get(ctx, secondServer.Server.ID)
	if err != nil || deregistered.State != StateOffline || deregistered.TokenRevokedAt == nil {
		t.Fatalf("deregistered server = %#v, %v", deregistered, err)
	}
}

func errorStatus(err error) int {
	if err == nil {
		return 0
	}
	status, _, _, _ := errorDetails(err)
	return status
}
