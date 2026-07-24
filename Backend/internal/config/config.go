package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Environment       string              `yaml:"environment"`
	HTTPAddr          string              `yaml:"http_addr"`
	HTTP              HTTPConfig          `yaml:"http"`
	UDPRendezvousPort int                 `yaml:"udp_rendezvous_port"`
	UDPRelayPort      int                 `yaml:"udp_relay_port"`
	UDPQoSPort        int                 `yaml:"udp_qos_port"`
	Database          DBConfig            `yaml:"database"`
	Redis             RedisConfig         `yaml:"redis"`
	CORS              CORSConfig          `yaml:"cors"`
	RateLimit         RateLimitConfig     `yaml:"rate_limit"`
	Auth              AuthConfig          `yaml:"auth"`
	Admin             AdminConfig         `yaml:"admin"`
	GameServer        GameServerConfig    `yaml:"game_server"`
	P2PRoom           P2PRoomConfig       `yaml:"p2p_room"`
	Connection        ConnectionConfig    `yaml:"connection"`
	RelayRegistry     RelayRegistryConfig `yaml:"relay_registry"`
	Update            UpdateConfig        `yaml:"update"`
	MatchServer       MatchServerConfig   `yaml:"matchserver"`
	Relay             RelayConfig         `yaml:"relay"`
	Logging           LogConfig           `yaml:"logging"`
}

type HTTPConfig struct {
	Addr                  string `yaml:"addr"`
	ReadHeaderTimeoutSecs int    `yaml:"read_header_timeout_seconds"`
	ReadTimeoutSecs       int    `yaml:"read_timeout_seconds"`
	WriteTimeoutSecs      int    `yaml:"write_timeout_seconds"`
	IdleTimeoutSecs       int    `yaml:"idle_timeout_seconds"`
	ShutdownTimeoutSecs   int    `yaml:"shutdown_timeout_seconds"`
	MaxRequestBodyBytes   int64  `yaml:"max_request_body_bytes"`
	TrustProxyHeaders     bool   `yaml:"trust_proxy_headers"`
}

type DBConfig struct {
	// Path is retained only for the legacy SQLite server during the migration.
	Path              string `yaml:"path"`
	URL               string `yaml:"url"`
	MaxConnections    int32  `yaml:"max_connections"`
	MinConnections    int32  `yaml:"min_connections"`
	MaxConnectionMins int    `yaml:"max_connection_lifetime_minutes"`
	HealthTimeoutSecs int    `yaml:"health_timeout_seconds"`
}

type RedisConfig struct {
	Address              string `yaml:"address"`
	Username             string `yaml:"username"`
	Password             string `yaml:"password"`
	DB                   int    `yaml:"db"`
	PoolSize             int    `yaml:"pool_size"`
	ConnectTimeoutSecs   int    `yaml:"connect_timeout_seconds"`
	OperationTimeoutSecs int    `yaml:"operation_timeout_seconds"`
}

type CORSConfig struct {
	AllowedOrigins   []string `yaml:"allowed_origins"`
	AllowedHeaders   []string `yaml:"allowed_headers"`
	AllowCredentials bool     `yaml:"allow_credentials"`
	MaxAgeSeconds    int      `yaml:"max_age_seconds"`
}

type RateLimitConfig struct {
	RequestsPerSecond float64 `yaml:"requests_per_second"`
	Burst             int     `yaml:"burst"`
}

type AuthConfig struct {
	Issuer                         string                  `yaml:"issuer"`
	Audience                       string                  `yaml:"audience"`
	AccessTokenKeyID               string                  `yaml:"access_token_key_id"`
	AccessTokenPrivateKeyBase64    string                  `yaml:"access_token_private_key_base64"`
	AccessTokenTTLMinutes          int                     `yaml:"access_token_ttl_minutes"`
	RefreshTokenTTLDays            int                     `yaml:"refresh_token_ttl_days"`
	DefaultPersonaName             string                  `yaml:"default_persona_name"`
	InviteRequired                 bool                    `yaml:"invite_required"`
	DeviceFingerprintKeyID         string                  `yaml:"device_fingerprint_key_id"`
	DeviceFingerprintHMACKeyBase64 string                  `yaml:"-"`
	BindRateLimit                  AuthBindRateLimitConfig `yaml:"bind_rate_limit"`
	// Deprecated: retained so existing configuration files continue to load.
	BindRequestsPerMinute int `yaml:"bind_requests_per_minute"`
	BindBurst             int `yaml:"bind_burst"`
}

type AuthBindRateLimitConfig struct {
	PerIPPerMinute      int `yaml:"per_ip_per_minute"`
	PerDevicePerMinute  int `yaml:"per_device_per_minute"`
	PerSteamIDPerMinute int `yaml:"per_steam_id_per_minute"`
}

