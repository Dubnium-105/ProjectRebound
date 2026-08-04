package admin

import "time"

type DashboardSummary struct {
	OnlinePlayers          int64     `json:"online_players"`
	ActiveP2PRooms         int64     `json:"active_p2p_rooms"`
	OnlineGameServers      int64     `json:"online_game_servers"`
	ReadyRelayNodes        int64     `json:"ready_relay_nodes"`
	ActiveRelayAllocations int64     `json:"active_relay_allocations"`
	UnresolvedRiskEvents   int64     `json:"unresolved_risk_events"`
	ActiveInviteCodes      int64     `json:"active_invite_codes"`
	ActiveAdminSessions    int64     `json:"active_admin_sessions"`
	GeneratedAt            time.Time `json:"generated_at"`
}

type DashboardPoint struct {
	BucketStart  time.Time `json:"bucket_start"`
	LoginCount   int64     `json:"login_count"`
	RoomsCreated int64     `json:"rooms_created"`
	RiskEvents   int64     `json:"risk_events"`
}

type DashboardAlert struct {
	ID           string `json:"id"`
	Severity     string `json:"severity"`
	ResourceType string `json:"resource_type"`
	Title        string `json:"title"`
	Summary      string `json:"summary"`
	Count        int64  `json:"count"`
	ResourcePath string `json:"resource_path"`
}

type AdminRiskEvent struct {
	ID             string         `json:"id"`
	PlayerID       string         `json:"player_id"`
	SteamID        string         `json:"steam_id"`
	IPAddress      string         `json:"ip_address"`
	EventType      string         `json:"event_type"`
	Severity       string         `json:"severity"`
	Details        map[string]any `json:"details"`
	CreatedAt      time.Time      `json:"created_at"`
	ResolvedAt     *time.Time     `json:"resolved_at"`
	ResolvedBy     string         `json:"resolved_by"`
	ResolutionNote string         `json:"resolution_note"`
}

type RiskEventFilter struct {
	Cursor         string
	PlayerID       string
	SteamID        string
	EventType      string
	Severity       string
	UnresolvedOnly bool
	Limit          int
}

type AuditFilter struct {
	Cursor     string
	AdminID    string
	Action     string
	TargetType string
	TargetID   string
	Limit      int
}

type AuditEntry struct {
	ID         string         `json:"id"`
	AdminID    string         `json:"admin_id"`
	Action     string         `json:"action"`
	TargetType string         `json:"target_type"`
	TargetID   string         `json:"target_id"`
	OldValue   map[string]any `json:"old_value"`
	NewValue   map[string]any `json:"new_value"`
	Reason     string         `json:"reason"`
	RequestID  string         `json:"request_id"`
	IPAddress  string         `json:"ip_address"`
	UserAgent  string         `json:"user_agent"`
	Result     string         `json:"result"`
	CreatedAt  time.Time      `json:"created_at"`
}

type LoginAuditFilter struct {
	Cursor  string
	AdminID string
	Result  string
	Limit   int
}

type VNTSecurityAuditFilter struct {
	Cursor    string
	EventType string
	Result    string
	ActorType string
	PlayerID  string
	AdminID   string
	NodeID    string
	RoomID    string
	Limit     int
}

type VNTSecurityAuditEntry struct {
	ID         string         `json:"id"`
	EventType  string         `json:"event_type"`
	Result     string         `json:"result"`
	ActorType  string         `json:"actor_type"`
	PlayerID   string         `json:"player_id"`
	AdminID    string         `json:"admin_id"`
	NodeID     string         `json:"node_id"`
	RoomID     string         `json:"room_id"`
	RequestID  string         `json:"request_id"`
	IPAddress  string         `json:"ip_address"`
	UserAgent  string         `json:"user_agent"`
	ReasonCode string         `json:"reason_code"`
	Details    map[string]any `json:"details"`
	CreatedAt  time.Time      `json:"created_at"`
}

type LoginAuditEntry struct {
	ID                       string    `json:"id"`
	AdminID                  string    `json:"admin_id"`
	EventType                string    `json:"event_type"`
	Result                   string    `json:"result"`
	ReasonCode               string    `json:"reason_code"`
	RequestID                string    `json:"request_id"`
	IPAddress                string    `json:"ip_address"`
	UserAgent                string    `json:"user_agent"`
	TurnstileSuccess         *bool     `json:"turnstile_success"`
	TurnstileErrorCodes      []string  `json:"turnstile_error_codes"`
	TurnstileHostname        string    `json:"turnstile_hostname"`
	TurnstileAction          string    `json:"turnstile_action"`
	TurnstileVerifyLatencyMS *int      `json:"turnstile_verify_latency_ms"`
	CreatedAt                time.Time `json:"created_at"`
}

type PlayerSessionEntry struct {
	ID              string     `json:"session_id"`
	DeviceIDSuffix  string     `json:"device_id_suffix"`
	IPAddress       string     `json:"ip_address"`
	TokenFamilyID   string     `json:"token_family_id"`
	CreatedAt       time.Time  `json:"created_at"`
	LastUsedAt      *time.Time `json:"last_used_at"`
	ExpiresAt       time.Time  `json:"expires_at"`
	RevokedAt       *time.Time `json:"revoked_at"`
	RevokedReason   string     `json:"revoked_reason"`
	ReuseDetectedAt *time.Time `json:"reuse_detected_at"`
	Active          bool       `json:"active"`
}

type PlayerLoginEventEntry struct {
	ID          string    `json:"id"`
	SessionID   string    `json:"session_id"`
	IPAddress   string    `json:"ip_address"`
	UserAgent   string    `json:"user_agent"`
	Result      string    `json:"result"`
	FailureCode string    `json:"failure_code"`
	CreatedAt   time.Time `json:"created_at"`
}
