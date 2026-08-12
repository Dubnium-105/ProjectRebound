package admin

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/auth"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/config"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/database"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/player"
	clientupdate "github.com/Dubnium-105/ProjectRebound/Backend/internal/update"
	"github.com/jackc/pgx/v5/pgxpool"
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
	t.Cleanup(pool.Close)
	if err := database.NewMigrator(pool).Up(ctx); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}

	playerRepository := player.NewRepository()
	authRepository := auth.NewRepository()
	tokenManager, _, err := auth.NewTokenManager(config.Defaults.Auth, "development")
	if err != nil {
		t.Fatal(err)
	}
	deviceFingerprinter, _, err := auth.NewDeviceFingerprinter(config.Defaults.Auth, "development")
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	authService := auth.NewService(
		pool, authRepository, playerRepository, tokenManager, deviceFingerprinter, config.Defaults.Auth, logger,
	)
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
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM auth_risk_events WHERE player_id = $1", bound.Player.ID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM auth_login_events WHERE player_id = $1", bound.Player.ID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM auth_login_audit_logs WHERE player_id = $1", bound.Player.ID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM auth_sessions WHERE player_id = $1", bound.Player.ID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM players WHERE id = $1", bound.Player.ID)
	})

	banned := player.AccountStatus("banned")
	vip := true
	patched, err := service.PatchPlayer(ctx, bound.Player.ID, PlayerPatch{
		AccountStatus: &banned,
		IsVIP:         &vip,
		Reason:        "Integration test account moderation",
		InternalNote:  "Work order TEST-42",
	}, RequestMeta{AdminID: "operator", RequestID: "req_admin_patch", IPAddress: "10.0.0.5", UserAgent: "admin-integration-test"})
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

	revoked, err := service.RevokePlayerSessions(ctx, bound.Player.ID, "Integration test session revocation", RequestMeta{
		AdminID: "operator", RequestID: "req_admin_revoke", IPAddress: "10.0.0.5", UserAgent: "admin-integration-test",
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
		  AND reason <> ''
		  AND user_agent = 'admin-integration-test'
		  AND result = 'SUCCEEDED'
	`, bound.Player.ID).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 2 {
		t.Fatalf("admin audit count = %d", auditCount)
	}
}

func TestAdministratorGovernanceAgainstPostgreSQL(t *testing.T) {
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
	secretBox, _, err := NewSecretBox("", "development")
	if err != nil {
		t.Fatal(err)
	}
	service := NewGovernanceService(pool, NewRepository(), secretBox)
	username := fmt.Sprintf("governance-%d@example.test", time.Now().UnixNano())
	meta := RequestMeta{
		AdminID: "integration-operator", RequestID: "req_governance_create",
		IPAddress: "192.0.2.50", UserAgent: "admin-governance-integration-test",
	}
	created, err := service.CreateAdmin(ctx, CreateGovernedAdminInput{
		Username: username, DisplayName: "Governance Target",
		Password: "integration-password-123", Roles: []string{"viewer"},
		Reason: "Integration test administrator creation",
	}, meta)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM admin_audit_logs WHERE target_id = $1", created.Admin.ID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM admin_users WHERE id = $1", created.Admin.ID)
	})
	if !strings.HasPrefix(created.ProvisioningURI, "otpauth://totp/") || len(created.RecoveryCodes) != 10 {
		t.Fatalf("one-time administrator provisioning result = %#v", created)
	}
	if created.Admin.Status != AdminStatusActive || !created.Admin.MFAEnabled ||
		strings.Join(created.Admin.Roles, ",") != "VIEWER" {
		t.Fatalf("created administrator = %#v", created.Admin)
	}

	displayName := "Updated Governance Target"
	status := AdminStatusActive
	meta.RequestID = "req_governance_update"
	updated, err := service.UpdateAdmin(ctx, created.Admin.ID, UpdateGovernedAdminInput{
		DisplayName: &displayName, Status: &status,
		Roles: []string{"operations"}, RolesSet: true, RevokeSessions: true,
		Reason: "Integration test role update",
	}, meta)
	if err != nil {
		t.Fatal(err)
	}
	if updated.DisplayName != displayName || strings.Join(updated.Roles, ",") != "OPERATIONS" {
		t.Fatalf("updated administrator = %#v", updated)
	}

	meta.RequestID = "req_governance_mfa_reset"
	reset, err := service.ResetMFA(
		ctx, created.Admin.ID, "Integration test MFA rotation", meta,
	)
	if err != nil {
		t.Fatal(err)
	}
	if reset.ProvisioningURI == created.ProvisioningURI || len(reset.RecoveryCodes) != 10 {
		t.Fatalf("rotated provisioning result = %#v", reset)
	}
	var recoveryCodeCount, auditCount int
	if err := pool.QueryRow(
		ctx, "SELECT COUNT(*) FROM admin_recovery_codes WHERE admin_id = $1",
		created.Admin.ID,
	).Scan(&recoveryCodeCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM admin_audit_logs
		WHERE target_id = $1 AND admin_id = 'integration-operator'
		  AND action IN ('ADMIN_CREATED', 'ADMIN_UPDATED', 'ADMIN_MFA_RESET')
		  AND reason <> '' AND result = 'SUCCEEDED'
	`, created.Admin.ID).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if recoveryCodeCount != 10 || auditCount != 3 {
		t.Fatalf("recovery codes = %d, audits = %d", recoveryCodeCount, auditCount)
	}
}