type AdminConfig struct {
	// TokenSet is loaded only from ADMIN_TOKENS and is deliberately excluded
	// from YAML so credentials cannot be committed in configuration files.
	TokenSet                    string   `yaml:"-"`
	AccessTokenPrivateKeyBase64 string   `yaml:"-"`
	MFAEncryptionKeyBase64      string   `yaml:"-"`
	TurnstileSecretKey          string   `yaml:"-"`
	TrustedCIDRs                []string `yaml:"trusted_cidrs"`
	Issuer                      string   `yaml:"issuer"`
	Audience                    string   `yaml:"audience"`
	AccessTokenKeyID            string   `yaml:"access_token_key_id"`
	AccessTokenTTLMinutes       int      `yaml:"access_token_ttl_minutes"`
	RefreshTokenTTLDays         int      `yaml:"refresh_token_ttl_days"`
	LoginChallengeTTLSeconds    int      `yaml:"login_challenge_ttl_seconds"`
	StepUpTTLSeconds            int      `yaml:"step_up_ttl_seconds"`
	LoginPerIPPerMinute         int      `yaml:"login_per_ip_per_minute"`
	LoginPerUsernamePerMinute   int      `yaml:"login_per_username_per_minute"`
	TurnstileSiteKey            string   `yaml:"turnstile_site_key"`
	TurnstileExpectedHostname   string   `yaml:"turnstile_expected_hostname"`
	TurnstileExpectedAction     string   `yaml:"turnstile_expected_action"`
	TurnstileVerifyURL          string   `yaml:"turnstile_verify_url"`
	TurnstileTimeoutSeconds     int      `yaml:"turnstile_timeout_seconds"`
}

type GameServerConfig struct {
	RegistrationTokenSet     string `yaml:"-"`
	HeartbeatIntervalSeconds int    `yaml:"heartbeat_interval_seconds"`
	UnhealthyAfterSeconds    int    `yaml:"unhealthy_after_seconds"`
	OfflineAfterSeconds      int    `yaml:"offline_after_seconds"`
	ServerTokenTTLHours      int    `yaml:"server_token_ttl_hours"`
	SweepIntervalSeconds     int    `yaml:"sweep_interval_seconds"`
}

type P2PRoomConfig struct {
	HeartbeatIntervalSeconds int `yaml:"heartbeat_interval_seconds"`
	StaleAfterSeconds        int `yaml:"stale_after_seconds"`
	ClosedAfterSeconds       int `yaml:"closed_after_seconds"`
	SweepIntervalSeconds     int `yaml:"sweep_interval_seconds"`
	MaximumPlayers           int `yaml:"maximum_players"`
}

type ConnectionConfig struct {
	SessionTTLSeconds    int `yaml:"session_ttl_seconds"`
	SweepIntervalSeconds int `yaml:"sweep_interval_seconds"`
	WebSocketQueueSize   int `yaml:"websocket_queue_size"`
	WebSocketMaxBytes    int `yaml:"websocket_max_message_bytes"`
}

type RelayRegistryConfig struct {
	ControlAddr                string   `yaml:"control_addr"`
	ServerNames                []string `yaml:"server_names"`
	BootstrapTokenSet          string   `yaml:"-"`
	HeartbeatIntervalSeconds   int      `yaml:"heartbeat_interval_seconds"`
	UnhealthyAfterSeconds      int      `yaml:"unhealthy_after_seconds"`
	OfflineAfterSeconds        int      `yaml:"offline_after_seconds"`
	SweepIntervalSeconds       int      `yaml:"sweep_interval_seconds"`
	DrainDeadlineSeconds       int      `yaml:"drain_deadline_seconds"`
	MigrationTimeoutSeconds    int      `yaml:"migration_timeout_seconds"`
	MigrationMaxAttempts       int      `yaml:"migration_max_attempts"`
	CertificateTTLHours        int      `yaml:"certificate_ttl_hours"`
	CACertificatePEMBase64     string   `yaml:"-"`
	CAPrivateKeyPEMBase64      string   `yaml:"-"`
	RelayTokenKeyID            string   `yaml:"relay_token_key_id"`
	RelayTokenPrivateKeyBase64 string   `yaml:"-"`
	RelayTokenRotationKeys     string   `yaml:"-"`
	RelayTokenTTLSeconds       int      `yaml:"relay_token_ttl_seconds"`
	AllocationTTLSeconds       int      `yaml:"allocation_ttl_seconds"`
	CapacityThresholdPercent   int      `yaml:"capacity_threshold_percent"`
}

type UpdateConfig struct {
	Product                 string   `yaml:"product"`
	ManifestDirectory       string   `yaml:"manifest_directory"`
	CDNBaseURL              string   `yaml:"cdn_base_url"`
	SigningKeyID            string   `yaml:"signing_key_id"`
	SigningPrivateKeyBase64 string   `yaml:"-"`
	DefaultChannel          string   `yaml:"default_channel"`
	DefaultArchitecture     string   `yaml:"default_architecture"`
	MinimumClientVersion    string   `yaml:"minimum_client_version"`
	RealtimeURL             string   `yaml:"realtime_url"`
	STUNServers             []string `yaml:"stun_servers"`
	APIVersion              string   `yaml:"api_version"`
	ProtocolVersion         int      `yaml:"protocol_version"`
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
	Level     string `yaml:"level"`
	AddSource bool   `yaml:"add_source"`
}

