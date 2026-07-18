package compose_test

import (
	"os"
	"strings"
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
			Profiles    []string       `yaml:"profiles"`
			Environment map[string]any `yaml:"environment"`
			Ports       []string       `yaml:"ports"`
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

func TestSeparatedControlPlaneHasSecureNetworkAndPersistentSecrets(t *testing.T) {
	contents, err := os.ReadFile("../control-plane/docker-compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Services map[string]struct {
			Environment map[string]any `yaml:"environment"`
			Ports       []string       `yaml:"ports"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(contents, &document); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"postgres", "redis"} {
		ports := document.Services[name].Ports
		if len(ports) != 1 || !strings.HasPrefix(ports[0], "127.0.0.1:") {
			t.Fatalf("separated %s port must be loopback-only: %#v", name, ports)
		}
	}
	control := document.Services["control-plane"]
	if len(control.Ports) != 2 || !strings.HasPrefix(control.Ports[0], "127.0.0.1:") {
		t.Fatalf("direct control-plane HTTP must be loopback-only: %#v", control.Ports)
	}
	for _, name := range []string{
		"ACCESS_TOKEN_PRIVATE_KEY_BASE64", "RELAY_CA_CERT_PEM_BASE64", "RELAY_CA_KEY_PEM_BASE64",
		"RELAY_TOKEN_PRIVATE_KEY_BASE64", "UPDATE_SIGNING_PRIVATE_KEY_BASE64", "ADMIN_TOKENS",
	} {
		if _, ok := control.Environment[name]; !ok {
			t.Fatalf("separated control plane does not inject %s", name)
		}
	}
	caddy, err := os.ReadFile("../control-plane/Caddyfile")
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range []string{"handle /internal/v1/relay-nodes/enroll", "handle /internal/*", "handle /v1/admin*"} {
		if !strings.Contains(string(caddy), rule) {
			t.Fatalf("public Caddy policy is missing %q", rule)
		}
	}
}

func TestSeparatedEdgeRelayHasNoControlPlaneDependencies(t *testing.T) {
	contents, err := os.ReadFile("../edge-relay/docker-compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Services map[string]struct {
			NetworkMode string         `yaml:"network_mode"`
			Environment map[string]any `yaml:"environment"`
			CapDrop     []string       `yaml:"cap_drop"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(contents, &document); err != nil {
		t.Fatal(err)
	}
	if len(document.Services) != 1 {
		t.Fatalf("edge deployment must contain only the edge relay: %#v", document.Services)
	}
	relay, ok := document.Services["edge-relay"]
	if !ok || relay.NetworkMode != "host" {
		t.Fatalf("edge relay must use Linux host networking: %#v", relay)
	}
	for _, forbidden := range []string{"DATABASE_URL", "REDIS_ADDRESS", "NATS_URL"} {
		if _, exists := relay.Environment[forbidden]; exists {
			t.Fatalf("separated edge relay depends on forbidden service through %s", forbidden)
		}
	}
	if len(relay.CapDrop) != 1 || relay.CapDrop[0] != "ALL" {
		t.Fatalf("edge relay must drop Linux capabilities: %#v", relay.CapDrop)
	}
}

func TestMonitoringProfileStaysOptInAndLoopbackOnly(t *testing.T) {
	contents, err := os.ReadFile("docker-compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Services map[string]struct {
			Profiles []string `yaml:"profiles"`
			Ports    []string `yaml:"ports"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(contents, &document); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"prometheus", "grafana"} {
		service, ok := document.Services[name]
		if !ok || len(service.Profiles) != 1 || service.Profiles[0] != "monitoring" {
			t.Fatalf("%s must be an opt-in monitoring service: %#v", name, service)
		}
		for _, port := range service.Ports {
			if len(port) < len("127.0.0.1:") || port[:len("127.0.0.1:")] != "127.0.0.1:" {
				t.Fatalf("%s exposes a non-loopback port: %s", name, port)
			}
		}
	}
}

func TestDevelopmentDatastoresAreLoopbackOnly(t *testing.T) {
	contents, err := os.ReadFile("docker-compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Services map[string]struct {
			Ports []string `yaml:"ports"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(contents, &document); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"postgres", "redis"} {
		service := document.Services[name]
		if len(service.Ports) != 1 || len(service.Ports[0]) < len("127.0.0.1:") || service.Ports[0][:len("127.0.0.1:")] != "127.0.0.1:" {
			t.Fatalf("%s must expose exactly one loopback-only port: %#v", name, service.Ports)
		}
	}
}
