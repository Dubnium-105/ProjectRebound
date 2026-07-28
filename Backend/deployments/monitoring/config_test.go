package monitoring_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestPrometheusScrapesInternalControlPlaneAndMetaMetrics(t *testing.T) {
	contents, err := os.ReadFile("prometheus.yml")
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		RuleFiles     []string `yaml:"rule_files"`
		ScrapeConfigs []struct {
			MetricsPath   string `yaml:"metrics_path"`
			JobName       string `yaml:"job_name"`
			StaticConfigs []struct {
				Targets []string `yaml:"targets"`
			} `yaml:"static_configs"`
			FileSDConfigs []struct {
				Files []string `yaml:"files"`
			} `yaml:"file_sd_configs"`
		} `yaml:"scrape_configs"`
	}
	if err := yaml.Unmarshal(contents, &config); err != nil {
		t.Fatal(err)
	}
	if len(config.ScrapeConfigs) != 5 || config.ScrapeConfigs[0].MetricsPath != "/internal/metrics" ||
		len(config.ScrapeConfigs[0].StaticConfigs) != 1 || len(config.ScrapeConfigs[0].StaticConfigs[0].Targets) != 1 ||
		config.ScrapeConfigs[0].StaticConfigs[0].Targets[0] != "control-plane:8080" {
		t.Fatalf("unexpected Prometheus scrape config: %#v", config)
	}
	if len(config.RuleFiles) != 1 || config.RuleFiles[0] != "/etc/prometheus/alerts/*.yml" {
		t.Fatalf("unexpected Prometheus rule files: %#v", config.RuleFiles)
	}
	if config.ScrapeConfigs[1].JobName != "project-rebound-meta-server" ||
		config.ScrapeConfigs[1].MetricsPath != "/internal/metrics" ||
		len(config.ScrapeConfigs[1].StaticConfigs) != 1 ||
		config.ScrapeConfigs[1].StaticConfigs[0].Targets[0] != "meta-server:8081" {
		t.Fatalf("unexpected MetaServer scrape config: %#v", config.ScrapeConfigs[1])
	}
	if config.ScrapeConfigs[2].JobName != "project-rebound-edge-relay" ||
		len(config.ScrapeConfigs[2].FileSDConfigs) != 1 ||
		len(config.ScrapeConfigs[2].FileSDConfigs[0].Files) != 1 ||
		config.ScrapeConfigs[2].FileSDConfigs[0].Files[0] != "/etc/prometheus/targets/edge-relays.yml" {
		t.Fatalf("unexpected edge relay discovery config: %#v", config.ScrapeConfigs[2])
	}
	if config.ScrapeConfigs[3].JobName != "project-rebound-meta-public" ||
		config.ScrapeConfigs[3].MetricsPath != "/probe" ||
		config.ScrapeConfigs[3].StaticConfigs[0].Targets[0] != "https://meta.dubnium.top/health/ready" {
		t.Fatalf("unexpected public MetaServer probe config: %#v", config.ScrapeConfigs[3])
	}
	if config.ScrapeConfigs[4].JobName != "project-rebound-logic-public" ||
		config.ScrapeConfigs[4].MetricsPath != "/probe" ||
		config.ScrapeConfigs[4].StaticConfigs[0].Targets[0] != "logic.dubnium.top:443" {
		t.Fatalf("unexpected public Logic probe config: %#v", config.ScrapeConfigs[4])
	}
}