var Defaults = Config{
	Environment: "development",
	HTTPAddr:    ":5000",
	HTTP: HTTPConfig{
		Addr:                  ":8080",
		ReadHeaderTimeoutSecs: 5,
		ReadTimeoutSecs:       15,
		WriteTimeoutSecs:      30,
		IdleTimeoutSecs:       60,
		ShutdownTimeoutSecs:   10,
		MaxRequestBodyBytes:   1 << 20,
	},
	UDPRendezvousPort: 5001,
	UDPRelayPort:      5002,
	UDPQoSPort:        9000,
	Database: DBConfig{
		Path:              "matchserver.db",
		URL:               "postgres://projectrebound:projectrebound_dev@127.0.0.1:5432/projectrebound?sslmode=disable",
		MaxConnections:    20,
		MinConnections:    2,
		MaxConnectionMins: 30,
		HealthTimeoutSecs: 2,
	},
	Redis: RedisConfig{
		Address:              "127.0.0.1:6379",
		PoolSize:             20,
		ConnectTimeoutSecs:   3,
		OperationTimeoutSecs: 2,
	},
	CORS: CORSConfig{
		AllowedOrigins: []string{
			"http://localhost",
			"http://127.0.0.1",
			"http://localhost:5173",
			"http://127.0.0.1:5173",
		},
		AllowedHeaders:   []string{"Authorization", "Content-Type", "X-Request-Id", "X-Room-Host-Token"},
		AllowCredentials: true,
		MaxAgeSeconds:    600,
	},
	RateLimit: RateLimitConfig{
		RequestsPerSecond: 25,
		Burst:             50,
	},
	Auth: AuthConfig{
		Issuer:                 "game-control-plane",
		Audience:               "game-client",
		AccessTokenKeyID:       "access-dev-ephemeral",
		AccessTokenTTLMinutes:  15,
		RefreshTokenTTLDays:    30,
		DefaultPersonaName:     "Player",
		DeviceFingerprintKeyID: "device-fingerprint-v1",
		BindRateLimit: AuthBindRateLimitConfig{
			PerIPPerMinute:      5,
			PerDevicePerMinute:  3,
			PerSteamIDPerMinute: 3,
		},
		BindRequestsPerMinute: 10,
		BindBurst:             5,
	},
	Admin: AdminConfig{
		Issuer:                    "game-control-plane",
		Audience:                  "admin-web",
		AccessTokenKeyID:          "admin-access-dev-ephemeral",
		AccessTokenTTLMinutes:     15,
		RefreshTokenTTLDays:       7,
		LoginChallengeTTLSeconds:  300,
		StepUpTTLSeconds:          300,
		LoginPerIPPerMinute:       10,
		LoginPerUsernamePerMinute: 5,
		TurnstileExpectedHostname: "admin.example.com",
		TurnstileExpectedAction:   "admin_login",
		TurnstileVerifyURL:        "https://challenges.cloudflare.com/turnstile/v0/siteverify",
		TurnstileTimeoutSeconds:   5,
		TrustedCIDRs: []string{
			"127.0.0.0/8",
			"10.0.0.0/8",
			"172.16.0.0/12",
			"192.168.0.0/16",
			"::1/128",
			"fc00::/7",
		},
	},
	GameServer: GameServerConfig{
		HeartbeatIntervalSeconds: 15,
		UnhealthyAfterSeconds:    45,
		OfflineAfterSeconds:      90,
		ServerTokenTTLHours:      168,
		SweepIntervalSeconds:     5,
	},
	P2PRoom: P2PRoomConfig{
		HeartbeatIntervalSeconds: 15,
		StaleAfterSeconds:        45,
		ClosedAfterSeconds:       90,
		SweepIntervalSeconds:     5,
		MaximumPlayers:           64,
	},
	Connection: ConnectionConfig{
		SessionTTLSeconds:    600,
		SweepIntervalSeconds: 5,
		WebSocketQueueSize:   64,
		WebSocketMaxBytes:    16 * 1024,
	},
	RelayRegistry: RelayRegistryConfig{
		ControlAddr:              ":9090",
		ServerNames:              []string{"control-plane", "localhost"},
		HeartbeatIntervalSeconds: 15,
		UnhealthyAfterSeconds:    45,
		OfflineAfterSeconds:      90,
		SweepIntervalSeconds:     5,
		DrainDeadlineSeconds:     300,
		MigrationTimeoutSeconds:  45,
		MigrationMaxAttempts:     3,
		CertificateTTLHours:      24,
		RelayTokenKeyID:          "relay-dev-ephemeral",
		RelayTokenTTLSeconds:     120,
		AllocationTTLSeconds:     1800,
		CapacityThresholdPercent: 80,
	},
	Update: UpdateConfig{
		Product:              "project-rebound",
		ManifestDirectory:    "deployments/updates",
		CDNBaseURL:           "https://cdn.example.com/project-rebound",
		SigningKeyID:         "update-dev-ephemeral",
		DefaultChannel:       "stable",
		DefaultArchitecture:  "amd64",
		MinimumClientVersion: "1.0.0",
		RealtimeURL:          "wss://realtime.example.com/v1/realtime/connect",
		STUNServers:          []string{"stun:stun.example.com:3478"},
		APIVersion:           "v1",
		ProtocolVersion:      2,
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
		Level:     "info",
		AddSource: false,
	},
}

