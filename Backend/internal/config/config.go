package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Environment   string              `yaml:"environment"`
	HTTP          HTTPConfig          `yaml:"http"`
	Database      DBConfig            `yaml:"database"`
	Redis         RedisConfig         `yaml:"redis"`
	CORS          CORSConfig          `yaml:"cors"`
	RateLimit     RateLimitConfig     `yaml:"rate_limit"`
	Auth          AuthConfig          `yaml:"auth"`
	Admin         AdminConfig         `yaml:"admin"`
	GameServer    GameServerConfig    `yaml:"game_server"`
	MetaServer    MetaServerConfig    `yaml:"meta_server"`
	MatchLobby    MatchLobbyConfig    `yaml:"match_lobby"`
	P2PRoom       P2PRoomConfig       `yaml:"p2p_room"`
	P2PBattleLog  P2PBattleLogConfig  `yaml:"p2p_battlelog"`
	Connection    ConnectionConfig    `yaml:"connection"`
	RelayRegistry RelayRegistryConfig `yaml:"relay_registry"`
	VNT           VNTConfig           `yaml:"vnt"`
	Update        UpdateConfig        `yaml:"update"`
	Downloads     DownloadConfig      `yaml:"downloads"`
	Logging       LogConfig           `yaml:"logging"`
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

// SteamAppID, TicketMaximumAgeSeconds, and TicketClockSkewSeconds are retained
// for configuration compatibility but no longer participate in ticket acceptance.
type AuthConfig struct {
	Issuer                         string                  `yaml:"issuer"`
	Audience                       string                  `yaml:"audience"`
	AccessTokenKeyID               string                  `yaml:"access_token_key_id"`
	AccessTokenPrivateKeyBase64    string                  `yaml:"access_token_private_key_base64"`
	AccessTokenPublicKeyBase64     string                  `yaml:"access_token_public_key_base64"`
	AccessTokenTTLMinutes          int                     `yaml:"access_token_ttl_minutes"`
	RefreshTokenTTLDays            int                     `yaml:"refresh_token_ttl_days"`
	DefaultPersonaName             string                  `yaml:"default_persona_name"`
	InviteRequired                 bool                    `yaml:"invite_required"`
	DeviceFingerprintKeyID         string                  `yaml:"device_fingerprint_key_id"`
	DeviceFingerprintHMACKeyBase64 string                  `yaml:"-"`
	SteamAppID                     uint32                  `yaml:"steam_app_id"`
	TicketVerifierExecutable       string                  `yaml:"ticket_verifier_executable"`
	TicketVerifierTimeoutSeconds   int                     `yaml:"ticket_verifier_timeout_seconds"`
	TicketMaximumAgeSeconds        int                     `yaml:"ticket_maximum_age_seconds"`
	TicketClockSkewSeconds         int                     `yaml:"ticket_clock_skew_seconds"`
	TicketMaximumHexBytes          int                     `yaml:"ticket_maximum_hex_bytes"`
	TicketMaximumOutputBytes       int                     `yaml:"ticket_maximum_output_bytes"`
	IntegrityPublicKeyPath         string                  `yaml:"integrity_public_key_path"`
	IntegrityPublicKeyPEM          string                  `yaml:"-"`
	IntegrityChallengeTTLSeconds   int                     `yaml:"integrity_challenge_ttl_seconds"`
	IntegrityMaximumFailures       int                     `yaml:"integrity_maximum_failures"`
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
	AccessTokenPublicKeyBase64  string   `yaml:"-"`
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
	HeartbeatIntervalSeconds        int    `yaml:"heartbeat_interval_seconds"`
	UnhealthyAfterSeconds           int    `yaml:"unhealthy_after_seconds"`
	OfflineAfterSeconds             int    `yaml:"offline_after_seconds"`
	RegistrationTokenTTLMinutes     int    `yaml:"registration_token_ttl_minutes"`
	ServerTokenTTLHours             int    `yaml:"server_token_ttl_hours"`
	ServerTokenRotationGraceSeconds int    `yaml:"server_token_rotation_grace_seconds"`
	CertificateTTLHours             int    `yaml:"certificate_ttl_hours"`
	SignatureMaxClockSkewSeconds    int    `yaml:"signature_max_clock_skew_seconds"`
	CACertificatePEMBase64          string `yaml:"-"`
	CAPrivateKeyPEMBase64           string `yaml:"-"`
	SweepIntervalSeconds            int    `yaml:"sweep_interval_seconds"`
}

type MetaServerConfig struct {
	HTTPAddr                   string `yaml:"http_addr"`
	LogicAddr                  string `yaml:"logic_addr"`
	PublicHTTPBaseURL          string `yaml:"public_http_base_url"`
	PublicLogicEndpoint        string `yaml:"public_logic_endpoint"`
	LogicProxyProtocol         bool   `yaml:"logic_proxy_protocol"`
	ProtocolVersion            int    `yaml:"protocol_version"`
	GateTicketTTLSeconds       int    `yaml:"gate_ticket_ttl_seconds"`
	MaxFrameBytes              int    `yaml:"max_frame_bytes"`
	MaxWriteQueueBytes         int    `yaml:"max_write_queue_bytes"`
	HandshakeTimeoutSeconds    int    `yaml:"handshake_timeout_seconds"`
	FrameTimeoutSeconds        int    `yaml:"frame_timeout_seconds"`
	IdleTimeoutSeconds         int    `yaml:"idle_timeout_seconds"`
	MaxConnectionsPerIP        int    `yaml:"max_connections_per_ip"`
	ConnectionsPerIPPerMinute  int    `yaml:"connections_per_ip_per_minute"`
	RPCCallsPerPlayerPerMinute int    `yaml:"rpc_calls_per_player_per_minute"`
	MatchTicketTTLSeconds      int    `yaml:"match_ticket_ttl_seconds"`
	MatchReservationTTLSeconds int    `yaml:"match_reservation_ttl_seconds"`
	SchedulerIntervalSeconds   int    `yaml:"scheduler_interval_seconds"`
	RelayFreshnessSeconds      int    `yaml:"relay_freshness_seconds"`
	// NativePlayerLevel is the level reported by GetPlayerArchiveV2. Boundary
	// consumes this value while initializing its native progression and FieldMod
	// state; keep it configurable so ownership and progression can be A/B tested.
	NativePlayerLevel int `yaml:"native_player_level"`
	// NativeCharacterLevel is returned through GetDataStatisticsInfo for every
	// playable operator. The current pinned Boundary build has 30 character
	// levels; keeping it separate from the 70-level player progression prevents
	// the native CareerManager from resetting operator rewards to level zero.
	NativeCharacterLevel int `yaml:"native_character_level"`
	// NativeOwnershipMode controls the QueryAssets ownership set. "full" is the
	// production mode and includes every pinned DT_ItemType row. "compact"
	// remains available only for controlled diagnostics that exclude generated
	// per-slot/suite painting instances.
	NativeOwnershipMode         string `yaml:"native_ownership_mode"`
	DevelopmentLegacyLoadoutAPI bool   `yaml:"development_legacy_loadout_api"`
}

