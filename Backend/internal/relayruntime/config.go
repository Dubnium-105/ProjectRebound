package relayruntime

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Environment           string     `yaml:"environment"`
	DisplayName           string     `yaml:"display_name"`
	Region                string     `yaml:"region"`
	Zone                  string     `yaml:"zone"`
	Provider              string     `yaml:"provider"`
	SoftwareVersion       string     `yaml:"software_version"`
	ProtocolVersion       int        `yaml:"protocol_version"`
	AcceptProtocolV1      bool       `yaml:"accept_protocol_v1"`
	ControlPlaneURL       string     `yaml:"control_plane_url"`
	ControlAddr           string     `yaml:"control_addr"`
	ControlServerName     string     `yaml:"control_server_name"`
	BootstrapToken        string     `yaml:"-"`
	DataDir               string     `yaml:"data_dir"`
	ListenAddr            string     `yaml:"listen_addr"`
	MetricsAddr           string     `yaml:"metrics_addr"`
	AdvertisedEndpoints   []Endpoint `yaml:"advertised_endpoints"`
	SupportedProtocols    []string   `yaml:"supported_protocols"`
	MaxAllocations        int        `yaml:"max_allocations"`
	MaxEgressBPS          int64      `yaml:"max_egress_bps"`
	HeartbeatSeconds      int        `yaml:"heartbeat_seconds"`
	AllocationIdleSeconds int        `yaml:"allocation_idle_seconds"`
	CookieTTLSeconds      int        `yaml:"cookie_ttl_seconds"`
	MaxDatagramBytes      int        `yaml:"max_datagram_bytes"`
	MaxPayloadBytes       int        `yaml:"max_payload_bytes"`
	IPPacketsPerSecond    int        `yaml:"ip_packets_per_second"`
	NATRebindWindowSecs   int        `yaml:"nat_rebind_window_seconds"`
	MaxTokenReplayEntries int        `yaml:"max_token_replay_entries"`
}

var DefaultConfig = Config{
	Environment: "development", DisplayName: "edge-relay", Region: "local", Zone: "local-1", Provider: "local",
	SoftwareVersion: "0.1.0", ProtocolVersion: int(ProtocolVersion),
	ControlPlaneURL: "http://control-plane:8080", ControlAddr: "control-plane:9090", ControlServerName: "control-plane",
	DataDir: "./edge-relay-data", ListenAddr: ":8443", MetricsAddr: "127.0.0.1:9100",
	SupportedProtocols: []string{"UDP"}, MaxAllocations: 1000, MaxEgressBPS: 200_000_000,
	HeartbeatSeconds: 15, AllocationIdleSeconds: 120, CookieTTLSeconds: 10,
	MaxDatagramBytes: 1280, MaxPayloadBytes: 1200, IPPacketsPerSecond: 300,
	NATRebindWindowSecs: 30, MaxTokenReplayEntries: 4000,
}

func LoadConfig(path string) (Config, error) {
	cfg := DefaultConfig
	if strings.TrimSpace(path) != "" {
		contents, err := os.ReadFile(path)
		if err != nil {
			return Config{}, fmt.Errorf("read edge relay config: %w", err)
		}
		if err := yaml.Unmarshal(contents, &cfg); err != nil {
			return Config{}, fmt.Errorf("parse edge relay config: %w", err)
		}
	}
	applyEdgeEnv(&cfg)
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	absoluteDataDir, err := filepath.Abs(cfg.DataDir)
	if err != nil {
		return Config{}, fmt.Errorf("resolve relay data directory: %w", err)
	}
	cfg.DataDir = absoluteDataDir
	return cfg, nil
}