func Load(path string) (*Config, error) {
	cfg := Defaults
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
	} else {
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return nil, err
		}
	}
	cfg.applyEnvOverrides()
	return &cfg, nil
}

func (c *Config) applyEnvOverrides() {
	overrideString("CONTROL_PLANE_ENVIRONMENT", &c.Environment)
	overrideString("CONTROL_PLANE_HTTP_ADDR", &c.HTTP.Addr)
	overrideString("DATABASE_URL", &c.Database.URL)
	overrideString("REDIS_ADDRESS", &c.Redis.Address)
	overrideString("REDIS_USERNAME", &c.Redis.Username)
	overrideString("REDIS_PASSWORD", &c.Redis.Password)
	overrideString("LOG_LEVEL", &c.Logging.Level)
	overrideString("ACCESS_TOKEN_PRIVATE_KEY_BASE64", &c.Auth.AccessTokenPrivateKeyBase64)
	overrideString("ACCESS_TOKEN_KEY_ID", &c.Auth.AccessTokenKeyID)
	overrideString("DEVICE_FINGERPRINT_KEY_ID", &c.Auth.DeviceFingerprintKeyID)
	overrideString("DEVICE_FINGERPRINT_HMAC_KEY_BASE64", &c.Auth.DeviceFingerprintHMACKeyBase64)
	overrideString("ADMIN_TOKENS", &c.Admin.TokenSet)
	overrideString("ADMIN_ACCESS_TOKEN_PRIVATE_KEY_BASE64", &c.Admin.AccessTokenPrivateKeyBase64)
	overrideString("ADMIN_ACCESS_TOKEN_KEY_ID", &c.Admin.AccessTokenKeyID)
	overrideString("ADMIN_MFA_ENCRYPTION_KEY_BASE64", &c.Admin.MFAEncryptionKeyBase64)
	overrideString("TURNSTILE_SITE_KEY", &c.Admin.TurnstileSiteKey)
	overrideString("TURNSTILE_SECRET_KEY", &c.Admin.TurnstileSecretKey)
	overrideString("TURNSTILE_EXPECTED_HOSTNAME", &c.Admin.TurnstileExpectedHostname)
	overrideString("TURNSTILE_EXPECTED_ACTION", &c.Admin.TurnstileExpectedAction)
	overrideString("TURNSTILE_VERIFY_URL", &c.Admin.TurnstileVerifyURL)
	overrideInt("ADMIN_ACCESS_TOKEN_TTL_MINUTES", &c.Admin.AccessTokenTTLMinutes)
	overrideInt("ADMIN_REFRESH_TOKEN_TTL_DAYS", &c.Admin.RefreshTokenTTLDays)
	overrideInt("ADMIN_LOGIN_CHALLENGE_TTL_SECONDS", &c.Admin.LoginChallengeTTLSeconds)
	overrideInt("ADMIN_STEP_UP_TTL_SECONDS", &c.Admin.StepUpTTLSeconds)
	overrideInt("ADMIN_LOGIN_PER_IP_PER_MINUTE", &c.Admin.LoginPerIPPerMinute)
	overrideInt("ADMIN_LOGIN_PER_USERNAME_PER_MINUTE", &c.Admin.LoginPerUsernamePerMinute)
	overrideInt("TURNSTILE_TIMEOUT_SECONDS", &c.Admin.TurnstileTimeoutSeconds)
	overrideString("GAME_SERVER_REGISTRATION_TOKENS", &c.GameServer.RegistrationTokenSet)
	overrideInt("REDIS_DB", &c.Redis.DB)
	overrideInt("HTTP_RATE_LIMIT_BURST", &c.RateLimit.Burst)
	overrideInt("AUTH_BIND_REQUESTS_PER_MINUTE", &c.Auth.BindRequestsPerMinute)
	overrideInt("AUTH_BIND_BURST", &c.Auth.BindBurst)
	overrideInt("AUTH_BIND_PER_IP_PER_MINUTE", &c.Auth.BindRateLimit.PerIPPerMinute)
	overrideInt("AUTH_BIND_PER_DEVICE_PER_MINUTE", &c.Auth.BindRateLimit.PerDevicePerMinute)
	overrideInt("AUTH_BIND_PER_STEAM_ID_PER_MINUTE", &c.Auth.BindRateLimit.PerSteamIDPerMinute)
	overrideBool("AUTH_INVITE_REQUIRED", &c.Auth.InviteRequired)
	overrideInt("CONNECTION_SESSION_TTL_SECONDS", &c.Connection.SessionTTLSeconds)
	overrideInt("CONNECTION_SWEEP_INTERVAL_SECONDS", &c.Connection.SweepIntervalSeconds)
	overrideInt("CONNECTION_WEBSOCKET_QUEUE_SIZE", &c.Connection.WebSocketQueueSize)
	overrideInt("CONNECTION_WEBSOCKET_MAX_MESSAGE_BYTES", &c.Connection.WebSocketMaxBytes)
	overrideString("RELAY_CONTROL_ADDR", &c.RelayRegistry.ControlAddr)
	if raw := os.Getenv("RELAY_CONTROL_SERVER_NAMES"); raw != "" {
		c.RelayRegistry.ServerNames = splitCSV(raw)
	}
	overrideString("RELAY_BOOTSTRAP_TOKENS", &c.RelayRegistry.BootstrapTokenSet)
	overrideString("RELAY_CA_CERT_PEM_BASE64", &c.RelayRegistry.CACertificatePEMBase64)
	overrideString("RELAY_CA_KEY_PEM_BASE64", &c.RelayRegistry.CAPrivateKeyPEMBase64)
	overrideString("RELAY_TOKEN_KEY_ID", &c.RelayRegistry.RelayTokenKeyID)
	overrideString("RELAY_TOKEN_PRIVATE_KEY_BASE64", &c.RelayRegistry.RelayTokenPrivateKeyBase64)
	overrideString("RELAY_TOKEN_ROTATION_KEYS", &c.RelayRegistry.RelayTokenRotationKeys)
	overrideInt("RELAY_HEARTBEAT_INTERVAL_SECONDS", &c.RelayRegistry.HeartbeatIntervalSeconds)
	overrideInt("RELAY_UNHEALTHY_AFTER_SECONDS", &c.RelayRegistry.UnhealthyAfterSeconds)
	overrideInt("RELAY_OFFLINE_AFTER_SECONDS", &c.RelayRegistry.OfflineAfterSeconds)
	overrideInt("RELAY_SWEEP_INTERVAL_SECONDS", &c.RelayRegistry.SweepIntervalSeconds)
	overrideInt("RELAY_MIGRATION_TIMEOUT_SECONDS", &c.RelayRegistry.MigrationTimeoutSeconds)
	overrideInt("RELAY_MIGRATION_MAX_ATTEMPTS", &c.RelayRegistry.MigrationMaxAttempts)
	overrideString("UPDATE_MANIFEST_DIRECTORY", &c.Update.ManifestDirectory)
	overrideString("UPDATE_CDN_BASE_URL", &c.Update.CDNBaseURL)
	overrideString("UPDATE_SIGNING_KEY_ID", &c.Update.SigningKeyID)
	overrideString("UPDATE_SIGNING_PRIVATE_KEY_BASE64", &c.Update.SigningPrivateKeyBase64)
	overrideString("UPDATE_DEFAULT_CHANNEL", &c.Update.DefaultChannel)
	overrideString("UPDATE_MINIMUM_CLIENT_VERSION", &c.Update.MinimumClientVersion)
	overrideString("UPDATE_REALTIME_URL", &c.Update.RealtimeURL)
	if raw := os.Getenv("HTTP_RATE_LIMIT_RPS"); raw != "" {
		if value, err := strconv.ParseFloat(raw, 64); err == nil {
			c.RateLimit.RequestsPerSecond = value
		}
	}
	if raw := os.Getenv("CORS_ALLOWED_ORIGINS"); raw != "" {
		c.CORS.AllowedOrigins = splitCSV(raw)
	}
	if raw := os.Getenv("ADMIN_TRUSTED_CIDRS"); raw != "" {
		c.Admin.TrustedCIDRs = splitCSV(raw)
	}
	if raw := os.Getenv("UPDATE_STUN_SERVERS"); raw != "" {
		c.Update.STUNServers = splitCSV(raw)
	}
	if raw := os.Getenv("TRUST_PROXY_HEADERS"); raw != "" {
		if value, err := strconv.ParseBool(raw); err == nil {
			c.HTTP.TrustProxyHeaders = value
		}
	}

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

func overrideString(name string, target *string) {
	if value := os.Getenv(name); value != "" {
		*target = value
	}
}

func overrideInt(name string, target *int) {
	if raw := os.Getenv(name); raw != "" {
		if value, err := strconv.Atoi(raw); err == nil {
			*target = value
		}
	}
}

func overrideBool(name string, target *bool) {
	if raw := os.Getenv(name); raw != "" {
		if value, err := strconv.ParseBool(raw); err == nil {
			*target = value
		}
	}
}

func splitCSV(raw string) []string {
	parts := strings.Split(raw, ",")
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		if value := strings.TrimSpace(part); value != "" {
			values = append(values, value)
		}
	}
	return values
}