type P2PRoomConfig struct {
	HeartbeatIntervalSeconds int `yaml:"heartbeat_interval_seconds"`
	StaleAfterSeconds        int `yaml:"stale_after_seconds"`
	ClosedAfterSeconds       int `yaml:"closed_after_seconds"`
	SweepIntervalSeconds     int `yaml:"sweep_interval_seconds"`
	MaximumPlayers           int `yaml:"maximum_players"`
}

type MatchLobbyConfig struct {
	StrictRosterV1Enabled     bool   `yaml:"strict_roster_v1_enabled"`
	LockedGameSHA256          string `yaml:"locked_game_sha256"`
	PresenceGraceSeconds      int    `yaml:"presence_grace_seconds"`
	ProvisioningSeconds       int    `yaml:"provisioning_seconds"`
	InitialConnectionSeconds  int    `yaml:"initial_connection_seconds"`
	P2PHostReconnectSeconds   int    `yaml:"p2p_host_reconnect_seconds"`
	AdmissionGrantTTLSeconds  int    `yaml:"admission_grant_ttl_seconds"`
	SweepIntervalSeconds      int    `yaml:"sweep_interval_seconds"`
	AdmissionSigningKeyID     string `yaml:"admission_signing_key_id"`
	AdmissionPrivateKeyBase64 string `yaml:"-"`
}