func TestV11AlertRulesCoverRequiredFailureModes(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join("alerts", "v1.1.rules.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var rules struct {
		Groups []struct {
			Rules []struct {
				Alert string `yaml:"alert"`
				Expr  string `yaml:"expr"`
			} `yaml:"rules"`
		} `yaml:"groups"`
	}
	if err := yaml.Unmarshal(contents, &rules); err != nil {
		t.Fatal(err)
	}
	alerts := make(map[string]string)
	for _, group := range rules.Groups {
		for _, rule := range group.Rules {
			if rule.Alert == "" || rule.Expr == "" {
				t.Fatalf("alert name and expression are required: %#v", rule)
			}
			alerts[rule.Alert] = rule.Expr
		}
	}
	for _, name := range []string{
		"ControlPlaneAPIHighErrorRate", "ControlPlaneAPIHighP95Latency", "PostgreSQLUnavailable",
		"DatabasePoolNearlyExhausted", "RedisUnavailable", "BackgroundJobsRepeatedlyFailing", "HostFilesystemLow",
		"AuthBindRateLimitSpike", "RefreshTokenReplayDetected", "MultiAccountRiskSpike", "InviteCodeFailureSpike",
		"RelayNodeOffline", "NoRelayAvailable", "RelayRegionCapacityHigh", "RelayInvalidTokenSpike",
		"RelayTokenReplayDetected", "RelayBindFailureRateHigh", "RelayMemoryGrowing", "RelayMigrationFailureRateHigh",
		"DailyBackupMissing", "BackupVerificationFailed", "RestoreDrillOverdue",
		"MetaServerDown", "MetaGateTicketReplaySpike", "MetaMalformedFrameSpike",
		"MetaHTTPFRPTunnelDown", "MetaLogicFRPTunnelDown",
		"MetaMatchQueueBacklog", "MetaLogicTLSExpiring",
	} {
		if _, ok := alerts[name]; !ok {
			t.Errorf("required alert %s is missing", name)
		}
	}
}

func TestV11DashboardSetIsVersionedAndValid(t *testing.T) {
	expected := map[string]bool{
		"Control Plane Overview": false, "Authentication and Session Security": false,
		"P2P Rooms and Connections": false, "Relay Fleet Overview": false,
		"Relay Security": false, "Relay Traffic and Capacity": false,
		"Database and Redis": false, "Release and Update Status": false,
	}
	files, err := filepath.Glob(filepath.Join("grafana", "dashboards", "v1.1-*.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		contents, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var dashboard struct {
			UID    string `json:"uid"`
			Title  string `json:"title"`
			Panels []any  `json:"panels"`
		}
		if err := json.Unmarshal(contents, &dashboard); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		if dashboard.UID == "" || len(dashboard.Panels) == 0 {
			t.Errorf("dashboard %s needs a stable UID and panels", file)
		}
		if _, ok := expected[dashboard.Title]; ok {
			expected[dashboard.Title] = true
		}
	}
	for title, found := range expected {
		if !found {
			t.Errorf("required V1.1 dashboard %q is missing", title)
		}
	}
}

func TestGrafanaDashboardIsValidJSONAndUsesRequiredMetrics(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join("grafana", "dashboards", "control-plane.json"))
	if err != nil {
		t.Fatal(err)
	}
	var dashboard map[string]any
	if err := json.Unmarshal(contents, &dashboard); err != nil {
		t.Fatal(err)
	}
	for _, metric := range []string{"http_requests_total", "http_request_duration_seconds_bucket", "active_sessions", "relay_allocations_active", "refresh_token_reuse_total", "relay_node_control_connected", "relay_node_packets_forwarded_total", "relay_node_state", "node_cpu_seconds_total"} {
		if !strings.Contains(string(contents), metric) {
			t.Fatalf("dashboard does not query %s", metric)
		}
	}
}

