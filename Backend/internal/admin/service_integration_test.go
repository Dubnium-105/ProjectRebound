package admin

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/projectrebound/matchserver/internal/auth"
	"github.com/projectrebound/matchserver/internal/config"
	"github.com/projectrebound/matchserver/internal/database"
	"github.com/projectrebound/matchserver/internal/player"
)

func TestAdminPlayerLifecycleAgainstPostgreSQL(t *testing.T) {
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

	playerRepository := player.NewRepository()
	authRepository := auth.NewRepository()
	tokenManager, _, err := auth.NewTokenManager(config.Defaults.Auth, "development")
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	authService := auth.NewService(pool, authRepository, playerRepository, tokenManager, config.Defaults.Auth, logger)
	service := NewService(pool, playerRepository, authRepository, NewRepository())

	steamID := fmt.Sprintf("%017d", uint64(time.Now().UnixNano())%100_000_000_000_000_000)
	bound, err := authService.Bind(ctx, auth.BindInput{SteamID: steamID, PersonaName: "Admin Target"}, auth.RequestMeta{
		RequestID: "req_admin_bind",
		IPAddress: "192.0.2.20",
		UserAgent: "admin-integration-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM admin_audit_logs WHERE target_id = $1", bound.Player.ID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM auth_login_audit_logs WHERE player_id = $1", bound.Player.ID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM auth_sessions WHERE player_id = $1", bound.Player.ID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM players WHERE id = $1", bound.Player.ID)
	})

	banned := player.AccountStatus("banned")
	vip := true
	patched, err := service.PatchPlayer(ctx, bound.Player.ID, PlayerPatch{
		AccountStatus: &banned,
		IsVIP:         &vip,
	}, RequestMeta{AdminID: "operator", RequestID: "req_admin_patch", IPAddress: "10.0.0.5"})
	if err != nil {
		t.Fatal(err)
	}
	if patched.Player.AccountStatus != player.AccountStatusBanned || !patched.Player.IsVIP || patched.RevokedSessions != 0 {
		t.Fatalf("patch result = %#v", patched)
	}

	principal, err := authService.AuthenticateAccess(ctx, bound.Tokens.AccessToken)
	if err != nil || principal.Player.AccountStatus != player.AccountStatusBanned || !principal.Player.IsVIP {
		t.Fatalf("updated state was not immediately visible: %#v, %v", principal, err)
	}
	listed, err := service.ListPlayers(ctx, "", "BANNED", 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range listed.Items {
		if item.ID == bound.Player.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("updated player was not returned by admin list")
	}

	revoked, err := service.RevokePlayerSessions(ctx, bound.Player.ID, RequestMeta{
		AdminID: "operator", RequestID: "req_admin_revoke", IPAddress: "10.0.0.5",
	})
	if err != nil || revoked < 1 {
		t.Fatalf("revoke sessions = %d, %v", revoked, err)
	}
	if _, err := authService.AuthenticateAccess(ctx, bound.Tokens.AccessToken); auth.ErrorCode(err) != auth.CodeSessionRevoked {
		t.Fatalf("revoked access token error = %v", err)
	}

	var auditCount int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM admin_audit_logs
		WHERE target_id = $1 AND admin_id = 'operator'
		  AND request_id IN ('req_admin_patch', 'req_admin_revoke')
		  AND ip_address = '10.0.0.5'::inet
	`, bound.Player.ID).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 2 {
		t.Fatalf("admin audit count = %d", auditCount)
	}
}
