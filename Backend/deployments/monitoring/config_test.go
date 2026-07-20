package monitoring_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestPrometheusScrapesOnlyInternalControlPlaneMetrics(t *testing.T) {
	contents, err := os.ReadFile("prometheus.yml")
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
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
	if len(config.ScrapeConfigs) != 2 || config.ScrapeConfigs[0].MetricsPath != "/internal/metrics" ||
		len(config.ScrapeConfigs[0].StaticConfigs) != 1 || len(config.ScrapeConfigs[0].StaticConfigs[0].Targets) != 1 ||
		config.ScrapeConfigs[0].StaticConfigs[0].Targets[0] != "control-plane:8080" {
		t.Fatalf("unexpected Prometheus scrape config: %#v", config)
	}
	if config.ScrapeConfigs[1].JobName != "project-rebound-edge-relay" ||
		len(config.ScrapeConfigs[1].FileSDConfigs) != 1 ||
		len(config.ScrapeConfigs[1].FileSDConfigs[0].Files) != 1 ||
		config.ScrapeConfigs[1].FileSDConfigs[0].Files[0] != "/etc/prometheus/targets/edge-relays.yml" {
		t.Fatalf("unexpected edge relay discovery config: %#v", config.ScrapeConfigs[1])
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