func TestGrafanaDashboardRepeatsDynamicServiceAndRelayCards(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join("grafana", "dashboards", "control-plane.json"))
	if err != nil {
		t.Fatal(err)
	}
	var dashboard struct {
		Panels []struct {
			ID              int    `json:"id"`
			Title           string `json:"title"`
			Repeat          string `json:"repeat"`
			RepeatDirection string `json:"repeatDirection"`
			MaxPerRow       int    `json:"maxPerRow"`
		} `json:"panels"`
		Templating struct {
			List []struct {
				Name       string `json:"name"`
				Definition string `json:"definition"`
				Multi      bool   `json:"multi"`
				IncludeAll bool   `json:"includeAll"`
			} `json:"list"`
		} `json:"templating"`
	}
	if err := json.Unmarshal(contents, &dashboard); err != nil {
		t.Fatal(err)
	}
	variables := make(map[string]struct {
		Definition string
		Multi      bool
		IncludeAll bool
	}, len(dashboard.Templating.List))
	for _, variable := range dashboard.Templating.List {
		variables[variable.Name] = struct {
			Definition string
			Multi      bool
			IncludeAll bool
		}{variable.Definition, variable.Multi, variable.IncludeAll}
	}
	onlineRelay, ok := variables["online_relay"]
	if !ok || !strings.Contains(onlineRelay.Definition, "relay_node_control_connected == 1") ||
		!onlineRelay.Multi || !onlineRelay.IncludeAll {
		t.Fatalf("online relay variable is not configured for dynamic expansion: %#v", dashboard.Templating.List)
	}
	serviceTarget, ok := variables["service_target"]
	if !ok || !strings.Contains(serviceTarget.Definition, `up{job="project-rebound-control-plane"}`) ||
		!strings.Contains(serviceTarget.Definition, "project-rebound-meta-server") ||
		!strings.Contains(serviceTarget.Definition, "relay_node_control_connected == 1") ||
		!serviceTarget.Multi || !serviceTarget.IncludeAll {
		t.Fatalf("service target variable is not configured for dynamic expansion: %#v", dashboard.Templating.List)
	}
	seenPanelIDs := make(map[int]bool, len(dashboard.Panels))
	foundRepeatPanels := make(map[string]bool, 2)
	for _, panel := range dashboard.Panels {
		if panel.ID <= 0 || seenPanelIDs[panel.ID] {
			t.Fatalf("dashboard panel IDs must be stable and unique: %#v", panel)
		}
		seenPanelIDs[panel.ID] = true
		if panel.Repeat == "online_relay" || panel.Repeat == "service_target" {
			if panel.RepeatDirection != "h" || panel.MaxPerRow != 3 {
				t.Fatalf("unexpected repeated relay card layout: %#v", panel)
			}
			foundRepeatPanels[panel.Repeat] = true
		}
	}
	if !foundRepeatPanels["online_relay"] || !foundRepeatPanels["service_target"] {
		t.Fatalf("dashboard is missing a dynamically repeated panel: %#v", foundRepeatPanels)
	}
}

func TestPublicProxyBlocksInternalMetrics(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join("..", "caddy", "Caddyfile"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	if !strings.Contains(text, "handle /internal/*") || !strings.Contains(text, "respond 404") {
		t.Fatal("Caddy does not block internal endpoints")
	}
}

func TestPublicMetaGatewayBlocksPrivateRoutes(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join("..", "public-http-gateway", "haproxy.cfg.example"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	if !strings.Contains(text, "acl private_meta_path path_beg /internal/ /v1/admin") ||
		!strings.Contains(text, "http-request deny deny_status 404 if private_meta_path") ||
		!strings.Contains(text, "ssl-default-bind-options ssl-min-ver TLSv1.2") ||
		!strings.Contains(text, "tcp-request content reject if sni_meta !from_cloudflare") ||
		!strings.Contains(text, "use_backend meta_logic_tls if sni_logic") ||
		!strings.Contains(text, "127.0.0.1:10444 send-proxy") ||
		!strings.Contains(text, "127.0.0.1:10444 accept-proxy ssl") ||
		!strings.Contains(text, "127.0.0.1:16969 send-proxy") {
		t.Fatal("public Meta HAProxy frontend is missing route isolation, TLS policy, or Cloudflare source policy")
	}
}

func TestComposeMountsPrometheusAlertRules(t *testing.T) {
	for _, file := range []string{
		filepath.Join("..", "control-plane", "docker-compose.yaml"),
		filepath.Join("..", "compose", "docker-compose.yaml"),
	} {
		contents, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var compose map[string]any
		if err := yaml.Unmarshal(contents, &compose); err != nil {
			t.Fatalf("%s is invalid YAML: %v", file, err)
		}
		if !strings.Contains(string(contents), "../monitoring/alerts:/etc/prometheus/alerts:ro") {
			t.Errorf("%s does not mount the alert rules", file)
		}
	}
}