func (c *Config) ValidateControlPlane() error {
	var errs []error
	if strings.TrimSpace(c.HTTP.Addr) == "" {
		errs = append(errs, errors.New("http.addr is required"))
	}
	if parsed, err := url.Parse(c.Database.URL); err != nil || parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" {
		errs = append(errs, errors.New("database.url must be a PostgreSQL URL"))
	}
	if strings.TrimSpace(c.Redis.Address) == "" {
		errs = append(errs, errors.New("redis.address is required"))
	}
	if c.Database.MaxConnections < 1 {
		errs = append(errs, errors.New("database.max_connections must be positive"))
	}
	if c.Database.MinConnections < 0 || c.Database.MinConnections > c.Database.MaxConnections {
		errs = append(errs, errors.New("database.min_connections must be between zero and max_connections"))
	}
	if c.RateLimit.RequestsPerSecond <= 0 || c.RateLimit.Burst < 1 {
		errs = append(errs, errors.New("rate_limit values must be positive"))
	}
	if c.HTTP.MaxRequestBodyBytes < 1 {
		errs = append(errs, errors.New("http.max_request_body_bytes must be positive"))
	}
	if strings.TrimSpace(c.Auth.Issuer) == "" || strings.TrimSpace(c.Auth.Audience) == "" {
		errs = append(errs, errors.New("auth issuer and audience are required"))
	}
	if strings.TrimSpace(c.Auth.AccessTokenKeyID) == "" {
		errs = append(errs, errors.New("auth.access_token_key_id is required"))
	}
	if strings.TrimSpace(c.Auth.DeviceFingerprintKeyID) == "" {
		errs = append(errs, errors.New("auth.device_fingerprint_key_id is required"))
	}
	if c.Auth.AccessTokenTTLMinutes < 1 || c.Auth.RefreshTokenTTLDays < 1 {
		errs = append(errs, errors.New("auth token lifetimes must be positive"))
	}
	if c.Auth.BindRequestsPerMinute < 1 || c.Auth.BindBurst < 1 ||
		c.Auth.BindRateLimit.PerIPPerMinute < 1 || c.Auth.BindRateLimit.PerDevicePerMinute < 1 ||
		c.Auth.BindRateLimit.PerSteamIDPerMinute < 1 {
		errs = append(errs, errors.New("auth bind rate limit values must be positive"))
	}
	if strings.EqualFold(c.Environment, "production") && strings.TrimSpace(c.Auth.AccessTokenPrivateKeyBase64) == "" {
		errs = append(errs, errors.New("ACCESS_TOKEN_PRIVATE_KEY_BASE64 is required in production"))
	}
	if strings.EqualFold(c.Environment, "production") && strings.TrimSpace(c.Auth.DeviceFingerprintHMACKeyBase64) == "" {
		errs = append(errs, errors.New("DEVICE_FINGERPRINT_HMAC_KEY_BASE64 is required in production"))
	}
	if len(c.Admin.TrustedCIDRs) == 0 {
		errs = append(errs, errors.New("admin.trusted_cidrs must not be empty"))
	}
	if strings.TrimSpace(c.Admin.Issuer) == "" || strings.TrimSpace(c.Admin.Audience) == "" ||
		strings.TrimSpace(c.Admin.AccessTokenKeyID) == "" {
		errs = append(errs, errors.New("admin issuer, audience, and access token key ID are required"))
	}
	if c.Admin.AccessTokenTTLMinutes < 1 || c.Admin.RefreshTokenTTLDays < 1 ||
		c.Admin.LoginChallengeTTLSeconds < 30 || c.Admin.LoginChallengeTTLSeconds > 600 ||
		c.Admin.StepUpTTLSeconds < 30 || c.Admin.StepUpTTLSeconds > 600 ||
		c.Admin.LoginPerIPPerMinute < 1 || c.Admin.LoginPerUsernamePerMinute < 1 ||
		c.Admin.TurnstileTimeoutSeconds < 1 {
		errs = append(errs, errors.New("admin authentication timing and rate limit values are invalid"))
	}
	if strings.TrimSpace(c.Admin.TurnstileVerifyURL) == "" ||
		strings.TrimSpace(c.Admin.TurnstileExpectedHostname) == "" ||
		strings.TrimSpace(c.Admin.TurnstileExpectedAction) == "" {
		errs = append(errs, errors.New("admin Turnstile verification URL, hostname, and action are required"))
	}
	if strings.EqualFold(c.Environment, "production") && strings.TrimSpace(c.Admin.TokenSet) == "" {
		errs = append(errs, errors.New("ADMIN_TOKENS is required in production"))
	}
	if strings.EqualFold(c.Environment, "production") &&
		(strings.TrimSpace(c.Admin.AccessTokenPrivateKeyBase64) == "" ||
			strings.TrimSpace(c.Admin.MFAEncryptionKeyBase64) == "" ||
			strings.TrimSpace(c.Admin.TurnstileSiteKey) == "" ||
			strings.TrimSpace(c.Admin.TurnstileSecretKey) == "") {
		errs = append(errs, errors.New("admin access token, MFA encryption, and Turnstile keys are required in production"))
	}
	if c.GameServer.HeartbeatIntervalSeconds < 1 ||
		c.GameServer.UnhealthyAfterSeconds <= c.GameServer.HeartbeatIntervalSeconds ||
		c.GameServer.OfflineAfterSeconds <= c.GameServer.UnhealthyAfterSeconds ||
		c.GameServer.ServerTokenTTLHours < 1 || c.GameServer.SweepIntervalSeconds < 1 {
		errs = append(errs, errors.New("game_server timing and token settings are invalid"))
	}
	if strings.EqualFold(c.Environment, "production") && strings.TrimSpace(c.GameServer.RegistrationTokenSet) == "" {
		errs = append(errs, errors.New("GAME_SERVER_REGISTRATION_TOKENS is required in production"))
	}
	if c.P2PRoom.HeartbeatIntervalSeconds < 1 ||
		c.P2PRoom.StaleAfterSeconds <= c.P2PRoom.HeartbeatIntervalSeconds ||
		c.P2PRoom.ClosedAfterSeconds <= c.P2PRoom.StaleAfterSeconds ||
		c.P2PRoom.SweepIntervalSeconds < 1 || c.P2PRoom.MaximumPlayers < 2 || c.P2PRoom.MaximumPlayers > 64 {
		errs = append(errs, errors.New("p2p_room timing and capacity settings are invalid"))
	}
	if c.Connection.SessionTTLSeconds < 30 || c.Connection.SweepIntervalSeconds < 1 ||
		c.Connection.WebSocketQueueSize < 1 || c.Connection.WebSocketMaxBytes < 1024 {
		errs = append(errs, errors.New("connection timing and websocket settings are invalid"))
	}
	if strings.TrimSpace(c.RelayRegistry.ControlAddr) == "" || len(c.RelayRegistry.ServerNames) == 0 ||
		c.RelayRegistry.HeartbeatIntervalSeconds < 1 ||
		c.RelayRegistry.UnhealthyAfterSeconds <= c.RelayRegistry.HeartbeatIntervalSeconds ||
		c.RelayRegistry.OfflineAfterSeconds <= c.RelayRegistry.UnhealthyAfterSeconds ||
		c.RelayRegistry.SweepIntervalSeconds < 1 || c.RelayRegistry.DrainDeadlineSeconds < 1 ||
		c.RelayRegistry.MigrationTimeoutSeconds < c.RelayRegistry.SweepIntervalSeconds ||
		c.RelayRegistry.MigrationMaxAttempts < 1 || c.RelayRegistry.MigrationMaxAttempts > 10 ||
		c.RelayRegistry.CertificateTTLHours < 1 || c.RelayRegistry.RelayTokenTTLSeconds < 30 ||
		c.RelayRegistry.AllocationTTLSeconds < c.RelayRegistry.RelayTokenTTLSeconds ||
		c.RelayRegistry.CapacityThresholdPercent < 1 || c.RelayRegistry.CapacityThresholdPercent > 100 ||
		strings.TrimSpace(c.RelayRegistry.RelayTokenKeyID) == "" {
		errs = append(errs, errors.New("relay_registry timing, capacity, certificate, or token settings are invalid"))
	}
	if strings.EqualFold(c.Environment, "production") &&
		(strings.TrimSpace(c.RelayRegistry.BootstrapTokenSet) == "" ||
			strings.TrimSpace(c.RelayRegistry.CACertificatePEMBase64) == "" ||
			strings.TrimSpace(c.RelayRegistry.CAPrivateKeyPEMBase64) == "" ||
			strings.TrimSpace(c.RelayRegistry.RelayTokenPrivateKeyBase64) == "") {
		errs = append(errs, errors.New("relay bootstrap, CA, and signing key environment variables are required in production"))
	}
	updateCDNURL, updateCDNErr := url.Parse(c.Update.CDNBaseURL)
	realtimeURL, realtimeErr := url.Parse(c.Update.RealtimeURL)
	if strings.TrimSpace(c.Update.Product) == "" || strings.TrimSpace(c.Update.ManifestDirectory) == "" ||
		strings.TrimSpace(c.Update.SigningKeyID) == "" ||
		(c.Update.DefaultChannel != "stable" && c.Update.DefaultChannel != "beta") ||
		strings.TrimSpace(c.Update.DefaultArchitecture) == "" || strings.TrimSpace(c.Update.MinimumClientVersion) == "" ||
		strings.TrimSpace(c.Update.APIVersion) == "" || c.Update.ProtocolVersion < 1 || len(c.Update.STUNServers) == 0 ||
		updateCDNErr != nil || updateCDNURL.Host == "" || (updateCDNURL.Scheme != "https" && updateCDNURL.Scheme != "http") ||
		realtimeErr != nil || realtimeURL.Host == "" || (realtimeURL.Scheme != "wss" && realtimeURL.Scheme != "ws") {
		errs = append(errs, errors.New("update service configuration is invalid"))
	}
	if strings.EqualFold(c.Environment, "production") &&
		(strings.TrimSpace(c.Update.SigningPrivateKeyBase64) == "" || updateCDNURL.Scheme != "https" || realtimeURL.Scheme != "wss") {
		errs = append(errs, errors.New("UPDATE_SIGNING_PRIVATE_KEY_BASE64 and secure update URLs are required in production"))
	}
	if len(errs) > 0 {
		return fmt.Errorf("invalid control-plane configuration: %w", errors.Join(errs...))
	}
	return nil
}

