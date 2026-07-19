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
