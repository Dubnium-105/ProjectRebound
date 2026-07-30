package loadbot

import (
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"
)

func TestFixtureEncryptedTicketCarriesIdentityAndUniqueNonce(t *testing.T) {
	const steamID = "76561198000000000"
	first := fixtureEncryptedTicket(steamID)
	second := fixtureEncryptedTicket(steamID)
	if first == second {
		t.Fatal("fixture tickets were replayable")
	}
	decoded, err := hex.DecodeString(first)
	if err != nil || !strings.HasPrefix(string(decoded), steamID+"|") {
		t.Fatalf("fixture ticket = %q, %v", decoded, err)
	}
}

func TestV11ScenarioFilesMatchReleaseGates(t *testing.T) {
	tests := []struct {
		name             string
		clients          int
		rooms            int
		relayConnections int
		duration         string
	}{
		{"scenario-v1.1-basic.yaml", 100, 30, 20, "1h"},
		{"scenario-v1.1-full.yaml", 300, 100, 100, "6h"},
		{"scenario-v1.1-relay-soak.yaml", 200, 100, 100, "24h"},
		{"scenario-v1.1-reconnect-storm.yaml", 100, 50, 50, "10m"},
		{"scenario-v1.1-relay-failure.yaml", 100, 50, 50, "30m"},
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
		if !cfg.Auth.UnsafeTestTicketFixture {
			t.Errorf("%s does not exercise verified Steam sessions", test.name)
		}
	}
}
