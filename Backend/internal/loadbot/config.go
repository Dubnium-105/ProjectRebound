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
	Auth              struct {
		InviteCode string `yaml:"invite_code"`
	} `yaml:"auth"`
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
	if cfg.RequestIntervalMS <= 0 {
		cfg.RequestIntervalMS = 1000
	}
	return cfg, nil
}
