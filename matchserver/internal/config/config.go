package config

import (
	"os"
	"strconv"

	"gopkg.in/yaml.v3"
)

type Config struct {
	HTTPAddr          string             `yaml:"http_addr"`
	UDPRendezvousPort int                `yaml:"udp_rendezvous_port"`
	UDPRelayPort      int                `yaml:"udp_relay_port"`
	UDPQoSPort        int                `yaml:"udp_qos_port"`
	Database          DBConfig           `yaml:"database"`
	MatchServer       MatchServerConfig  `yaml:"matchserver"`
	Relay             RelayConfig        `yaml:"relay"`
	Logging           LogConfig          `yaml:"logging"`
}

type DBConfig struct {
	Path string `yaml:"path"`
}

type MatchServerConfig struct {
	HeartbeatSeconds              int `yaml:"heartbeat_seconds"`
	StaleAfterSeconds             int `yaml:"stale_after_seconds"`
	HostLostAfterSeconds          int `yaml:"host_lost_after_seconds"`
	HostProbeSeconds              int `yaml:"host_probe_seconds"`
	JoinTicketSeconds             int `yaml:"join_ticket_seconds"`
	MatchTicketSeconds            int `yaml:"match_ticket_seconds"`
	EndedRoomRetentionMinutes     int `yaml:"ended_room_retention_minutes"`
	NatBindingSeconds             int `yaml:"nat_binding_seconds"`
	PunchTicketSeconds            int `yaml:"punch_ticket_seconds"`
	RelayAllocationSeconds        int `yaml:"relay_allocation_seconds"`
	MetaserverTargetPlayerCount   int `yaml:"metaserver_target_player_count"`
	MetaserverMatchTimeoutSeconds int `yaml:"metaserver_match_timeout_seconds"`
}

type RelayConfig struct {
	Compression               string  `yaml:"compression"`
	ForceCompression          bool    `yaml:"force_compression"`
	CompressionLossThreshold  float64 `yaml:"compression_loss_threshold"`
	CompressionRTTThresholdMs int     `yaml:"compression_rtt_threshold_ms"`
}

type LogConfig struct {
	Level string `yaml:"level"`
}

var Defaults = Config{
	HTTPAddr:          ":5000",
	UDPRendezvousPort: 5001,
	UDPRelayPort:      5002,
	UDPQoSPort:        9000,
	Database: DBConfig{
		Path: "matchserver.db",
	},
	MatchServer: MatchServerConfig{
		HeartbeatSeconds:              5,
		StaleAfterSeconds:             15,
		HostLostAfterSeconds:          45,
		HostProbeSeconds:              60,
		JoinTicketSeconds:             90,
		MatchTicketSeconds:            120,
		EndedRoomRetentionMinutes:     30,
		NatBindingSeconds:             120,
		PunchTicketSeconds:            120,
		RelayAllocationSeconds:        1800,
		MetaserverTargetPlayerCount:   12,
		MetaserverMatchTimeoutSeconds: 300,
	},
	Relay: RelayConfig{
		Compression:               "auto",
		ForceCompression:          false,
		CompressionLossThreshold:  0.05,
		CompressionRTTThresholdMs: 200,
	},
	Logging: LogConfig{
		Level: "info",
	},
}

func Load(path string) (*Config, error) {
	cfg := Defaults
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &cfg, nil
		}
		return nil, err
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	cfg.applyEnvOverrides()
	return &cfg, nil
}

func (c *Config) applyEnvOverrides() {
	if v := os.Getenv("MATCHSERVER_HEARTBEAT_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.MatchServer.HeartbeatSeconds = n
		}
	}
	if v := os.Getenv("MATCHSERVER_HOST_LOST_AFTER_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.MatchServer.HostLostAfterSeconds = n
		}
	}
	if v := os.Getenv("MATCHSERVER_RELAY_ALLOCATION_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.MatchServer.RelayAllocationSeconds = n
		}
	}
	if v := os.Getenv("MATCHSERVER_DATABASE_PATH"); v != "" {
		c.Database.Path = v
	}
}
