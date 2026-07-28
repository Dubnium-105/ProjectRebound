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
	if control.Ports[1] != "${RELAY_CONTROL_BIND_IP:-127.0.0.1}:${RELAY_CONTROL_PORT:-9090}:9090" {
		t.Fatalf("relay control endpoint must default to loopback: %#v", control.Ports)
	}
	for _, name := range []string{
		"ACCESS_TOKEN_PRIVATE_KEY_BASE64", "RELAY_CA_CERT_PEM_BASE64", "RELAY_CA_KEY_PEM_BASE64",
		"RELAY_TOKEN_PRIVATE_KEY_BASE64", "UPDATE_SIGNING_PRIVATE_KEY_BASE64", "ADMIN_TOKENS",
		"ADMIN_ACCESS_TOKEN_PRIVATE_KEY_BASE64", "ADMIN_MFA_ENCRYPTION_KEY_BASE64",
		"TURNSTILE_SITE_KEY", "TURNSTILE_SECRET_KEY",
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

func TestSeparatedAdminWebHasOnlyEdgeNetworkAndNoSecrets(t *testing.T) {
	contents, err := os.ReadFile("../control-plane/docker-compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Services map[string]struct {
			Environment map[string]any `yaml:"environment"`
			Networks    []string       `yaml:"networks"`
			ReadOnly    bool           `yaml:"read_only"`
			CapDrop     []string       `yaml:"cap_drop"`
			Expose      []string       `yaml:"expose"`
		} `yaml:"services"`
		Networks map[string]struct {
			Internal bool `yaml:"internal"`
		} `yaml:"networks"`
	}
	if err := yaml.Unmarshal(contents, &document); err != nil {
		t.Fatal(err)
	}
	adminWeb, ok := document.Services["admin-web"]
	if !ok {
		t.Fatal("separated deployment is missing admin-web")
	}
	if len(adminWeb.Networks) != 1 || adminWeb.Networks[0] != "edge" {
		t.Fatalf("admin-web must join only the edge network: %#v", adminWeb.Networks)
	}
	if !adminWeb.ReadOnly || len(adminWeb.CapDrop) != 1 || adminWeb.CapDrop[0] != "ALL" {
		t.Fatalf("admin-web container hardening is incomplete: %#v", adminWeb)
	}
	if len(adminWeb.Expose) != 1 || adminWeb.Expose[0] != "8080" {
		t.Fatalf("admin-web exposes unexpected ports: %#v", adminWeb.Expose)
	}
	for _, secret := range []string{
		"TURNSTILE_SECRET_KEY", "ADMIN_MFA_ENCRYPTION_KEY_BASE64",
		"ADMIN_ACCESS_TOKEN_PRIVATE_KEY_BASE64", "DATABASE_URL", "REDIS_PASSWORD",
	} {
		if _, exists := adminWeb.Environment[secret]; exists {
			t.Fatalf("admin-web received forbidden secret %s", secret)
		}
	}
	if !document.Networks["data"].Internal {
		t.Fatal("control-plane data network must remain internal")
	}

	caddy, err := os.ReadFile("../control-plane/Caddyfile")
	if err != nil {
		t.Fatal(err)
	}
	for _, policy := range []string{
		"{$ADMIN_WEB_SITE:admin.example.com}",
		"reverse_proxy admin-web:8080",
		"frame-src https://challenges.cloudflare.com",
		"frame-ancestors 'none'",
		"X-Frame-Options DENY",
	} {
		if !strings.Contains(string(caddy), policy) {
			t.Fatalf("administrator Caddy policy is missing %q", policy)
		}
	}
}

func TestSeparatedMetaServerIsHardenedAndOptIn(t *testing.T) {
	contents, err := os.ReadFile("../control-plane/docker-compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Services map[string]struct {
			Profiles    []string       `yaml:"profiles"`
			Environment map[string]any `yaml:"environment"`
			Ports       []string       `yaml:"ports"`
			ReadOnly    bool           `yaml:"read_only"`
			CapDrop     []string       `yaml:"cap_drop"`
			PidsLimit   int            `yaml:"pids_limit"`
			Networks    []string       `yaml:"networks"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(contents, &document); err != nil {
		t.Fatal(err)
	}
	meta, ok := document.Services["meta-server"]
	if !ok {
		t.Fatal("separated deployment is missing meta-server")
	}
	if len(meta.Profiles) != 1 || meta.Profiles[0] != "meta" {
		t.Fatalf("meta-server must remain independently deployable: %#v", meta.Profiles)
	}
	if !meta.ReadOnly || len(meta.CapDrop) != 1 || meta.CapDrop[0] != "ALL" || meta.PidsLimit < 1 {
		t.Fatalf("meta-server hardening is incomplete: %#v", meta)
	}
	if len(meta.Ports) != 2 {
		t.Fatalf("meta-server must publish exactly two loopback origins: %#v", meta.Ports)
	}
	for _, port := range meta.Ports {
		if !strings.HasPrefix(port, "127.0.0.1:") {
			t.Fatalf("meta-server origin is not loopback-only: %s", port)
		}
	}
	databaseURL, _ := meta.Environment["DATABASE_URL"].(string)
	if !strings.Contains(databaseURL, "META_POSTGRES") {
		t.Fatal("meta-server does not use the dedicated PostgreSQL role")
	}
	if _, ok := meta.Environment["RELAY_CA_KEY_PEM_BASE64"]; ok {
		t.Fatal("meta-server received the relay CA private key")
	}
	for _, secret := range []string{
		"ACCESS_TOKEN_PRIVATE_KEY_BASE64",
		"ADMIN_ACCESS_TOKEN_PRIVATE_KEY_BASE64",
		"ADMIN_MFA_ENCRYPTION_KEY_BASE64",
	} {
		if _, ok := meta.Environment[secret]; ok {
			t.Fatalf("meta-server received signing or MFA secret %s", secret)
		}
	}
	for _, verifier := range []string{
		"ACCESS_TOKEN_PUBLIC_KEY_BASE64",
		"ADMIN_ACCESS_TOKEN_PUBLIC_KEY_BASE64",
	} {
		if _, ok := meta.Environment[verifier]; !ok {
			t.Fatalf("meta-server is missing verifier %s", verifier)
		}
	}
}

func TestAdminWebImageUsesLockedBuildAndUnprivilegedRuntime(t *testing.T) {
	dockerfile, err := os.ReadFile("../../../AdminWeb/Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	contents := string(dockerfile)
	for _, requirement := range []string{
		"RUN npm ci",
		"RUN npm run build",
		"FROM scratch",
		"COPY --from=caddy-build /out/caddy /usr/bin/caddy",
		"USER 10001:10001",
		"EXPOSE 8080",
	} {
		if !strings.Contains(contents, requirement) {
			t.Fatalf("admin-web Dockerfile is missing %q", requirement)
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
