package auth

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/projectrebound/matchserver/internal/config"
	"github.com/projectrebound/matchserver/internal/database"
	"github.com/projectrebound/matchserver/internal/player"
)

var steamSequence atomic.Uint64

func TestAuthenticationLifecycleAgainstPostgreSQL(t *testing.T) {
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

	authConfig := config.Defaults.Auth
	tokenManager, _, err := NewTokenManager(authConfig, "development")
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(
		pool,
		NewRepository(),
		player.NewRepository(),
		tokenManager,
		authConfig,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	createdSteamIDs := make([]string, 0)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		for _, steamID := range createdSteamIDs {
			_, _ = pool.Exec(cleanupCtx, "DELETE FROM auth_risk_events WHERE steam_id = $1 OR player_id IN (SELECT id FROM players WHERE steam_id = $1)", steamID)
			_, _ = pool.Exec(cleanupCtx, "DELETE FROM auth_login_events WHERE steam_id = $1 OR player_id IN (SELECT id FROM players WHERE steam_id = $1)", steamID)
			_, _ = pool.Exec(cleanupCtx, "DELETE FROM auth_login_audit_logs WHERE steam_id = $1 OR player_id IN (SELECT id FROM players WHERE steam_id = $1)", steamID)
			_, _ = pool.Exec(cleanupCtx, "DELETE FROM auth_sessions WHERE player_id IN (SELECT id FROM players WHERE steam_id = $1)", steamID)
			_, _ = pool.Exec(cleanupCtx, "DELETE FROM players WHERE steam_id = $1", steamID)
		}
	})
	meta := RequestMeta{RequestID: "req_integration", IPAddress: "192.0.2.10", UserAgent: "auth-integration-test"}

	t.Run("new and existing bind", func(t *testing.T) {
		steamID := nextSteamID()
		createdSteamIDs = append(createdSteamIDs, steamID)
		first, err := service.Bind(ctx, BindInput{SteamID: steamID, PersonaName: " First "}, meta)
		if err != nil {
			t.Fatal(err)
		}
		if !first.IsNewPlayer || first.Player.AccountStatus != player.AccountStatusActive || first.Player.IsVIP {
			t.Fatalf("first bind = %#v", first)
		}
		longName := "Updated"
		for i := 0; i < 70; i++ {
			longName += "界"
		}
		second, err := service.Bind(ctx, BindInput{SteamID: steamID, PersonaName: longName}, meta)
		if err != nil {
			t.Fatal(err)
		}
		if second.IsNewPlayer || second.Player.ID != first.Player.ID || utf8.RuneCountInString(second.Player.PersonaName) != 64 {
			t.Fatalf("second bind = %#v", second)
		}
		principal, err := service.AuthenticateAccess(ctx, second.Tokens.AccessToken)
		if err != nil || principal.Player.ID != first.Player.ID {
			t.Fatalf("AuthenticateAccess() = %#v, %v", principal, err)
		}
		var storedHash []byte
		if err := pool.QueryRow(ctx, "SELECT refresh_token_hash FROM auth_sessions WHERE id = $1", second.Tokens.SessionID).Scan(&storedHash); err != nil {
			t.Fatal(err)
		}
		if len(storedHash) != 32 || string(storedHash) == second.Tokens.RefreshToken {
			t.Fatal("refresh token was not stored as a SHA-256 hash")
		}
	})

	t.Run("concurrent bind is idempotent", func(t *testing.T) {
		steamID := nextSteamID()
		createdSteamIDs = append(createdSteamIDs, steamID)
		const workers = 8
		results := make(chan BindResult, workers)
		errorsCh := make(chan error, workers)
		var wait sync.WaitGroup
		for index := 0; index < workers; index++ {
			wait.Add(1)
			go func(index int) {
				defer wait.Done()
				result, err := service.Bind(ctx, BindInput{SteamID: steamID, PersonaName: fmt.Sprintf("Concurrent-%d", index)}, meta)
				if err != nil {
					errorsCh <- err
					return
				}
				results <- result
			}(index)
		}
		wait.Wait()
		close(results)
		close(errorsCh)
		for err := range errorsCh {
			t.Errorf("concurrent bind: %v", err)
		}
		playerID := ""
		newCount := 0
		for result := range results {
			if playerID == "" {
				playerID = result.Player.ID
			}
			if result.Player.ID != playerID {
				t.Errorf("duplicate player IDs: %s and %s", playerID, result.Player.ID)
			}
			if result.IsNewPlayer {
				newCount++
			}
		}
		if newCount != 1 {
			t.Errorf("is_new_player count = %d", newCount)
		}
	})

	t.Run("banned player may bind", func(t *testing.T) {
		steamID := nextSteamID()
		createdSteamIDs = append(createdSteamIDs, steamID)
		initial, err := service.Bind(ctx, BindInput{SteamID: steamID, PersonaName: "Banned"}, meta)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, "UPDATE players SET account_status = 'BANNED' WHERE id = $1", initial.Player.ID); err != nil {
			t.Fatal(err)
		}
		result, err := service.Bind(ctx, BindInput{SteamID: steamID, PersonaName: "Still Banned"}, meta)
		if err != nil || result.Player.AccountStatus != player.AccountStatusBanned {
			t.Fatalf("banned bind = %#v, %v", result, err)
		}
	})

	t.Run("rotation reuse and logout revoke sessions", func(t *testing.T) {
		steamID := nextSteamID()
		createdSteamIDs = append(createdSteamIDs, steamID)
		bound, err := service.Bind(ctx, BindInput{
			SteamID: steamID, PersonaName: "Rotation", DeviceID: "integration-device-1234",
		}, meta)
		if err != nil {
			t.Fatal(err)
		}
		rotated, err := service.Refresh(ctx, bound.Tokens.RefreshToken, meta)
		if err != nil {
			t.Fatal(err)
		}
		if rotated.Tokens.RefreshToken == bound.Tokens.RefreshToken || rotated.Tokens.SessionID == bound.Tokens.SessionID {
			t.Fatal("refresh token rotation did not replace credentials")
		}
		var rotatedDeviceHash []byte
		if err := pool.QueryRow(ctx, "SELECT device_id_hash FROM auth_sessions WHERE id = $1", rotated.Tokens.SessionID).Scan(&rotatedDeviceHash); err != nil {
			t.Fatal(err)
		}
		if string(rotatedDeviceHash) != string(HashDeviceID("integration-device-1234")) {
			t.Fatal("refresh without a device header did not inherit the session device hash")
		}
		if _, err := service.AuthenticateAccess(ctx, bound.Tokens.AccessToken); ErrorCode(err) != CodeSessionRevoked {
			t.Fatalf("old access token error = %v", err)
		}
		if _, err := service.Refresh(ctx, bound.Tokens.RefreshToken, meta); ErrorCode(err) != CodeRefreshTokenReused {
			t.Fatalf("refresh reuse error = %v", err)
		}
		var reuseRiskEvents, reuseLoginFailures int
		if err := pool.QueryRow(ctx, `
			SELECT COUNT(*) FROM auth_risk_events
			WHERE player_id = $1 AND event_type = 'REFRESH_TOKEN_REUSE'
		`, bound.Player.ID).Scan(&reuseRiskEvents); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `
			SELECT COUNT(*) FROM auth_login_events
			WHERE player_id = $1 AND result = 'FAILURE' AND failure_code = $2
		`, bound.Player.ID, CodeRefreshTokenReused).Scan(&reuseLoginFailures); err != nil {
			t.Fatal(err)
		}
		if reuseRiskEvents != 1 || reuseLoginFailures != 1 {
			t.Fatalf("reuse risk events=%d login failures=%d", reuseRiskEvents, reuseLoginFailures)
		}
		var leakedTokens int
		if err := pool.QueryRow(ctx, `
			SELECT COUNT(*) FROM auth_risk_events
			WHERE player_id = $1 AND (details::text LIKE '%' || $2 || '%' OR details::text LIKE '%' || $3 || '%')
		`, bound.Player.ID, bound.Tokens.RefreshToken, rotated.Tokens.RefreshToken).Scan(&leakedTokens); err != nil {
			t.Fatal(err)
		}
		if leakedTokens != 0 {
			t.Fatal("refresh token plaintext leaked into risk events")
		}
		if _, err := service.AuthenticateAccess(ctx, rotated.Tokens.AccessToken); ErrorCode(err) != CodeSessionRevoked {
			t.Fatalf("rotated family token error = %v", err)
		}

		logoutBound, err := service.Bind(ctx, BindInput{SteamID: steamID, PersonaName: "Logout"}, meta)
		if err != nil {
			t.Fatal(err)
		}
		if err := service.Logout(ctx, logoutBound.Tokens.SessionID); err != nil {
			t.Fatal(err)
		}
		if _, err := service.AuthenticateAccess(ctx, logoutBound.Tokens.AccessToken); ErrorCode(err) != CodeSessionRevoked {
			t.Fatalf("logged-out access token error = %v", err)
		}
	})
}

func nextSteamID() string {
	sequence := steamSequence.Add(1)
	return fmt.Sprintf("%017d", (uint64(time.Now().UnixNano())+sequence)%100_000_000_000_000_000)
}
