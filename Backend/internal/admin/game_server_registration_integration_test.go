package admin

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/config"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/database"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/gameserver"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/gameserverregistration"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAdministratorIssuesAndRevokesInstanceBoundGameServerToken(t *testing.T) {
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
	now := time.Now().UTC().Truncate(time.Second)
	suffix := time.Now().UnixNano()
	adminID := fmt.Sprintf("adm_registration_%d", suffix)
	instanceID := fmt.Sprintf("registration-instance-%d", suffix)
	if _, err := pool.Exec(ctx, `
		INSERT INTO admin_users (
			id, username, display_name, password_hash, status, mfa_required,
			created_at, updated_at
		) VALUES ($1, $2, 'Registration Integration', 'test-only', 'ACTIVE', TRUE, $3, $3)
	`, adminID, adminID, now); err != nil {
		t.Fatal(err)
	}
	var serverID string
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM admin_audit_logs WHERE admin_id = $1", adminID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM game_server_registration_tokens WHERE instance_id = $1", instanceID)
		if serverID != "" {
			_, _ = pool.Exec(cleanupCtx, "DELETE FROM game_servers WHERE id = $1", serverID)
		}
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM admin_users WHERE id = $1", adminID)
	})

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	registrationRepository := gameserverregistration.NewRepository()
	adminService := NewOnlineService(pool, NewRepository(), nil, registrationRepository, logger)
	adminService.now = func() time.Time { return now }
	gameServerService := gameserver.NewService(
		gameserver.NewRepository(pool), registrationRepository, config.Defaults.GameServer,
	)
	meta := RequestMeta{
		AdminID: adminID, RequestID: "req_registration_integration",
		IPAddress: "192.0.2.50", UserAgent: "registration-integration-test",
	}

	issued, err := adminService.CreateGameServerRegistration(ctx, GameServerRegistrationInput{
		InstanceID: instanceID, ExpiresInHours: 24, Reason: "Provision integration dedicated server",
	}, meta)
	if err != nil {
		t.Fatal(err)
	}
	if !gameserverregistration.HasValidShape(issued.Plaintext) || issued.Credential.InstanceID != instanceID {
		t.Fatalf("issued registration = %#v", issued.Credential)
	}
	var storedHash []byte
	if err := pool.QueryRow(ctx, `
		SELECT token_hash FROM game_server_registration_tokens WHERE id = $1
	`, issued.Credential.ID).Scan(&storedHash); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(storedHash, gameserverregistration.HashToken(issued.Plaintext)) || bytes.Contains(storedHash, []byte(issued.Plaintext)) {
		t.Fatal("registration credential was not stored as the expected one-way hash")
	}

	registered, err := gameServerService.Register(ctx, gameserver.RegistrationInput{
		InstanceID: instanceID, DisplayName: "Registration Integration", Region: "asia-hk",
		Mode: "casual", Version: "1.0.0", PublicHost: "8.8.4.4", PublicPort: 7777,
		MaxPlayers: 16,
	}, issued.Plaintext)
	if err != nil {
		t.Fatal(err)
	}
	serverID = registered.Server.ID
	var consumedServerID string
	if err := pool.QueryRow(ctx, `
		SELECT consumed_server_id FROM game_server_registration_tokens WHERE id = $1 AND consumed_at IS NOT NULL
	`, issued.Credential.ID).Scan(&consumedServerID); err != nil {
		t.Fatal(err)
	}
	if consumedServerID != serverID {
		t.Fatalf("consumed server ID = %q, want %q", consumedServerID, serverID)
	}

	replaced, err := adminService.CreateGameServerRegistration(ctx, GameServerRegistrationInput{
		InstanceID: instanceID, ExpiresInHours: 12, Reason: "Prepare first replacement credential",
	}, meta)
	if err != nil {
		t.Fatal(err)
	}
	latest, err := adminService.CreateGameServerRegistration(ctx, GameServerRegistrationInput{
		InstanceID: instanceID, ExpiresInHours: 12, Reason: "Replace unused credential",
	}, meta)
	if err != nil {
		t.Fatal(err)
	}
	var replacedRevoked bool
	if err := pool.QueryRow(ctx, `
		SELECT revoked_at IS NOT NULL FROM game_server_registration_tokens WHERE id = $1
	`, replaced.Credential.ID).Scan(&replacedRevoked); err != nil {
		t.Fatal(err)
	}
	if !replacedRevoked {
		t.Fatal("previous unused registration token was not revoked")
	}

	if _, err := adminService.ChangeGameServerState(
		ctx, serverID, "disable", "Disable integration dedicated server", meta,
	); err != nil {
		t.Fatal(err)
	}
	var latestRevoked bool
	if err := pool.QueryRow(ctx, `
		SELECT revoked_at IS NOT NULL FROM game_server_registration_tokens WHERE id = $1
	`, latest.Credential.ID).Scan(&latestRevoked); err != nil {
		t.Fatal(err)
	}
	if !latestRevoked {
		t.Fatal("disabling a game server did not revoke its pending registration token")
	}
	var leakedAuditCount int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM admin_audit_logs
		WHERE admin_id = $1 AND (old_value::text LIKE '%' || $2 || '%' OR new_value::text LIKE '%' || $2 || '%')
	`, adminID, issued.Plaintext).Scan(&leakedAuditCount); err != nil {
		t.Fatal(err)
	}
	if leakedAuditCount != 0 {
		t.Fatal("plaintext registration token leaked into the administrator audit log")
	}
}
