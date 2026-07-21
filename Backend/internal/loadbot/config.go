package loadbot

import (
	"errors"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Scenario          string `yaml:"scenario"`
	ControlPlaneURL   string `yaml:"control_plane_url"`
	RealtimeURL       string `yaml:"realtime_url"`
	Clients           int    `yaml:"clients"`
	Rooms             int    `yaml:"rooms"`
	RelayConnections  int    `yaml:"relay_connections"`
	Duration          string `yaml:"duration"`
	RequestIntervalMS int    `yaml:"request_interval_ms"`
	SetupConcurrency  int    `yaml:"setup_concurrency"`
	Auth              struct {
		InviteCode      string `yaml:"invite_code"`
		RefreshInterval string `yaml:"refresh_interval"`
	} `yaml:"auth"`
	Room struct {
		Region  string `yaml:"region"`
		Mode    string `yaml:"mode"`
		Version string `yaml:"version"`
	} `yaml:"room"`
	Traffic struct {
		PacketsPerSecond int `yaml:"packets_per_second"`
		PayloadBytes     int `yaml:"payload_bytes"`
		JitterMS         int `yaml:"jitter_ms"`
	} `yaml:"traffic"`
	FailureInjection struct {
		DisconnectPercent     int `yaml:"disconnect_percent"`
		ReconnectDelaySeconds int `yaml:"reconnect_delay_seconds"`
	} `yaml:"failure_injection"`
}

func LoadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}
	if cfg.Clients < 1 || cfg.Clients > 10000 || cfg.ControlPlaneURL == "" || cfg.Duration == "" {
		return Config{}, errors.New("control_plane_url, duration, and 1..10000 clients are required")
	}
	if _, err := time.ParseDuration(cfg.Duration); err != nil {
		return Config{}, errors.New("duration must be a Go duration such as 1h or 30m")
	}
	switch cfg.Scenario {
	case "", "basic", "auth", "auth-bind", "p2p", "relay", "full", "soak":
	default:
		return Config{}, errors.New("scenario must be basic, auth-bind, p2p, relay, full, or soak")
	}
	if cfg.RequestIntervalMS <= 0 {
		cfg.RequestIntervalMS = 1000
	}
	if cfg.SetupConcurrency <= 0 {
		cfg.SetupConcurrency = 10
	}
	if cfg.SetupConcurrency > 100 {
		return Config{}, errors.New("setup_concurrency must be at most 100")
	}
	if cfg.Rooms < 0 || cfg.Rooms > cfg.Clients/2 || cfg.RelayConnections < 0 || cfg.RelayConnections > cfg.Rooms {
		return Config{}, errors.New("rooms must be at most clients/2 and relay_connections must be at most rooms")
	}
	if cfg.Room.Region == "" {
		cfg.Room.Region = "test"
	}
	if cfg.Room.Mode == "" {
		cfg.Room.Mode = "load"
	}
	if cfg.Room.Version == "" {
		cfg.Room.Version = "1.1.0"
	}
	if cfg.Auth.RefreshInterval == "" {
		cfg.Auth.RefreshInterval = "10m"
	}
	if interval, err := time.ParseDuration(cfg.Auth.RefreshInterval); err != nil || interval <= 0 {
		return Config{}, errors.New("auth.refresh_interval must be a Go duration")
	}
	if cfg.Traffic.PacketsPerSecond < 0 || cfg.Traffic.PacketsPerSecond > 10000 || cfg.Traffic.PayloadBytes < 0 || cfg.Traffic.PayloadBytes > 1200 {
		return Config{}, errors.New("traffic packets_per_second must be 0..10000 and payload_bytes must be 0..1200")
	}
	if cfg.Traffic.JitterMS < 0 || cfg.Traffic.JitterMS > 60000 || cfg.FailureInjection.DisconnectPercent < 0 || cfg.FailureInjection.DisconnectPercent > 100 || cfg.FailureInjection.ReconnectDelaySeconds < 0 {
		return Config{}, errors.New("traffic jitter and failure injection values are outside the accepted range")
	}
	if (cfg.Scenario == "p2p" || cfg.Scenario == "relay" || cfg.Scenario == "full" || cfg.Scenario == "soak") && cfg.Rooms == 0 {
		return Config{}, errors.New("end-to-end scenarios require rooms")
	}
	return cfg, nil
}