type P2PBattleLogConfig struct {
	Enabled                   bool   `yaml:"enabled"`
	ShadowMode                bool   `yaml:"shadow_mode"`
	PolicyVersion             string `yaml:"policy_version"`
	MaxReportBytes            int    `yaml:"max_report_bytes"`
	MaxEvents                 int    `yaml:"max_events"`
	CollectionDeadlineSeconds int    `yaml:"collection_deadline_seconds"`
	HardExpiryHours           int    `yaml:"hard_expiry_hours"`
	CapabilityTTLHours        int    `yaml:"capability_ttl_hours"`
	FinalizerIntervalSeconds  int    `yaml:"finalizer_interval_seconds"`
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

type VNTConfig struct {
	// Deprecated compatibility inputs retained only so older deployment files
	// continue to decode. Published ToolBox manifests are authoritative.
	AllowedVNTSVersions                     []string `yaml:"allowed_vnts_versions"`
	AllowedWrapperVersions                  []string `yaml:"allowed_wrapper_versions"`
	CredentialRotationGraceSeconds          int      `yaml:"credential_rotation_grace_seconds"`
	EnrollmentRequestsPerPlayerPerHour      int      `yaml:"enrollment_requests_per_player_per_hour"`
	DirectoryRequestsPerIPPerMinute         int      `yaml:"directory_requests_per_ip_per_minute"`
	BootstrapRequestsPerPlayerPerMinute     int      `yaml:"bootstrap_requests_per_player_per_minute"`
	HeartbeatRequestsPerCredentialPerMinute int      `yaml:"heartbeat_requests_per_credential_per_minute"`
	ManagementRequestsPerCredentialPerHour  int      `yaml:"management_requests_per_credential_per_hour"`
	MaxNodesPerPlayer                       int      `yaml:"max_nodes_per_player"`
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
	VNTRoomsEnabled         bool     `yaml:"vnt_rooms_enabled"`
}

type DownloadConfig struct {
	Enabled                  bool     `yaml:"enabled"`
	S3Endpoint               string   `yaml:"s3_endpoint"`
	S3UploadEndpoint         string   `yaml:"s3_upload_endpoint"`
	S3Region                 string   `yaml:"s3_region"`
	S3Bucket                 string   `yaml:"s3_bucket"`
	S3AccessKeyID            string   `yaml:"-"`
	S3SecretAccessKey        string   `yaml:"-"`
	PublicBaseURL            string   `yaml:"public_base_url"`
	PublicProbeBaseURL       string   `yaml:"public_probe_base_url"`
	AllowedExtensions        []string `yaml:"allowed_extensions"`
	MaxFileBytes             int64    `yaml:"max_file_bytes"`
	MultipartThresholdBytes  int64    `yaml:"multipart_threshold_bytes"`
	PartSizeBytes            int64    `yaml:"part_size_bytes"`
	UploadSessionTTLHours    int      `yaml:"upload_session_ttl_hours"`
	PresignTTLMinutes        int      `yaml:"presign_ttl_minutes"`
	VerificationIntervalSecs int      `yaml:"verification_interval_seconds"`
}

type LogConfig struct {
	Level     string `yaml:"level"`
	AddSource bool   `yaml:"add_source"`
}

var Defaults = Config{
	Environment: "development",
	HTTP: HTTPConfig{
		Addr:                  ":8080",
		ReadHeaderTimeoutSecs: 5,
		ReadTimeoutSecs:       15,
		WriteTimeoutSecs:      30,
		IdleTimeoutSecs:       60,
		ShutdownTimeoutSecs:   10,
		MaxRequestBodyBytes:   1 << 20,
	},
	Database: DBConfig{
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
		AllowedHeaders: []string{
			"Authorization", "Content-Type", "X-Request-Id", "X-Room-Host-Token", "X-P2P-Report-Token",
			"X-Match-Transport-Host-Token", "X-Match-Authority-Session",
			"X-Admin-Step-Up",
			"X-Game-Server-Id", "X-Game-Server-Certificate", "X-Game-Server-Timestamp",
			"X-Game-Server-Nonce", "X-Game-Server-Generation", "X-Game-Server-Signature",
		},
		AllowCredentials: true,
		MaxAgeSeconds:    600,
	},
	RateLimit: RateLimitConfig{
		RequestsPerSecond: 25,
		Burst:             50,
	},
	Auth: AuthConfig{
		Issuer:                       "game-control-plane",
		Audience:                     "game-client",
		AccessTokenKeyID:             "access-dev-ephemeral",
		AccessTokenTTLMinutes:        15,
		RefreshTokenTTLDays:          30,
		DefaultPersonaName:           "Player",
		DeviceFingerprintKeyID:       "device-fingerprint-v1",
		SteamAppID:                   480,
		TicketVerifierExecutable:     "decrypt-ticket.exe",
		TicketVerifierTimeoutSeconds: 3,
		TicketMaximumAgeSeconds:      300,
		TicketClockSkewSeconds:       60,
		TicketMaximumHexBytes:        4096,
		TicketMaximumOutputBytes:     8192,
		IntegrityChallengeTTLSeconds: 120,
		IntegrityMaximumFailures:     3,
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
		HeartbeatIntervalSeconds:        15,
		UnhealthyAfterSeconds:           45,
		OfflineAfterSeconds:             90,
		RegistrationTokenTTLMinutes:     10,
		ServerTokenTTLHours:             24,
		ServerTokenRotationGraceSeconds: 60,
		CertificateTTLHours:             24,
		SignatureMaxClockSkewSeconds:    60,
		SweepIntervalSeconds:            5,
	},
	MetaServer: MetaServerConfig{
		HTTPAddr:                   ":8081",
		LogicAddr:                  ":6968",
		PublicHTTPBaseURL:          "https://meta.dubnium.top",
		PublicLogicEndpoint:        "logic.dubnium.top:443",
		ProtocolVersion:            1,
		GateTicketTTLSeconds:       60,
		MaxFrameBytes:              2 << 20,
		MaxWriteQueueBytes:         4 << 20,
		HandshakeTimeoutSeconds:    10,
		FrameTimeoutSeconds:        15,
		IdleTimeoutSeconds:         120,
		MaxConnectionsPerIP:        8,
		ConnectionsPerIPPerMinute:  30,
		RPCCallsPerPlayerPerMinute: 600,
		MatchTicketTTLSeconds:      300,
		MatchReservationTTLSeconds: 90,
		SchedulerIntervalSeconds:   2,
		RelayFreshnessSeconds:      45,
		NativePlayerLevel:          70,
		NativeCharacterLevel:       30,
		NativeOwnershipMode:        "full",
	},
	MatchLobby: MatchLobbyConfig{
		StrictRosterV1Enabled:    false,
		LockedGameSHA256:         "181c49ffb522b3eb01014c84fd9d3a2a5c0b66ae80a6a6addff4bdd6f8125843",
		PresenceGraceSeconds:     60,
		ProvisioningSeconds:      120,
		InitialConnectionSeconds: 120,
		P2PHostReconnectSeconds:  120,
		AdmissionGrantTTLSeconds: 60,
		SweepIntervalSeconds:     5,
		AdmissionSigningKeyID:    "match-admission-dev-ephemeral",
	},
	P2PRoom: P2PRoomConfig{
		HeartbeatIntervalSeconds: 15,
		StaleAfterSeconds:        45,
		ClosedAfterSeconds:       90,
		SweepIntervalSeconds:     5,
		MaximumPlayers:           64,
	},
	P2PBattleLog: P2PBattleLogConfig{
		Enabled:                   false,
		ShadowMode:                true,
		PolicyVersion:             "p2p-v1",
		MaxReportBytes:            512 * 1024,
		MaxEvents:                 4096,
		CollectionDeadlineSeconds: 300,
		HardExpiryHours:           8,
		CapabilityTTLHours:        24,
		FinalizerIntervalSeconds:  5,
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
	VNT: VNTConfig{
		CredentialRotationGraceSeconds:          60,
		EnrollmentRequestsPerPlayerPerHour:      5,
		DirectoryRequestsPerIPPerMinute:         120,
		BootstrapRequestsPerPlayerPerMinute:     30,
		HeartbeatRequestsPerCredentialPerMinute: 120,
		ManagementRequestsPerCredentialPerHour:  10,
		MaxNodesPerPlayer:                       3,
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
		VNTRoomsEnabled:      false,
	},
	Downloads: DownloadConfig{
		Enabled:                  false,
		S3Region:                 "auto",
		AllowedExtensions:        []string{"exe", "msi", "zip", "7z", "pdf", "md", "txt", "docx"},
		MaxFileBytes:             2 << 30,
		MultipartThresholdBytes:  64 << 20,
		PartSizeBytes:            16 << 20,
		UploadSessionTTLHours:    24,
		PresignTTLMinutes:        15,
		VerificationIntervalSecs: 5,
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
	overrideString("ACCESS_TOKEN_PUBLIC_KEY_BASE64", &c.Auth.AccessTokenPublicKeyBase64)
	overrideString("ACCESS_TOKEN_KEY_ID", &c.Auth.AccessTokenKeyID)
	overrideString("DEVICE_FINGERPRINT_KEY_ID", &c.Auth.DeviceFingerprintKeyID)
	overrideString("DEVICE_FINGERPRINT_HMAC_KEY_BASE64", &c.Auth.DeviceFingerprintHMACKeyBase64)
	overrideString("STEAM_TICKET_VERIFIER_PATH", &c.Auth.TicketVerifierExecutable)
	overrideString("TOOLBOX_PUBKEY_PATH", &c.Auth.IntegrityPublicKeyPath)
	overrideString("TOOLBOX_PUBKEY", &c.Auth.IntegrityPublicKeyPEM)
	overrideString("ADMIN_TOKENS", &c.Admin.TokenSet)
	overrideString("ADMIN_ACCESS_TOKEN_PRIVATE_KEY_BASE64", &c.Admin.AccessTokenPrivateKeyBase64)
	overrideString("ADMIN_ACCESS_TOKEN_PUBLIC_KEY_BASE64", &c.Admin.AccessTokenPublicKeyBase64)
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
	overrideString("META_HTTP_ADDR", &c.MetaServer.HTTPAddr)
	overrideString("META_LOGIC_ADDR", &c.MetaServer.LogicAddr)
	overrideString("META_PUBLIC_HTTP_BASE_URL", &c.MetaServer.PublicHTTPBaseURL)
	overrideString("META_PUBLIC_LOGIC_ENDPOINT", &c.MetaServer.PublicLogicEndpoint)
	overrideBool("META_LOGIC_PROXY_PROTOCOL", &c.MetaServer.LogicProxyProtocol)
	overrideInt("META_PROTOCOL_VERSION", &c.MetaServer.ProtocolVersion)
	overrideInt("META_GATE_TICKET_TTL_SECONDS", &c.MetaServer.GateTicketTTLSeconds)
	overrideInt("META_MAX_FRAME_BYTES", &c.MetaServer.MaxFrameBytes)
	overrideInt("META_MAX_WRITE_QUEUE_BYTES", &c.MetaServer.MaxWriteQueueBytes)
	overrideInt("META_HANDSHAKE_TIMEOUT_SECONDS", &c.MetaServer.HandshakeTimeoutSeconds)
	overrideInt("META_FRAME_TIMEOUT_SECONDS", &c.MetaServer.FrameTimeoutSeconds)
	overrideInt("META_IDLE_TIMEOUT_SECONDS", &c.MetaServer.IdleTimeoutSeconds)
	overrideInt("META_MAX_CONNECTIONS_PER_IP", &c.MetaServer.MaxConnectionsPerIP)
	overrideInt("META_CONNECTIONS_PER_IP_PER_MINUTE", &c.MetaServer.ConnectionsPerIPPerMinute)
	overrideInt("META_RPC_CALLS_PER_PLAYER_PER_MINUTE", &c.MetaServer.RPCCallsPerPlayerPerMinute)
	overrideInt("META_MATCH_TICKET_TTL_SECONDS", &c.MetaServer.MatchTicketTTLSeconds)
	overrideInt("META_MATCH_RESERVATION_TTL_SECONDS", &c.MetaServer.MatchReservationTTLSeconds)
	overrideInt("META_SCHEDULER_INTERVAL_SECONDS", &c.MetaServer.SchedulerIntervalSeconds)
	overrideInt("META_RELAY_FRESHNESS_SECONDS", &c.MetaServer.RelayFreshnessSeconds)
	overrideInt("META_NATIVE_PLAYER_LEVEL", &c.MetaServer.NativePlayerLevel)
	overrideInt("META_NATIVE_CHARACTER_LEVEL", &c.MetaServer.NativeCharacterLevel)
	overrideString("META_NATIVE_OWNERSHIP_MODE", &c.MetaServer.NativeOwnershipMode)
	overrideBool("META_DEVELOPMENT_LEGACY_LOADOUT_API", &c.MetaServer.DevelopmentLegacyLoadoutAPI)
	overrideInt("MATCH_LOBBY_PRESENCE_GRACE_SECONDS", &c.MatchLobby.PresenceGraceSeconds)
	overrideBool("STRICT_ROSTER_V1_ENABLED", &c.MatchLobby.StrictRosterV1Enabled)
	overrideString("STRICT_ROSTER_LOCKED_GAME_SHA256", &c.MatchLobby.LockedGameSHA256)
	overrideInt("MATCH_LOBBY_PROVISIONING_SECONDS", &c.MatchLobby.ProvisioningSeconds)
	overrideInt("MATCH_LOBBY_INITIAL_CONNECTION_SECONDS", &c.MatchLobby.InitialConnectionSeconds)
	overrideInt("MATCH_LOBBY_P2P_HOST_RECONNECT_SECONDS", &c.MatchLobby.P2PHostReconnectSeconds)
	overrideInt("MATCH_ADMISSION_GRANT_TTL_SECONDS", &c.MatchLobby.AdmissionGrantTTLSeconds)
	overrideInt("MATCH_LOBBY_SWEEP_INTERVAL_SECONDS", &c.MatchLobby.SweepIntervalSeconds)
	overrideString("MATCH_ADMISSION_SIGNING_KEY_ID", &c.MatchLobby.AdmissionSigningKeyID)
	overrideString("MATCH_ADMISSION_PRIVATE_KEY_BASE64", &c.MatchLobby.AdmissionPrivateKeyBase64)
	overrideInt("REDIS_DB", &c.Redis.DB)
	overrideInt("HTTP_RATE_LIMIT_BURST", &c.RateLimit.Burst)
	overrideInt("AUTH_BIND_REQUESTS_PER_MINUTE", &c.Auth.BindRequestsPerMinute)
	overrideInt("AUTH_BIND_BURST", &c.Auth.BindBurst)
	overrideInt("AUTH_BIND_PER_IP_PER_MINUTE", &c.Auth.BindRateLimit.PerIPPerMinute)
	overrideInt("AUTH_BIND_PER_DEVICE_PER_MINUTE", &c.Auth.BindRateLimit.PerDevicePerMinute)
	overrideInt("AUTH_BIND_PER_STEAM_ID_PER_MINUTE", &c.Auth.BindRateLimit.PerSteamIDPerMinute)
	overrideBool("AUTH_INVITE_REQUIRED", &c.Auth.InviteRequired)
	overrideUint32("STEAM_APP_ID", &c.Auth.SteamAppID)
	overrideInt("STEAM_TICKET_VERIFIER_TIMEOUT_SECONDS", &c.Auth.TicketVerifierTimeoutSeconds)
	overrideInt("STEAM_TICKET_MAXIMUM_AGE_SECONDS", &c.Auth.TicketMaximumAgeSeconds)
	overrideInt("STEAM_TICKET_CLOCK_SKEW_SECONDS", &c.Auth.TicketClockSkewSeconds)
	overrideInt("STEAM_TICKET_MAXIMUM_HEX_BYTES", &c.Auth.TicketMaximumHexBytes)
	overrideInt("STEAM_TICKET_MAXIMUM_OUTPUT_BYTES", &c.Auth.TicketMaximumOutputBytes)
	overrideInt("INTEGRITY_CHALLENGE_TTL_SECONDS", &c.Auth.IntegrityChallengeTTLSeconds)
	overrideInt("INTEGRITY_MAXIMUM_FAILURES", &c.Auth.IntegrityMaximumFailures)
	overrideInt("CONNECTION_SESSION_TTL_SECONDS", &c.Connection.SessionTTLSeconds)
	overrideInt("CONNECTION_SWEEP_INTERVAL_SECONDS", &c.Connection.SweepIntervalSeconds)
	overrideInt("CONNECTION_WEBSOCKET_QUEUE_SIZE", &c.Connection.WebSocketQueueSize)
	overrideInt("CONNECTION_WEBSOCKET_MAX_MESSAGE_BYTES", &c.Connection.WebSocketMaxBytes)
	overrideBool("P2P_BATTLELOG_ENABLED", &c.P2PBattleLog.Enabled)
	overrideBool("P2P_BATTLELOG_SHADOW_MODE", &c.P2PBattleLog.ShadowMode)
	overrideString("P2P_BATTLELOG_POLICY_VERSION", &c.P2PBattleLog.PolicyVersion)
	overrideInt("P2P_BATTLELOG_MAX_REPORT_BYTES", &c.P2PBattleLog.MaxReportBytes)
	overrideInt("P2P_BATTLELOG_MAX_EVENTS", &c.P2PBattleLog.MaxEvents)
	overrideInt("P2P_BATTLELOG_COLLECTION_DEADLINE_SECONDS", &c.P2PBattleLog.CollectionDeadlineSeconds)
	overrideInt("P2P_BATTLELOG_HARD_EXPIRY_HOURS", &c.P2PBattleLog.HardExpiryHours)
	overrideInt("P2P_BATTLELOG_CAPABILITY_TTL_HOURS", &c.P2PBattleLog.CapabilityTTLHours)
	overrideInt("P2P_BATTLELOG_FINALIZER_INTERVAL_SECONDS", &c.P2PBattleLog.FinalizerIntervalSeconds)
	overrideString("GAME_SERVER_CA_CERT_PEM_BASE64", &c.GameServer.CACertificatePEMBase64)
	overrideString("GAME_SERVER_CA_KEY_PEM_BASE64", &c.GameServer.CAPrivateKeyPEMBase64)
	overrideInt("GAME_SERVER_CERTIFICATE_TTL_HOURS", &c.GameServer.CertificateTTLHours)
	overrideInt("GAME_SERVER_SIGNATURE_MAX_CLOCK_SKEW_SECONDS", &c.GameServer.SignatureMaxClockSkewSeconds)
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
	overrideBool("DOWNLOADS_ENABLED", &c.Downloads.Enabled)
	overrideString("DOWNLOAD_S3_ENDPOINT", &c.Downloads.S3Endpoint)
	overrideString("DOWNLOAD_S3_UPLOAD_ENDPOINT", &c.Downloads.S3UploadEndpoint)
	overrideString("DOWNLOAD_S3_REGION", &c.Downloads.S3Region)
	overrideString("DOWNLOAD_S3_BUCKET", &c.Downloads.S3Bucket)
	overrideString("DOWNLOAD_S3_ACCESS_KEY_ID", &c.Downloads.S3AccessKeyID)
	overrideString("DOWNLOAD_S3_SECRET_ACCESS_KEY", &c.Downloads.S3SecretAccessKey)
	overrideString("DOWNLOAD_PUBLIC_BASE_URL", &c.Downloads.PublicBaseURL)
	overrideString("DOWNLOAD_PUBLIC_PROBE_BASE_URL", &c.Downloads.PublicProbeBaseURL)
	overrideInt64("DOWNLOAD_MAX_FILE_BYTES", &c.Downloads.MaxFileBytes)
	overrideInt64("DOWNLOAD_MULTIPART_THRESHOLD_BYTES", &c.Downloads.MultipartThresholdBytes)
	overrideInt64("DOWNLOAD_PART_SIZE_BYTES", &c.Downloads.PartSizeBytes)
	overrideInt("DOWNLOAD_UPLOAD_SESSION_TTL_HOURS", &c.Downloads.UploadSessionTTLHours)
	overrideInt("DOWNLOAD_PRESIGN_TTL_MINUTES", &c.Downloads.PresignTTLMinutes)
	overrideInt("DOWNLOAD_VERIFICATION_INTERVAL_SECONDS", &c.Downloads.VerificationIntervalSecs)
	if raw := os.Getenv("DOWNLOAD_ALLOWED_EXTENSIONS"); raw != "" {
		c.Downloads.AllowedExtensions = splitCSV(raw)
	}
	overrideBool("VNT_ROOMS_ENABLED", &c.Update.VNTRoomsEnabled)
	if raw := os.Getenv("VNT_ALLOWED_VNTS_VERSIONS"); raw != "" {
		c.VNT.AllowedVNTSVersions = splitCSV(raw)
	}
	if raw := os.Getenv("VNT_ALLOWED_WRAPPER_VERSIONS"); raw != "" {
		c.VNT.AllowedWrapperVersions = splitCSV(raw)
	}
	overrideInt("VNT_CREDENTIAL_ROTATION_GRACE_SECONDS", &c.VNT.CredentialRotationGraceSeconds)
	overrideInt("VNT_ENROLLMENT_REQUESTS_PER_PLAYER_PER_HOUR", &c.VNT.EnrollmentRequestsPerPlayerPerHour)
	overrideInt("VNT_DIRECTORY_REQUESTS_PER_IP_PER_MINUTE", &c.VNT.DirectoryRequestsPerIPPerMinute)
	overrideInt("VNT_BOOTSTRAP_REQUESTS_PER_PLAYER_PER_MINUTE", &c.VNT.BootstrapRequestsPerPlayerPerMinute)
	overrideInt("VNT_HEARTBEAT_REQUESTS_PER_CREDENTIAL_PER_MINUTE", &c.VNT.HeartbeatRequestsPerCredentialPerMinute)
	overrideInt("VNT_MANAGEMENT_REQUESTS_PER_CREDENTIAL_PER_HOUR", &c.VNT.ManagementRequestsPerCredentialPerHour)
	overrideInt("VNT_MAX_NODES_PER_PLAYER", &c.VNT.MaxNodesPerPlayer)
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

func overrideInt64(name string, target *int64) {
	if raw := os.Getenv(name); raw != "" {
		if value, err := strconv.ParseInt(raw, 10, 64); err == nil {
			*target = value
		}
	}
}

func overrideUint32(name string, target *uint32) {
	if raw := os.Getenv(name); raw != "" {
		if value, err := strconv.ParseUint(raw, 10, 32); err == nil {
			*target = uint32(value)
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
	if strings.TrimSpace(c.Auth.TicketVerifierExecutable) == "" ||
		c.Auth.TicketVerifierTimeoutSeconds < 1 || c.Auth.TicketMaximumHexBytes < 2 ||
		c.Auth.TicketMaximumOutputBytes < 128 {
		errs = append(errs, errors.New("Steam encrypted ticket settings are invalid"))
	}
	if c.Auth.IntegrityChallengeTTLSeconds < 1 || c.Auth.IntegrityMaximumFailures < 1 {
		errs = append(errs, errors.New("integrity challenge settings are invalid"))
	}
	if strings.EqualFold(c.Environment, "production") &&
		strings.TrimSpace(c.Auth.IntegrityPublicKeyPath) == "" &&
		strings.TrimSpace(c.Auth.IntegrityPublicKeyPEM) == "" {
		errs = append(errs, errors.New("TOOLBOX_PUBKEY_PATH or TOOLBOX_PUBKEY is required in production"))
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
		c.GameServer.RegistrationTokenTTLMinutes < 1 ||
		c.GameServer.ServerTokenTTLHours < 1 ||
		c.GameServer.ServerTokenRotationGraceSeconds < 1 ||
		c.GameServer.CertificateTTLHours < 1 ||
		c.GameServer.SignatureMaxClockSkewSeconds < 10 ||
		c.GameServer.SignatureMaxClockSkewSeconds > 300 ||
		c.GameServer.SweepIntervalSeconds < 1 {
		errs = append(errs, errors.New("game_server timing, certificate, and token settings are invalid"))
	}
	if (strings.TrimSpace(c.GameServer.CACertificatePEMBase64) == "") !=
		(strings.TrimSpace(c.GameServer.CAPrivateKeyPEMBase64) == "") {
		errs = append(errs, errors.New("game server CA certificate and private key must be configured together"))
	}
	if c.P2PRoom.HeartbeatIntervalSeconds < 1 ||
		c.P2PRoom.StaleAfterSeconds <= c.P2PRoom.HeartbeatIntervalSeconds ||
		c.P2PRoom.ClosedAfterSeconds <= c.P2PRoom.StaleAfterSeconds ||
		c.P2PRoom.SweepIntervalSeconds < 1 || c.P2PRoom.MaximumPlayers < 2 || c.P2PRoom.MaximumPlayers > 64 {
		errs = append(errs, errors.New("p2p_room timing and capacity settings are invalid"))
	}
	if c.MatchLobby.PresenceGraceSeconds < 10 ||
		c.MatchLobby.ProvisioningSeconds < 30 ||
		c.MatchLobby.InitialConnectionSeconds < 30 ||
		c.MatchLobby.P2PHostReconnectSeconds < 30 ||
		c.MatchLobby.AdmissionGrantTTLSeconds < 15 ||
		c.MatchLobby.AdmissionGrantTTLSeconds > c.MatchLobby.InitialConnectionSeconds ||
		c.MatchLobby.SweepIntervalSeconds < 1 ||
		strings.TrimSpace(c.MatchLobby.AdmissionSigningKeyID) == "" {
		errs = append(errs, errors.New("match_lobby timing and admission signing settings are invalid"))
	}
	if c.MatchLobby.StrictRosterV1Enabled {
		lockedHash := strings.ToLower(strings.TrimSpace(c.MatchLobby.LockedGameSHA256))
		if len(lockedHash) != 64 || strings.Trim(lockedHash, "0123456789abcdef") != "" {
			errs = append(errs, errors.New("strict_roster_v1 requires a 64-character locked game SHA-256"))
		}
		if strings.TrimSpace(c.MatchLobby.AdmissionPrivateKeyBase64) == "" {
			errs = append(errs, errors.New("strict_roster_v1 requires MATCH_ADMISSION_PRIVATE_KEY_BASE64 in every environment"))
		}
	}
	if strings.TrimSpace(c.P2PBattleLog.PolicyVersion) == "" ||
		c.P2PBattleLog.MaxReportBytes < 16*1024 ||
		int64(c.P2PBattleLog.MaxReportBytes) > c.HTTP.MaxRequestBodyBytes ||
		c.P2PBattleLog.MaxEvents < 1 || c.P2PBattleLog.MaxEvents > 100000 ||
		c.P2PBattleLog.CollectionDeadlineSeconds < 30 ||
		c.P2PBattleLog.HardExpiryHours < 1 || c.P2PBattleLog.HardExpiryHours > 168 ||
		c.P2PBattleLog.CapabilityTTLHours < c.P2PBattleLog.HardExpiryHours ||
		c.P2PBattleLog.CapabilityTTLHours > 168 ||
		c.P2PBattleLog.FinalizerIntervalSeconds < 1 {
		errs = append(errs, errors.New("p2p_battlelog limits and timing settings are invalid"))
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
		(c.Update.DefaultChannel != "stable" && c.Update.DefaultChannel != "beta" && c.Update.DefaultChannel != "toolbox") ||
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
	if c.Downloads.Enabled {
		endpoint, endpointErr := url.Parse(c.Downloads.S3Endpoint)
		uploadEndpoint, uploadEndpointErr := url.Parse(c.Downloads.UploadEndpoint())
		publicURL, publicErr := url.Parse(c.Downloads.PublicBaseURL)
		publicProbeURL, publicProbeErr := url.Parse(c.Downloads.PublicProbeBase())
		if endpointErr != nil || endpoint.Host == "" || (endpoint.Scheme != "https" && endpoint.Scheme != "http") ||
			uploadEndpointErr != nil || uploadEndpoint.Host == "" || (uploadEndpoint.Scheme != "https" && uploadEndpoint.Scheme != "http") ||
			publicErr != nil || publicURL.Host == "" || (publicURL.Scheme != "https" && publicURL.Scheme != "http") ||
			publicProbeErr != nil || publicProbeURL.Host == "" || (publicProbeURL.Scheme != "https" && publicProbeURL.Scheme != "http") ||
			strings.TrimSpace(c.Downloads.S3Region) == "" || strings.TrimSpace(c.Downloads.S3Bucket) == "" ||
			strings.TrimSpace(c.Downloads.S3AccessKeyID) == "" || strings.TrimSpace(c.Downloads.S3SecretAccessKey) == "" ||
			!validDownloadExtensions(c.Downloads.AllowedExtensions) || c.Downloads.MaxFileBytes < 1 || c.Downloads.MaxFileBytes > 2<<30 ||
			c.Downloads.MultipartThresholdBytes < 1 || c.Downloads.MultipartThresholdBytes > c.Downloads.MaxFileBytes ||
			c.Downloads.PartSizeBytes < 5<<20 || c.Downloads.PartSizeBytes > 128<<20 ||
			c.Downloads.UploadSessionTTLHours < 1 || c.Downloads.UploadSessionTTLHours > 168 ||
			c.Downloads.PresignTTLMinutes < 1 || c.Downloads.PresignTTLMinutes > 60 ||
			c.Downloads.VerificationIntervalSecs < 1 || c.Downloads.VerificationIntervalSecs > 300 {
			errs = append(errs, errors.New("download storage, limits, or timing settings are invalid"))
		}
		if strings.EqualFold(c.Environment, "production") &&
			(!secureDownloadStorageEndpoint(endpoint) || uploadEndpoint.Scheme != "https" || publicURL.Scheme != "https" ||
				!secureDownloadStorageEndpoint(publicProbeURL)) {
			errs = append(errs, errors.New("secure download storage and public URLs are required in production"))
		}
	}
	if c.VNT.CredentialRotationGraceSeconds < 1 || c.VNT.CredentialRotationGraceSeconds > 600 {
		errs = append(errs, errors.New("vnt credential rotation grace must be between 1 and 600 seconds"))
	}
	if c.VNT.EnrollmentRequestsPerPlayerPerHour < 1 || c.VNT.EnrollmentRequestsPerPlayerPerHour > 100 ||
		c.VNT.DirectoryRequestsPerIPPerMinute < 1 || c.VNT.DirectoryRequestsPerIPPerMinute > 10_000 ||
		c.VNT.BootstrapRequestsPerPlayerPerMinute < 1 || c.VNT.BootstrapRequestsPerPlayerPerMinute > 1_000 ||
		c.VNT.HeartbeatRequestsPerCredentialPerMinute < 1 || c.VNT.HeartbeatRequestsPerCredentialPerMinute > 10_000 ||
		c.VNT.ManagementRequestsPerCredentialPerHour < 1 || c.VNT.ManagementRequestsPerCredentialPerHour > 1_000 {
		errs = append(errs, errors.New("vnt per-IP, per-player, and per-credential rate limits are invalid"))
	}
	if c.VNT.MaxNodesPerPlayer < 1 || c.VNT.MaxNodesPerPlayer > 100 {
		errs = append(errs, errors.New("vnt max nodes per player must be between 1 and 100"))
	}
	if len(errs) > 0 {
		return fmt.Errorf("invalid control-plane configuration: %w", errors.Join(errs...))
	}
	return nil
}

func secureDownloadStorageEndpoint(endpoint *url.URL) bool {
	if endpoint.Scheme == "https" {
		return true
	}
	if endpoint.Scheme != "http" {
		return false
	}
	host := strings.ToLower(endpoint.Hostname())
	if host == "minio" || host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast())
}

func validDownloadExtensions(values []string) bool {
	if len(values) == 0 {
		return false
	}
	seen := make(map[string]struct{}, len(values))
	for _, raw := range values {
		value := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(raw)), ".")
		if value == "" || len(value) > 16 || strings.IndexFunc(value, func(r rune) bool {
			return !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'))
		}) >= 0 {
			return false
		}
		if _, exists := seen[value]; exists {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func (c *Config) ValidateMetaServer() error {
	var errs []error
	if parsed, err := url.Parse(c.Database.URL); err != nil ||
		parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" {
		errs = append(errs, errors.New("database.url must be a PostgreSQL URL"))
	}
	if strings.TrimSpace(c.Redis.Address) == "" ||
		c.Database.MaxConnections < 1 || c.Database.MinConnections < 0 ||
		c.Database.MinConnections > c.Database.MaxConnections {
		errs = append(errs, errors.New("MetaServer database and Redis settings are invalid"))
	}
	if c.RateLimit.RequestsPerSecond <= 0 || c.RateLimit.Burst < 1 ||
		c.HTTP.MaxRequestBodyBytes < 1 {
		errs = append(errs, errors.New("MetaServer HTTP and rate settings are invalid"))
	}
	if strings.TrimSpace(c.Auth.Issuer) == "" || strings.TrimSpace(c.Auth.Audience) == "" ||
		strings.TrimSpace(c.Auth.AccessTokenKeyID) == "" ||
		c.Auth.AccessTokenTTLMinutes < 1 || c.Auth.RefreshTokenTTLDays < 1 {
		errs = append(errs, errors.New("MetaServer player token settings are invalid"))
	}
	if len(c.Admin.TrustedCIDRs) == 0 || strings.TrimSpace(c.Admin.Issuer) == "" ||
		strings.TrimSpace(c.Admin.Audience) == "" || strings.TrimSpace(c.Admin.AccessTokenKeyID) == "" ||
		c.Admin.AccessTokenTTLMinutes < 1 || c.Admin.RefreshTokenTTLDays < 1 ||
		c.Admin.LoginPerIPPerMinute < 1 || c.Admin.LoginPerUsernamePerMinute < 1 {
		errs = append(errs, errors.New("MetaServer administrator token settings are invalid"))
	}
	if strings.EqualFold(c.Environment, "production") &&
		(strings.TrimSpace(c.Auth.AccessTokenPublicKeyBase64) == "" ||
			strings.TrimSpace(c.Admin.AccessTokenPublicKeyBase64) == "") {
		errs = append(errs, errors.New("MetaServer player/admin token verification keys are required in production"))
	}
	if strings.EqualFold(c.Environment, "production") && !c.MetaServer.LogicProxyProtocol {
		errs = append(errs, errors.New("MetaServer Logic PROXY protocol is required in production"))
	}
	if strings.TrimSpace(c.MetaServer.HTTPAddr) == "" || strings.TrimSpace(c.MetaServer.LogicAddr) == "" {
		errs = append(errs, errors.New("meta_server HTTP and logic listen addresses are required"))
	}
	publicHTTP, err := url.Parse(c.MetaServer.PublicHTTPBaseURL)
	if err != nil || publicHTTP.Host == "" || publicHTTP.Scheme != "https" {
		errs = append(errs, errors.New("meta_server.public_http_base_url must be an absolute HTTPS URL"))
	}
	if _, _, err := net.SplitHostPort(c.MetaServer.PublicLogicEndpoint); err != nil {
		errs = append(errs, errors.New("meta_server.public_logic_endpoint must be host:port"))
	}
	if c.MetaServer.ProtocolVersion < 1 ||
		c.MetaServer.GateTicketTTLSeconds != 60 ||
		c.MetaServer.MaxFrameBytes < 1024 || c.MetaServer.MaxFrameBytes > 2<<20 ||
		c.MetaServer.MaxWriteQueueBytes < c.MetaServer.MaxFrameBytes ||
		c.MetaServer.HandshakeTimeoutSeconds < 1 || c.MetaServer.FrameTimeoutSeconds < 1 ||
		c.MetaServer.IdleTimeoutSeconds < c.MetaServer.FrameTimeoutSeconds ||
		c.MetaServer.MaxConnectionsPerIP < 1 || c.MetaServer.ConnectionsPerIPPerMinute < 1 ||
		c.MetaServer.RPCCallsPerPlayerPerMinute < 1 || c.MetaServer.MatchTicketTTLSeconds < 30 ||
		c.MetaServer.MatchReservationTTLSeconds < 10 ||
		c.MetaServer.SchedulerIntervalSeconds < 1 || c.MetaServer.RelayFreshnessSeconds < 1 ||
		c.MetaServer.NativePlayerLevel < 1 || c.MetaServer.NativePlayerLevel > 127 ||
		c.MetaServer.NativeCharacterLevel < 1 || c.MetaServer.NativeCharacterLevel > 127 ||
		!validNativeOwnershipMode(c.MetaServer.NativeOwnershipMode) {
		errs = append(errs, errors.New("meta_server protocol, timeout, queue, or rate settings are invalid"))
	}
	if strings.EqualFold(c.Environment, "production") && c.MetaServer.DevelopmentLegacyLoadoutAPI {
		errs = append(errs, errors.New("legacy loadout API cannot be enabled in production"))
	}
	if len(errs) > 0 {
		return fmt.Errorf("invalid MetaServer configuration: %w", errors.Join(errs...))
	}
	return nil
}

func validNativeOwnershipMode(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "compact", "full":
		return true
	default:
		return false
	}
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
		AccessTokenPublicKeyBase64:  c.AccessTokenPublicKeyBase64,
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

func (c GameServerConfig) RegistrationTokenTTL() time.Duration {
	return time.Duration(c.RegistrationTokenTTLMinutes) * time.Minute
}

func (c GameServerConfig) ServerTokenRotationGrace() time.Duration {
	return time.Duration(c.ServerTokenRotationGraceSeconds) * time.Second
}

func (c GameServerConfig) CertificateTTL() time.Duration {
	return time.Duration(c.CertificateTTLHours) * time.Hour
}

func (c GameServerConfig) SignatureMaxClockSkew() time.Duration {
	return time.Duration(c.SignatureMaxClockSkewSeconds) * time.Second
}

func (c GameServerConfig) SweepInterval() time.Duration {
	return time.Duration(c.SweepIntervalSeconds) * time.Second
}

func (c VNTConfig) CredentialRotationGrace() time.Duration {
	return time.Duration(c.CredentialRotationGraceSeconds) * time.Second
}

func (c P2PRoomConfig) SweepInterval() time.Duration {
	return time.Duration(c.SweepIntervalSeconds) * time.Second
}

func (c P2PBattleLogConfig) CollectionDeadline() time.Duration {
	return time.Duration(c.CollectionDeadlineSeconds) * time.Second
}

func (c P2PBattleLogConfig) HardExpiry() time.Duration {
	return time.Duration(c.HardExpiryHours) * time.Hour
}

func (c P2PBattleLogConfig) CapabilityTTL() time.Duration {
	return time.Duration(c.CapabilityTTLHours) * time.Hour
}

func (c P2PBattleLogConfig) FinalizerInterval() time.Duration {
	return time.Duration(c.FinalizerIntervalSeconds) * time.Second
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

func (c DownloadConfig) UploadSessionTTL() time.Duration {
	return time.Duration(c.UploadSessionTTLHours) * time.Hour
}

func (c DownloadConfig) UploadEndpoint() string {
	if endpoint := strings.TrimSpace(c.S3UploadEndpoint); endpoint != "" {
		return endpoint
	}
	return strings.TrimSpace(c.S3Endpoint)
}

func (c DownloadConfig) PublicProbeBase() string {
	if endpoint := strings.TrimSpace(c.PublicProbeBaseURL); endpoint != "" {
		return endpoint
	}
	return strings.TrimSpace(c.PublicBaseURL)
}

func (c DownloadConfig) PresignTTL() time.Duration {
	return time.Duration(c.PresignTTLMinutes) * time.Minute
}

func (c DownloadConfig) VerificationInterval() time.Duration {
	return time.Duration(c.VerificationIntervalSecs) * time.Second
}
