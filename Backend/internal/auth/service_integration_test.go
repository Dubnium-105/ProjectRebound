package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/config"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/database"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/player"
	"github.com/jackc/pgx/v5/pgxpool"
)

var steamSequence atomic.Uint64

type integrationTicketVerifier struct {
	result VerifiedTicket
	err    error
}

func (v *integrationTicketVerifier) Verify(context.Context, string) (VerifiedTicket, error) {
	return v.result, v.err
}

type integrationIntegrityManager struct {
	mu          sync.Mutex
	ticket      []byte
	sessionID   string
	rotatedFrom string
	removed     string
}

func (m *integrationIntegrityManager) RegisterSession(
	sessionID string,
	ticket []byte,
	_ time.Time,
) (IntegrityChallenge, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessionID = sessionID
	m.ticket = append([]byte(nil), ticket...)
	return IntegrityChallenge{Nonce: "integration-nonce"}, nil
}

func (m *integrationIntegrityManager) RotateSession(oldSessionID, newSessionID string, _ time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rotatedFrom = oldSessionID
	m.sessionID = newSessionID
}

func (m *integrationIntegrityManager) RemoveSession(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.removed = sessionID
}

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
	deviceFingerprinter, _, err := NewDeviceFingerprinter(authConfig, "development")
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(
		pool,
		NewRepository(),
		player.NewRepository(),
		tokenManager,
		deviceFingerprinter,
		authConfig,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	integrityManager := &integrationIntegrityManager{}
	service.SetIntegritySessionManager(integrityManager)
	createdSteamIDs := make([]string, 0)
	createdDeviceFingerprintIDs := make([]string, 0)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		for _, steamID := range createdSteamIDs {
			_, _ = pool.Exec(cleanupCtx, "DELETE FROM auth_steam_ticket_verifications WHERE steam_id = $1 OR player_id IN (SELECT id FROM players WHERE steam_id = $1)", steamID)
			_, _ = pool.Exec(cleanupCtx, "DELETE FROM auth_risk_events WHERE steam_id = $1 OR player_id IN (SELECT id FROM players WHERE steam_id = $1)", steamID)
			_, _ = pool.Exec(cleanupCtx, "DELETE FROM auth_login_events WHERE steam_id = $1 OR player_id IN (SELECT id FROM players WHERE steam_id = $1)", steamID)
			_, _ = pool.Exec(cleanupCtx, "DELETE FROM auth_login_audit_logs WHERE steam_id = $1 OR player_id IN (SELECT id FROM players WHERE steam_id = $1)", steamID)
			_, _ = pool.Exec(cleanupCtx, "DELETE FROM auth_sessions WHERE player_id IN (SELECT id FROM players WHERE steam_id = $1)", steamID)
			_, _ = pool.Exec(cleanupCtx, "DELETE FROM players WHERE steam_id = $1", steamID)
		}
		for _, fingerprintID := range createdDeviceFingerprintIDs {
			_, _ = pool.Exec(cleanupCtx, "DELETE FROM auth_device_fingerprints WHERE id = $1", fingerprintID)
		}
	})
	meta := RequestMeta{RequestID: "req_integration", IPAddress: "192.0.2.10", UserAgent: "auth-integration-test"}

	t.Run("encrypted ticket establishes and preserves a verified session", func(t *testing.T) {
		steamID := nextSteamID()
		createdSteamIDs = append(createdSteamIDs, steamID)
		now := service.now().UTC()
		verifier := &integrationTicketVerifier{result: VerifiedTicket{
			Valid: true, SteamID: steamID, AppID: authConfig.SteamAppID,
			IssueTime: now.Unix(),
		}}
		service.SetTicketVerifier(verifier)
		const ticketHex = "0102030405060708090a0b0c0d0e0f"

		bound, err := service.Bind(ctx, BindInput{
			SteamID: steamID, PersonaName: "Verified Player",
			EncryptedTicket: ticketHex,
		}, meta)
		if err != nil {
			t.Fatal(err)
		}
		if bound.Player.SteamID != steamID || bound.Player.AuthProvider != player.AuthProviderSteamTicket ||
			bound.Player.AuthLevel != player.AuthLevelVerified ||
			bound.AuthLevel != player.AuthLevelVerified || !bound.SteamVerified {
			t.Fatalf("verified bind = %#v", bound)
		}
		if bound.IntegrityChallenge.Nonce != "integration-nonce" ||
			fmt.Sprintf("%x", integrityManager.ticket) != ticketHex ||
			integrityManager.sessionID != bound.Tokens.SessionID {
			t.Fatalf("integrity registration = challenge=%+v manager=%+v", bound.IntegrityChallenge, integrityManager)
		}
		claims, err := tokenManager.Verify(bound.Tokens.AccessToken)
		if err != nil {
			t.Fatal(err)
		}
		if claims.UserID != bound.Player.ID || claims.Provider != player.AuthProviderSteamTicket ||
			claims.AuthLevel != player.AuthLevelVerified || !claims.SteamVerified {
			t.Fatalf("verified access claims = %#v", claims)
		}

		var verificationCount int
		var sessionLevel string
		var sessionVerified bool
		if err := pool.QueryRow(ctx, `
			SELECT COUNT(*) FROM auth_steam_ticket_verifications
			WHERE player_id = $1 AND ticket_hash = $2
		`, bound.Player.ID, mustTicketHashForTest(t, ticketHex)).Scan(&verificationCount); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `
			SELECT auth_level, steam_verified FROM auth_sessions WHERE id = $1
		`, bound.Tokens.SessionID).Scan(&sessionLevel, &sessionVerified); err != nil {
			t.Fatal(err)
		}
		if verificationCount != 1 || sessionLevel != player.AuthLevelVerified || !sessionVerified {
			t.Fatalf("verification/session state = %d, %q, %v", verificationCount, sessionLevel, sessionVerified)
		}

		principal, err := service.AuthenticateAccess(ctx, bound.Tokens.AccessToken)
		if err != nil {
			t.Fatal(err)
		}
		if err := service.PromoteIntegrityTrusted(ctx, principal, meta); err != nil {
			t.Fatal(err)
		}
		principal, err = service.AuthenticateAccess(ctx, bound.Tokens.AccessToken)
		if err != nil || principal.AuthLevel != player.AuthLevelTrusted {
			t.Fatalf("trusted principal = %#v, %v", principal, err)
		}

		refreshed, err := service.Refresh(ctx, bound.Tokens.RefreshToken, meta)
		if err != nil {
			t.Fatal(err)
		}
		refreshedClaims, err := tokenManager.Verify(refreshed.Tokens.AccessToken)
		if err != nil || !refreshedClaims.SteamVerified ||
			refreshedClaims.AuthLevel != player.AuthLevelTrusted {
			t.Fatalf("refreshed claims = %#v, %v", refreshedClaims, err)
		}
		if integrityManager.rotatedFrom != bound.Tokens.SessionID ||
			integrityManager.sessionID != refreshed.Tokens.SessionID {
			t.Fatalf("integrity rotation = %+v", integrityManager)
		}

		if _, err := service.Bind(ctx, BindInput{
			SteamID: steamID, PersonaName: "Replay", EncryptedTicket: ticketHex,
		}, meta); ErrorCode(err) != CodeSteamTicketReplay {
			t.Fatalf("ticket replay error = %v (%s)", err, ErrorCode(err))
		}
	})

	t.Run("ticket validation rejects invalid identity attributes", func(t *testing.T) {
		now := service.now().UTC()
		tests := []struct {
			name   string
			result VerifiedTicket
			err    error
			code   string
		}{
			{
				name: "decrypt failure", err: errors.New("bad ticket"),
				code: CodeInvalidSteamTicket,
			},
			{
				name:   "SteamID mismatch",
				result: VerifiedTicket{Valid: true, SteamID: nextSteamID(), AppID: authConfig.SteamAppID, IssueTime: now.Unix()},
				code:   CodeSteamIDMismatch,
			},
			{
				name:   "wrong app",
				result: VerifiedTicket{Valid: true, AppID: authConfig.SteamAppID + 1, IssueTime: now.Unix()},
				code:   CodeSteamTicketAppID,
			},
			{
				name:   "expired",
				result: VerifiedTicket{Valid: true, AppID: authConfig.SteamAppID, IssueTime: now.Add(-10 * time.Minute).Unix()},
				code:   CodeSteamTicketExpired,
			},
		}
		for index, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				requestSteamID := nextSteamID()
				createdSteamIDs = append(createdSteamIDs, requestSteamID)
				if test.result.SteamID == "" {
					test.result.SteamID = requestSteamID
				}
				service.SetTicketVerifier(&integrationTicketVerifier{result: test.result, err: test.err})
				_, err := service.Bind(ctx, BindInput{
					SteamID: requestSteamID, PersonaName: "Rejected Ticket",
					EncryptedTicket: fmt.Sprintf("%032x", index+100),
				}, meta)
				if ErrorCode(err) != test.code {
					t.Fatalf("error = %v (%s), want %s", err, ErrorCode(err), test.code)
				}
			})
		}
	})

	t.Run("structured device factors are normalized and linked", func(t *testing.T) {
		steamID := nextSteamID()
		createdSteamIDs = append(createdSteamIDs, steamID)
		const submitted = "CP:F846FFB743ECA479|UU:B09BC26D38BB76D6|DS:A867D4D49D01C90A"
		const canonical = "v1|uu:b09bc26d38bb76d6|ds:a867d4d49d01c90a|cp:f846ffb743eca479"

		bound, err := service.Bind(ctx, BindInput{
			SteamID: steamID, PersonaName: "Structured Device", DeviceID: submitted,
		}, meta)
		if err != nil {
			t.Fatal(err)
		}

		var fingerprintID, digestKeyID, storedDeviceSuffix string
		var formatVersion, factorMask int16
		var compositeDigest, smbiosDigest, diskDigest, cpuDigest, storedDeviceHash []byte
		if err := pool.QueryRow(ctx, `
			SELECT f.id, f.format_version, f.digest_key_id, f.composite_digest,
			       f.smbios_uuid_digest, f.disk_serial_digest, f.cpu_id_digest,
			       f.factor_mask, s.device_id_hash, s.device_id_suffix
			FROM auth_sessions s
			JOIN auth_device_fingerprints f ON f.id = s.device_fingerprint_id
			WHERE s.id = $1
		`, bound.Tokens.SessionID).Scan(
			&fingerprintID, &formatVersion, &digestKeyID, &compositeDigest,
			&smbiosDigest, &diskDigest, &cpuDigest, &factorMask, &storedDeviceHash, &storedDeviceSuffix,
		); err != nil {
			t.Fatal(err)
		}
		createdDeviceFingerprintIDs = append(createdDeviceFingerprintIDs, fingerprintID)
		if formatVersion != 1 || digestKeyID != authConfig.DeviceFingerprintKeyID || factorMask != 7 {
			t.Fatalf("fingerprint metadata = version %d key %q mask %d", formatVersion, digestKeyID, factorMask)
		}
		for name, digest := range map[string][]byte{
			"composite": compositeDigest,
			"smbios":    smbiosDigest,
			"disk":      diskDigest,
			"cpu":       cpuDigest,
		} {
			if len(digest) != 32 {
				t.Fatalf("%s digest length = %d", name, len(digest))
			}
		}
		if string(storedDeviceHash) != string(HashDeviceID(canonical)) {
			t.Fatal("session device hash was not derived from the canonical fingerprint")
		}
		if storedDeviceSuffix != DeviceIDSuffix(fingerprintID) || storedDeviceSuffix == "a479" {
			t.Fatalf("structured device suffix = %q", storedDeviceSuffix)
		}

		var linkedLoginEvents int
		if err := pool.QueryRow(ctx, `
			SELECT COUNT(*) FROM auth_login_events
			WHERE session_id = $1 AND device_fingerprint_id = $2
		`, bound.Tokens.SessionID, fingerprintID).Scan(&linkedLoginEvents); err != nil {
			t.Fatal(err)
		}
		if linkedLoginEvents != 1 {
			t.Fatalf("linked login events = %d, want 1", linkedLoginEvents)
		}

		refreshMeta := meta
		refreshMeta.DeviceID = "ds:a867d4d49d01c90a|cp:f846ffb743eca479|uu:b09bc26d38bb76d6"
		rotated, err := service.Refresh(ctx, bound.Tokens.RefreshToken, refreshMeta)
		if err != nil {
			t.Fatal(err)
		}
		var rotatedFingerprintID string
		if err := pool.QueryRow(ctx, `
			SELECT COALESCE(device_fingerprint_id, '')
			FROM auth_sessions
			WHERE id = $1
		`, rotated.Tokens.SessionID).Scan(&rotatedFingerprintID); err != nil {
			t.Fatal(err)
		}
		if rotatedFingerprintID != fingerprintID {
			t.Fatalf("rotated fingerprint ID = %q, want %q", rotatedFingerprintID, fingerprintID)
		}
		var fingerprintCount int
		if err := pool.QueryRow(ctx, `
			SELECT COUNT(*) FROM auth_device_fingerprints
			WHERE digest_key_id = $1 AND composite_digest = $2
		`, digestKeyID, compositeDigest).Scan(&fingerprintCount); err != nil {
			t.Fatal(err)
		}
		if fingerprintCount != 1 {
			t.Fatalf("canonical fingerprint rows = %d, want 1", fingerprintCount)
		}
	})

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
		touchedAt := time.Now().UTC()
		if _, err := pool.Exec(ctx, "UPDATE auth_sessions SET last_used_at = $2 WHERE id = $1", second.Tokens.SessionID, touchedAt.Add(-10*time.Minute)); err != nil {
			t.Fatal(err)
		}
		if err := NewRepository().TouchSession(ctx, pool, second.Tokens.SessionID, touchedAt); err != nil {
			t.Fatalf("TouchSession() against PostgreSQL: %v", err)
		}
		var lastUsedAt time.Time
		if err := pool.QueryRow(ctx, "SELECT last_used_at FROM auth_sessions WHERE id = $1", second.Tokens.SessionID).Scan(&lastUsedAt); err != nil {
			t.Fatal(err)
		}
		if lastUsedAt.Before(touchedAt.Add(-time.Second)) {
			t.Fatalf("last_used_at = %v, want approximately %v", lastUsedAt, touchedAt)
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

	t.Run("player manages only owned sessions", func(t *testing.T) {
		steamID := nextSteamID()
		createdSteamIDs = append(createdSteamIDs, steamID)
		first, err := service.Bind(ctx, BindInput{
			SteamID: steamID, PersonaName: "Session One", DeviceID: "device-one-1111",
		}, meta)
		if err != nil {
			t.Fatal(err)
		}
		secondMeta := meta
		secondMeta.IPAddress = "198.51.100.42"
		second, err := service.Bind(ctx, BindInput{
			SteamID: steamID, PersonaName: "Session Two", DeviceID: "device-two-2222",
		}, secondMeta)
		if err != nil {
			t.Fatal(err)
		}
		sessions, err := service.ListUserSessions(ctx, second.Player.ID, second.Tokens.SessionID)
		if err != nil {
			t.Fatal(err)
		}
		if len(sessions) != 2 {
			t.Fatalf("active sessions = %#v", sessions)
		}
		currentCount := 0
		for _, session := range sessions {
			if session.IsCurrent {
				currentCount++
			}
			if session.IPAddress != "192.0.2.xxx" && session.IPAddress != "198.51.100.xxx" {
				t.Fatalf("session contains unmasked IP: %#v", session)
			}
			if len(session.DeviceIDSuffix) > 4 {
				t.Fatalf("session contains full device ID: %#v", session)
			}
		}
		if currentCount != 1 {
			t.Fatalf("current session count = %d", currentCount)
		}
		if err := service.RevokeUserSession(ctx, second.Player.ID, first.Tokens.SessionID); err != nil {
			t.Fatal(err)
		}
		if _, err := service.AuthenticateAccess(ctx, first.Tokens.AccessToken); ErrorCode(err) != CodeSessionRevoked {
			t.Fatalf("revoked session access error = %v", err)
		}
		third, err := service.Bind(ctx, BindInput{SteamID: steamID, PersonaName: "Session Three"}, meta)
		if err != nil {
			t.Fatal(err)
		}
		revoked, err := service.RevokeOtherUserSessions(ctx, second.Player.ID, second.Tokens.SessionID)
		if err != nil || revoked != 1 {
			t.Fatalf("revoke others = %d, %v", revoked, err)
		}
		if _, err := service.AuthenticateAccess(ctx, second.Tokens.AccessToken); err != nil {
			t.Fatalf("current session was revoked: %v", err)
		}
		if _, err := service.AuthenticateAccess(ctx, third.Tokens.AccessToken); ErrorCode(err) != CodeSessionRevoked {
			t.Fatalf("other session access error = %v", err)
		}
		otherSteamID := nextSteamID()
		createdSteamIDs = append(createdSteamIDs, otherSteamID)
		otherPlayer, err := service.Bind(ctx, BindInput{SteamID: otherSteamID, PersonaName: "Other Player"}, meta)
		if err != nil {
			t.Fatal(err)
		}
		if err := service.RevokeUserSession(ctx, second.Player.ID, otherPlayer.Tokens.SessionID); ErrorCode(err) != CodeSessionNotFound {
			t.Fatalf("cross-player session revocation error = %v", err)
		}
		if _, err := service.AuthenticateAccess(ctx, otherPlayer.Tokens.AccessToken); err != nil {
			t.Fatalf("cross-player revocation changed other session: %v", err)
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

func mustTicketHashForTest(t *testing.T, ticketHex string) []byte {
	t.Helper()
	_, hash, err := normalizeEncryptedTicket(ticketHex, config.Defaults.Auth.TicketMaximumHexBytes)
	if err != nil {
		t.Fatal(err)
	}
	return hash
}
