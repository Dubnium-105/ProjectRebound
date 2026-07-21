package relayruntime

import "testing"

func TestExampleConfigLoads(t *testing.T) {
	for _, name := range []string{
		"EDGE_RELAY_ENVIRONMENT", "EDGE_RELAY_CONTROL_PLANE_URL", "EDGE_RELAY_CONTROL_ADDR",
		"EDGE_RELAY_CONTROL_SERVER_NAME", "EDGE_RELAY_BOOTSTRAP_TOKEN", "EDGE_RELAY_DATA_DIR",
		"EDGE_RELAY_LISTEN_ADDR", "EDGE_RELAY_METRICS_ADDR", "EDGE_RELAY_REGION",
		"EDGE_RELAY_ZONE", "EDGE_RELAY_PROVIDER", "EDGE_RELAY_MAX_ALLOCATIONS", "EDGE_RELAY_MAX_EGRESS_BPS",
		"EDGE_RELAY_ACCEPT_PROTOCOL_V1", "EDGE_RELAY_MAX_PAYLOAD_BYTES",
		"EDGE_RELAY_NAT_REBIND_WINDOW_SECONDS", "EDGE_RELAY_MAX_TOKEN_REPLAY_ENTRIES",
		"EDGE_RELAY_BIND_INIT_PER_SECOND", "EDGE_RELAY_BIND_PROOF_PER_SECOND",
		"EDGE_RELAY_INVALID_TOKENS_PER_MINUTE", "EDGE_RELAY_MAX_ALLOCATIONS_PER_IP",
		"EDGE_RELAY_MAX_IP_RATE_STATES", "EDGE_RELAY_MAX_INGRESS_PACKETS_PER_SECOND",
		"EDGE_RELAY_TEMPORARY_BAN_SECONDS",
		"EDGE_RELAY_MAX_INGRESS_MBPS", "EDGE_RELAY_MAX_MEMORY_MB",
		"EDGE_RELAY_DEGRADED_THRESHOLD_PERCENT", "EDGE_RELAY_REJECT_NEW_THRESHOLD_PERCENT",
		"EDGE_RELAY_CONTROL_DISCONNECT_GRACE_SECONDS",
	} {
		t.Setenv(name, "")
	}
	cfg, err := LoadConfig("../../config.edge-relay.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProtocolVersion != int(ProtocolVersion) || len(cfg.AdvertisedEndpoints) != 1 {
		t.Fatalf("example config = %#v", cfg)
	}
}

func TestProductionConfigRequiresHTTPSAndLoopbackMetrics(t *testing.T) {
	cfg := DefaultConfig
	cfg.Environment = "production"
	cfg.AdvertisedEndpoints = []Endpoint{{Protocol: "UDP", Host: "203.0.113.20", Port: 443}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("production relay accepted plaintext control-plane enrollment")
	}
	cfg.ControlPlaneURL = "https://control.example.com"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid production config: %v", err)
	}
	cfg.MetricsAddr = "0.0.0.0:9100"
	if err := cfg.Validate(); err == nil {
		t.Fatal("public metrics listener was accepted")
	}
}