func applyEdgeEnv(cfg *Config) {
	override := func(name string, target *string) {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			*target = value
		}
	}
	override("EDGE_RELAY_ENVIRONMENT", &cfg.Environment)
	override("EDGE_RELAY_CONTROL_PLANE_URL", &cfg.ControlPlaneURL)
	override("EDGE_RELAY_CONTROL_ADDR", &cfg.ControlAddr)
	override("EDGE_RELAY_CONTROL_SERVER_NAME", &cfg.ControlServerName)
	override("EDGE_RELAY_BOOTSTRAP_TOKEN", &cfg.BootstrapToken)
	override("EDGE_RELAY_DATA_DIR", &cfg.DataDir)
	override("EDGE_RELAY_LISTEN_ADDR", &cfg.ListenAddr)
	override("EDGE_RELAY_METRICS_ADDR", &cfg.MetricsAddr)
	override("EDGE_RELAY_REGION", &cfg.Region)
	override("EDGE_RELAY_ZONE", &cfg.Zone)
	override("EDGE_RELAY_PROVIDER", &cfg.Provider)
	if value, err := strconv.Atoi(os.Getenv("EDGE_RELAY_MAX_ALLOCATIONS")); err == nil && value > 0 {
		cfg.MaxAllocations = value
	}
	if value, err := strconv.ParseInt(os.Getenv("EDGE_RELAY_MAX_EGRESS_BPS"), 10, 64); err == nil && value > 0 {
		cfg.MaxEgressBPS = value
	}
	if value, err := strconv.ParseBool(os.Getenv("EDGE_RELAY_ACCEPT_PROTOCOL_V1")); err == nil {
		cfg.AcceptProtocolV1 = value
	}
	if value, err := strconv.Atoi(os.Getenv("EDGE_RELAY_MAX_PAYLOAD_BYTES")); err == nil && value > 0 {
		cfg.MaxPayloadBytes = value
	}
	if value, err := strconv.Atoi(os.Getenv("EDGE_RELAY_NAT_REBIND_WINDOW_SECONDS")); err == nil && value > 0 {
		cfg.NATRebindWindowSecs = value
	}
	if value, err := strconv.Atoi(os.Getenv("EDGE_RELAY_MAX_TOKEN_REPLAY_ENTRIES")); err == nil && value > 0 {
		cfg.MaxTokenReplayEntries = value
	}
}

func (c Config) Validate() error {
	var errs []error
	controlURL, err := url.Parse(c.ControlPlaneURL)
	if err != nil || controlURL.Host == "" || (controlURL.Scheme != "http" && controlURL.Scheme != "https") {
		errs = append(errs, errors.New("control_plane_url must be an absolute HTTP(S) URL"))
	}
	if strings.EqualFold(c.Environment, "production") && controlURL != nil && controlURL.Scheme != "https" {
		errs = append(errs, errors.New("control_plane_url must use HTTPS in production"))
	}
	for name, value := range map[string]string{
		"display_name": c.DisplayName, "region": c.Region, "zone": c.Zone, "provider": c.Provider,
		"software_version": c.SoftwareVersion, "control_addr": c.ControlAddr, "control_server_name": c.ControlServerName,
		"data_dir": c.DataDir, "listen_addr": c.ListenAddr, "metrics_addr": c.MetricsAddr,
	} {
		if strings.TrimSpace(value) == "" {
			errs = append(errs, fmt.Errorf("%s is required", name))
		}
	}
	if c.ProtocolVersion != int(ProtocolVersion) || c.MaxAllocations < 1 || c.MaxEgressBPS < 1 ||
		c.HeartbeatSeconds < 1 || c.AllocationIdleSeconds < 1 || c.CookieTTLSeconds < 5 || c.CookieTTLSeconds > 15 ||
		c.MaxPayloadBytes < 1000 || c.MaxPayloadBytes > 1350 ||
		c.MaxDatagramBytes < DataHeaderSize+c.MaxPayloadBytes || c.MaxDatagramBytes > 65507 || c.IPPacketsPerSecond < 1 ||
		c.NATRebindWindowSecs < 1 || c.NATRebindWindowSecs > 300 || c.MaxTokenReplayEntries < c.MaxAllocations*2 {
		errs = append(errs, errors.New("relay protocol, capacity, timeout, or datagram settings are invalid"))
	}
	if len(c.AdvertisedEndpoints) == 0 {
		errs = append(errs, errors.New("at least one advertised endpoint is required"))
	}
	if host, _, err := net.SplitHostPort(c.MetricsAddr); err != nil || (host != "127.0.0.1" && host != "::1" && !strings.EqualFold(host, "localhost")) {
		errs = append(errs, errors.New("metrics_addr must listen on loopback"))
	}
	if len(errs) > 0 {
		return fmt.Errorf("invalid edge relay configuration: %w", errors.Join(errs...))
	}
	return nil
}

func (c Config) HeartbeatInterval() time.Duration {
	return time.Duration(c.HeartbeatSeconds) * time.Second
}
func (c Config) AllocationIdleTTL() time.Duration {
	return time.Duration(c.AllocationIdleSeconds) * time.Second
}
func (c Config) CookieTTL() time.Duration { return time.Duration(c.CookieTTLSeconds) * time.Second }
func (c Config) NATRebindWindow() time.Duration {
	return time.Duration(c.NATRebindWindowSecs) * time.Second
}

type Endpoint struct {
	Protocol string `yaml:"protocol" json:"protocol"`
	Host     string `yaml:"host" json:"host"`
	Port     int    `yaml:"port" json:"port"`
}
