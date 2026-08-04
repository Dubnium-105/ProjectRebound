package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMissingFileAppliesEnvironment(t *testing.T) {
	t.Setenv("CONTROL_PLANE_HTTP_ADDR", ":9191")
	t.Setenv("CORS_ALLOWED_ORIGINS", "https://one.example, https://two.example")
	t.Setenv("RELAY_CONTROL_SERVER_NAMES", "control-plane, relay.example.com")
	t.Setenv("AUTH_BIND_PER_IP_PER_MINUTE", "7")
	t.Setenv("AUTH_INVITE_REQUIRED", "true")
	t.Setenv("TOOLBOX_PUBKEY_PATH", "/run/projectrebound/test-signer.pem")
	t.Setenv("INTEGRITY_CHALLENGE_TTL_SECONDS", "90")
	t.Setenv("RELAY_HEARTBEAT_INTERVAL_SECONDS", "2")
	t.Setenv("RELAY_UNHEALTHY_AFTER_SECONDS", "6")
	t.Setenv("RELAY_OFFLINE_AFTER_SECONDS", "10")
	t.Setenv("RELAY_SWEEP_INTERVAL_SECONDS", "1")
	t.Setenv("P2P_BATTLELOG_ENABLED", "true")
	t.Setenv("P2P_BATTLELOG_SHADOW_MODE", "false")
	t.Setenv("P2P_BATTLELOG_COLLECTION_DEADLINE_SECONDS", "180")
	t.Setenv("VNT_ROOMS_ENABLED", "true")
	t.Setenv("VNT_ALLOWED_VNTS_VERSIONS", "1.2.3,1.2.4")
	t.Setenv("VNT_ALLOWED_WRAPPER_VERSIONS", "0.4.0")
	t.Setenv("VNT_CREDENTIAL_ROTATION_GRACE_SECONDS", "75")
	t.Setenv("VNT_ENROLLMENT_REQUESTS_PER_PLAYER_PER_HOUR", "6")
	t.Setenv("VNT_DIRECTORY_REQUESTS_PER_IP_PER_MINUTE", "122")
	t.Setenv("VNT_BOOTSTRAP_REQUESTS_PER_PLAYER_PER_MINUTE", "31")
	t.Setenv("VNT_HEARTBEAT_REQUESTS_PER_CREDENTIAL_PER_MINUTE", "121")
	t.Setenv("VNT_MANAGEMENT_REQUESTS_PER_CREDENTIAL_PER_HOUR", "11")
	t.Setenv("VNT_MAX_NODES_PER_PLAYER", "4")

	cfg, err := Load(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.HTTP.Addr != ":9191" {
		t.Fatalf("HTTP.Addr = %q", cfg.HTTP.Addr)
	}
	if len(cfg.CORS.AllowedOrigins) != 2 || cfg.CORS.AllowedOrigins[1] != "https://two.example" {
		t.Fatalf("AllowedOrigins = %#v", cfg.CORS.AllowedOrigins)
	}
	if len(cfg.RelayRegistry.ServerNames) != 2 || cfg.RelayRegistry.ServerNames[1] != "relay.example.com" {
		t.Fatalf("RelayRegistry.ServerNames = %#v", cfg.RelayRegistry.ServerNames)
	}
	if cfg.Auth.BindRateLimit.PerIPPerMinute != 7 || !cfg.Auth.InviteRequired {
		t.Fatalf("Auth bind config = %#v", cfg.Auth)
	}
	if cfg.Auth.IntegrityPublicKeyPath != "/run/projectrebound/test-signer.pem" ||
		cfg.Auth.IntegrityChallengeTTLSeconds != 90 {
		t.Fatalf("Auth integrity config = %#v", cfg.Auth)
	}
	if cfg.RelayRegistry.HeartbeatIntervalSeconds != 2 || cfg.RelayRegistry.UnhealthyAfterSeconds != 6 ||
		cfg.RelayRegistry.OfflineAfterSeconds != 10 || cfg.RelayRegistry.SweepIntervalSeconds != 1 {
		t.Fatalf("Relay registry timing = %#v", cfg.RelayRegistry)
	}
	if !cfg.P2PBattleLog.Enabled || cfg.P2PBattleLog.ShadowMode ||
		cfg.P2PBattleLog.CollectionDeadlineSeconds != 180 {
		t.Fatalf("P2P BattleLog config = %#v", cfg.P2PBattleLog)
	}
	if !cfg.Update.VNTRoomsEnabled {
		t.Fatal("VNT_ROOMS_ENABLED environment override was not applied")
	}
	if len(cfg.VNT.AllowedVNTSVersions) != 2 || cfg.VNT.AllowedWrapperVersions[0] != "0.4.0" ||
		cfg.VNT.CredentialRotationGraceSeconds != 75 || cfg.VNT.EnrollmentRequestsPerPlayerPerHour != 6 ||
		cfg.VNT.DirectoryRequestsPerIPPerMinute != 122 || cfg.VNT.BootstrapRequestsPerPlayerPerMinute != 31 ||
		cfg.VNT.HeartbeatRequestsPerCredentialPerMinute != 121 || cfg.VNT.ManagementRequestsPerCredentialPerHour != 11 ||
		cfg.VNT.MaxNodesPerPlayer != 4 {
		t.Fatalf("VNT version policy = %#v", cfg.VNT)
	}
}

func TestValidateControlPlaneRejectsInvalidVNTCredentialRotationGrace(t *testing.T) {
	cfg := Defaults
	cfg.VNT.CredentialRotationGraceSeconds = 0
	if err := cfg.ValidateControlPlane(); err == nil {
		t.Fatal("zero VNT credential rotation grace was accepted")
	}
	cfg.VNT.CredentialRotationGraceSeconds = 601
	if err := cfg.ValidateControlPlane(); err == nil {
		t.Fatal("excessive VNT credential rotation grace was accepted")
	}
}

func TestValidateControlPlaneRejectsInvalidVNTRateLimits(t *testing.T) {
	cfg := Defaults
	cfg.VNT.EnrollmentRequestsPerPlayerPerHour = 0
	if err := cfg.ValidateControlPlane(); err == nil {
		t.Fatal("zero VNT enrollment rate limit was accepted")
	}
	cfg = Defaults
	cfg.VNT.DirectoryRequestsPerIPPerMinute = 0
	if err := cfg.ValidateControlPlane(); err == nil {
		t.Fatal("zero VNT directory rate limit was accepted")
	}
	cfg = Defaults
	cfg.VNT.BootstrapRequestsPerPlayerPerMinute = 1_001
	if err := cfg.ValidateControlPlane(); err == nil {
		t.Fatal("excessive VNT bootstrap rate limit was accepted")
	}
	cfg = Defaults
	cfg.VNT.HeartbeatRequestsPerCredentialPerMinute = 10_001
	if err := cfg.ValidateControlPlane(); err == nil {
		t.Fatal("excessive VNT heartbeat rate limit was accepted")
	}
	cfg = Defaults
	cfg.VNT.ManagementRequestsPerCredentialPerHour = 0
	if err := cfg.ValidateControlPlane(); err == nil {
		t.Fatal("zero VNT management rate limit was accepted")
	}
}

func TestValidateControlPlaneRejectsInvalidVNTNodeQuota(t *testing.T) {
	cfg := Defaults
	cfg.VNT.MaxNodesPerPlayer = 0
	if err := cfg.ValidateControlPlane(); err == nil {
		t.Fatal("zero VNT node quota was accepted")
	}
	cfg.VNT.MaxNodesPerPlayer = 101
	if err := cfg.ValidateControlPlane(); err == nil {
		t.Fatal("excessive VNT node quota was accepted")
	}
}

func TestValidateControlPlaneRequiresVNTVersionAllowlistsWhenEnabled(t *testing.T) {
	cfg := Defaults
	cfg.Update.VNTRoomsEnabled = true
	if err := cfg.ValidateControlPlane(); err == nil {
		t.Fatal("enabled VNT rooms without version allowlists were accepted")
	}
	cfg.VNT.AllowedVNTSVersions = []string{"1.2.3"}
	cfg.VNT.AllowedWrapperVersions = []string{"0.4.0"}
	if err := cfg.ValidateControlPlane(); err != nil {
		t.Fatalf("valid VNT allowlists were rejected: %v", err)
	}
}

func TestLoadYAMLAndEnvironmentPrecedence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	data := []byte("http:\n  addr: ':8181'\nredis:\n  address: redis.internal:6379\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONTROL_PLANE_HTTP_ADDR", ":8282")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.HTTP.Addr != ":8282" || cfg.Redis.Address != "redis.internal:6379" {
		t.Fatalf("unexpected config: %#v", cfg)
	}
}

func TestValidateControlPlaneRejectsInvalidConfiguration(t *testing.T) {
	cfg := Defaults
	cfg.Database.URL = "sqlite://local.db"
	cfg.RateLimit.Burst = 0
	if err := cfg.ValidateControlPlane(); err == nil {
		t.Fatal("ValidateControlPlane() returned nil")
	}
}

func TestValidateControlPlaneAcceptsToolboxDefaultChannel(t *testing.T) {
	cfg := Defaults
	cfg.Update.DefaultChannel = "toolbox"
	if err := cfg.ValidateControlPlane(); err != nil {
		t.Fatalf("ValidateControlPlane() error = %v", err)
	}
}

func TestValidateControlPlaneRejectsP2PReportLargerThanHTTPBody(t *testing.T) {
	cfg := Defaults
	cfg.HTTP.MaxRequestBodyBytes = 128 * 1024
	cfg.P2PBattleLog.MaxReportBytes = 256 * 1024
	if err := cfg.ValidateControlPlane(); err == nil {
		t.Fatal("ValidateControlPlane() returned nil")
	}
}