type integrationReleaseManifestService struct{}

func (integrationReleaseManifestService) BuildAndSign(source clientupdate.SourceRelease) (clientupdate.Manifest, error) {
	files := make([]clientupdate.File, 0, len(source.Files))
	for _, file := range source.Files {
		files = append(files, clientupdate.File{
			FileID: file.FileID, Path: file.Path, Size: file.Size,
			SHA256: file.SHA256, Compression: file.Compression,
			DownloadURL: "https://cdn.example.test/" + strings.TrimLeft(file.ObjectKey, "/"),
		})
	}
	return clientupdate.Manifest{
		SchemaVersion: source.SchemaVersion, Product: source.Product,
		Platform: source.Platform, Architecture: source.Architecture,
		Channel: source.Channel, Version: source.Version,
		MinimumSupportedVersion: source.MinimumSupportedVersion,
		PublishedAt:             source.PublishedAt, Files: files,
		ManifestHash:       "integration-manifest-hash",
		SignatureAlgorithm: clientupdate.SignatureAlgorithm,
		KeyID:              "integration-key", Signature: "integration-signature",
	}, nil
}

func (integrationReleaseManifestService) VerifySignedManifest(clientupdate.Manifest) error {
	return nil
}

func (integrationReleaseManifestService) VerifyReleaseObjects(context.Context, clientupdate.SourceRelease) error {
	return nil
}

func (integrationReleaseManifestService) ResolveVNTRuntime(_ context.Context, source clientupdate.SourceRelease) (clientupdate.SourceRelease, error) {
	return source, nil
}

func TestManagedReleaseLifecycleAgainstPostgreSQL(t *testing.T) {
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

	adminID := newID("adm_integration_")
	now := time.Now().UTC()
	if _, err := pool.Exec(ctx, `
		INSERT INTO admin_users (
			id, username, display_name, password_hash, status,
			mfa_required, created_at, updated_at
		) VALUES ($1, $2, 'Release Integration', 'integration-test-hash',
		          'ACTIVE', TRUE, $3, $3)
	`, adminID, adminID+"@example.test", now); err != nil {
		t.Fatal(err)
	}
	service := NewReleaseService(
		pool, NewReleaseRepository(pool), NewRepository(),
		integrationReleaseManifestService{}, "project-rebound",
	)
	version := fmt.Sprintf("99.0.%d", now.UnixNano()%1_000_000_000)
	meta := RequestMeta{
		AdminID: adminID, RequestID: "req_release_create",
		IPAddress: "192.0.2.60", UserAgent: "admin-release-integration-test",
	}
	created, err := service.Create(ctx, ReleaseCreateInput{
		Platform: "windows", Architecture: "amd64", Channel: "stable",
		Version: version, MinimumSupportedVersion: "1.0.0",
		Files: []clientupdate.SourceFile{{
			FileID: "integration-release-file", Path: "bin/game.exe", Size: 42,
			SHA256: strings.Repeat("a", 64), Compression: "none",
			ObjectKey: "integration/" + version + "/game.exe",
		}},
		Reason: "Integration test release creation",
	}, meta)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM admin_audit_logs WHERE target_id = $1", created.ID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM admin_releases WHERE id = $1", created.ID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM admin_users WHERE id = $1", adminID)
	})

	meta.RequestID = "req_release_validate"
	ready, err := service.Validate(ctx, created.ID, "Integration test validation", meta)
	if err != nil || ready.Status != ReleaseStatusReady || !ready.Validation.Valid {
		t.Fatalf("validated release = %#v, %v", ready, err)
	}
	meta.RequestID = "req_release_publish"
	published, err := service.Publish(ctx, created.ID, "Integration test publication", meta)
	if err != nil || published.Status != ReleaseStatusPublished || published.PublishedBy != adminID {
		t.Fatalf("published release = %#v, %v", published, err)
	}
	catalog, err := service.PublishedManifests(ctx)
	if err != nil || len(catalog) != 1 || catalog[0].Version != created.Version {
		t.Fatalf("published catalog = %#v, %v", catalog, err)
	}
	meta.RequestID = "req_release_rollback"
	rolledBack, err := service.Rollback(ctx, created.ID, "Integration test rollback", meta)
	if err != nil || rolledBack.Status != ReleaseStatusRolledBack || rolledBack.RolledBackBy != adminID {
		t.Fatalf("rolled-back release = %#v, %v", rolledBack, err)
	}
	meta.RequestID = "req_release_archive"
	archived, err := service.Archive(ctx, created.ID, "Integration test archive", meta)
	if err != nil || archived.Status != ReleaseStatusArchived || archived.ArchivedBy != adminID {
		t.Fatalf("archived release = %#v, %v", archived, err)
	}
	catalog, err = service.PublishedManifests(ctx)
	if err != nil || len(catalog) != 0 {
		t.Fatalf("catalog after rollback = %#v, %v", catalog, err)
	}
	var auditCount int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM admin_audit_logs
		WHERE target_id = $1 AND admin_id = $2
		  AND action IN (
		    'RELEASE_CREATED', 'RELEASE_VALIDATED', 'RELEASE_PUBLISHED',
		    'RELEASE_ROLLED_BACK', 'RELEASE_ARCHIVED'
		  )
		  AND reason <> '' AND result = 'SUCCEEDED'
	`, created.ID, adminID).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 5 {
		t.Fatalf("release audit count = %d", auditCount)
	}
}
