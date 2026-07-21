package loadbot

import (
	"path/filepath"
	"testing"
)

func TestV11ScenarioFilesMatchReleaseGates(t *testing.T) {
	tests := []struct {
		name             string
		clients          int
		rooms            int
		relayConnections int
		duration         string
	}{
		{"scenario-v1.1-full.yaml", 300, 100, 100, "6h"},
		{"scenario-v1.1-relay-soak.yaml", 200, 100, 100, "24h"},
	}
	for _, test := range tests {
		cfg, err := LoadConfig(filepath.Join("..", "..", "tests", "load", test.name))
		if err != nil {
			t.Fatalf("%s: %v", test.name, err)
		}
		if cfg.Clients != test.clients || cfg.Rooms != test.rooms || cfg.RelayConnections != test.relayConnections || cfg.Duration != test.duration {
			t.Errorf("%s does not match its release gate: %#v", test.name, cfg)
		}
		if cfg.Traffic.PacketsPerSecond <= 0 || cfg.Traffic.PayloadBytes <= 0 || cfg.Auth.RefreshInterval == "" {
			t.Errorf("%s does not exercise traffic and token rotation", test.name)
		}
	}
}