func (c HTTPConfig) ReadHeaderTimeout() time.Duration {
	return time.Duration(c.ReadHeaderTimeoutSecs) * time.Second
}

func (c HTTPConfig) ReadTimeout() time.Duration {
	return time.Duration(c.ReadTimeoutSecs) * time.Second
}

func (c HTTPConfig) WriteTimeout() time.Duration {
	return time.Duration(c.WriteTimeoutSecs) * time.Second
}

func (c HTTPConfig) IdleTimeout() time.Duration {
	return time.Duration(c.IdleTimeoutSecs) * time.Second
}

func (c HTTPConfig) ShutdownTimeout() time.Duration {
	return time.Duration(c.ShutdownTimeoutSecs) * time.Second
}

func (c DBConfig) MaxConnectionLifetime() time.Duration {
	return time.Duration(c.MaxConnectionMins) * time.Minute
}

func (c DBConfig) HealthTimeout() time.Duration {
	return time.Duration(c.HealthTimeoutSecs) * time.Second
}

func (c RedisConfig) ConnectTimeout() time.Duration {
	return time.Duration(c.ConnectTimeoutSecs) * time.Second
}

func (c RedisConfig) OperationTimeout() time.Duration {
	return time.Duration(c.OperationTimeoutSecs) * time.Second
}

