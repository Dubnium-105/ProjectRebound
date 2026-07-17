package compose_test

import (
	"os"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestComposeDefinesIsolatedOptionalEdgeRelay(t *testing.T) {
	contents, err := os.ReadFile("docker-compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Services map[string]struct {
			Profiles []string          `yaml:"profiles"`
			Environment map[string]any `yaml:"environment"`
			Ports []string             `yaml:"ports"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(contents, &document); err != nil {
		t.Fatalf("parse Docker Compose YAML: %v", err)
	}
	relay, ok := document.Services["edge-relay"]
	if !ok {
		t.Fatal("edge-relay service is missing")
	}
	if len(relay.Profiles) != 1 || relay.Profiles[0] != "relay" {
		t.Fatalf("edge-relay must remain opt-in: %#v", relay.Profiles)
	}
	if len(relay.Ports) != 1 || relay.Ports[0] != "8443:8443/udp" {
		t.Fatalf("edge-relay exposes unexpected ports: %#v", relay.Ports)
	}
	for _, forbidden := range []string{"DATABASE_URL", "REDIS_ADDRESS", "NATS_URL"} {
		if _, exists := relay.Environment[forbidden]; exists {
			t.Fatalf("edge-relay depends on forbidden service through %s", forbidden)
		}
	}
}
