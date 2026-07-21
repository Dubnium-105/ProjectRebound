package compose_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestV11ReleaseScriptsEnforceSafetyGates(t *testing.T) {
	files := map[string][]string{
		"preflight.sh": {
			"config-placeholders", "object-storage", "backup-verification", "image-digest",
			"postgres", "redis", "migration-state", "relay-ca-expiry", "openapi-schema", "disk-space",
		},
		"control-plane.sh":      {"postgres-backup.sh", "preflight.sh", "verify-control-plane.sh", "rollback.sh"},
		"rolling-edge-relay.sh": {"migrate_existing", "active_allocations", "rollback.sh", "resume", "READY"},
		"rollback.sh":           {"latest", "database_rollback=false", "verify-control-plane.sh"},
	}
	for name, required := range files {
		contents, err := os.ReadFile(filepath.Join("..", "..", "scripts", "release", name))
		if err != nil {
			t.Fatal(err)
		}
		for _, text := range required {
			if !strings.Contains(string(contents), text) {
				t.Errorf("%s does not enforce %q", name, text)
			}
		}
	}
}

func TestLoadBotHasMinimalContainerImage(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join("..", "load-bot", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"./cmd/load-bot", "FROM scratch", "USER 65532:65532", "ca-certificates.crt"} {
		if !strings.Contains(string(contents), required) {
			t.Errorf("load-bot Dockerfile is missing %q", required)
		}
	}
}