func (c AuthConfig) AccessTokenTTL() time.Duration {
	return time.Duration(c.AccessTokenTTLMinutes) * time.Minute
}

func (c AuthConfig) RefreshTokenTTL() time.Duration {
	return time.Duration(c.RefreshTokenTTLDays) * 24 * time.Hour
}

func (c AdminConfig) AccessTokenConfig() AuthConfig {
	return AuthConfig{
		Issuer:                      c.Issuer,
		Audience:                    c.Audience,
		AccessTokenKeyID:            c.AccessTokenKeyID,
		AccessTokenPrivateKeyBase64: c.AccessTokenPrivateKeyBase64,
		AccessTokenTTLMinutes:       c.AccessTokenTTLMinutes,
		RefreshTokenTTLDays:         c.RefreshTokenTTLDays,
	}
}

func (c AdminConfig) AccessTokenTTL() time.Duration {
	return time.Duration(c.AccessTokenTTLMinutes) * time.Minute
}

func (c AdminConfig) RefreshTokenTTL() time.Duration {
	return time.Duration(c.RefreshTokenTTLDays) * 24 * time.Hour
}

func (c AdminConfig) LoginChallengeTTL() time.Duration {
	return time.Duration(c.LoginChallengeTTLSeconds) * time.Second
}

func (c AdminConfig) StepUpTTL() time.Duration {
	return time.Duration(c.StepUpTTLSeconds) * time.Second
}

func (c AdminConfig) TurnstileTimeout() time.Duration {
	return time.Duration(c.TurnstileTimeoutSeconds) * time.Second
}

func (c GameServerConfig) ServerTokenTTL() time.Duration {
	return time.Duration(c.ServerTokenTTLHours) * time.Hour
}

func (c GameServerConfig) SweepInterval() time.Duration {
	return time.Duration(c.SweepIntervalSeconds) * time.Second
}

func (c P2PRoomConfig) SweepInterval() time.Duration {
	return time.Duration(c.SweepIntervalSeconds) * time.Second
}

func (c ConnectionConfig) SessionTTL() time.Duration {
	return time.Duration(c.SessionTTLSeconds) * time.Second
}

func (c ConnectionConfig) SweepInterval() time.Duration {
	return time.Duration(c.SweepIntervalSeconds) * time.Second
}

func (c RelayRegistryConfig) SweepInterval() time.Duration {
	return time.Duration(c.SweepIntervalSeconds) * time.Second
}

func (c RelayRegistryConfig) CertificateTTL() time.Duration {
	return time.Duration(c.CertificateTTLHours) * time.Hour
}

func (c RelayRegistryConfig) RelayTokenTTL() time.Duration {
	return time.Duration(c.RelayTokenTTLSeconds) * time.Second
}

func (c RelayRegistryConfig) AllocationTTL() time.Duration {
	return time.Duration(c.AllocationTTLSeconds) * time.Second
}
