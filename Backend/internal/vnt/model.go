package vnt

import "time"

const (
	StateRegistering = "REGISTERING"
	StateOnline      = "ONLINE"
	StateStale       = "STALE"
	StateOffline     = "OFFLINE"
	StateDraining    = "DRAINING"
	StateRevoked     = "REVOKED"
	StateRetired     = "RETIRED"
)

type Actor struct {
	PlayerID         string
	AccountStatus    string
	SteamVerified    bool
	IntegrityTrusted bool
}

type EnrollmentResult struct {
	Code      string
	ExpiresAt time.Time
}

type RegisterInput struct {
	AdvertisedHost       string
	Port                 int
	Region               string
	Location             string
	VNTSVersion          string
	WrapperVersion       string
	ServerKeyFingerprint string
	SupportedTransports  []string
	MaxRooms             int
}

type Node struct {
	ID                   string
	OwnerPlayerID        string
	AdvertisedHost       string
	Port                 int
	Region               string
	Location             string
	State                string
	VNTSVersion          string
	WrapperVersion       string
	ServerKeyFingerprint string
	SupportedTransports  []string
	MaxRooms             int
	ReportedSessions     int
	ActiveRooms          int
	LastHeartbeatAt      *time.Time
	LastReachableAt      *time.Time
	CreatedAt            time.Time
	UpdatedAt            time.Time
	RetiredAt            *time.Time
}

type RegisterResult struct {
	NodeID                   string
	NodeToken                string
	State                    string
	HeartbeatIntervalSeconds int
	CredentialExpiresAt      time.Time
}

type CredentialRotationResult struct {
	NodeToken           string
	CredentialExpiresAt time.Time
	PreviousValidUntil  time.Time
}

type HeartbeatInput struct {
	WrapperVersion       string
	VNTSVersion          string
	UptimeSeconds        int64
	ReportedSessions     int
	ServerProcessHealthy bool
}

type ListFilter struct {
	Status string
	Region string
	Cursor string
	Limit  int
}

type ListResult struct {
	Items      []PublicNode
	NextCursor string
}

type OwnedListFilter struct {
	Status string
	Cursor string
	Limit  int
}

type OwnedListResult struct {
	Items      []OwnedNode
	NextCursor string
}

type OwnedNode struct {
	NodeID               string     `json:"node_id"`
	Host                 string     `json:"host"`
	Port                 int        `json:"port"`
	Region               string     `json:"region"`
	Location             string     `json:"location"`
	State                string     `json:"state"`
	VNTSVersion          string     `json:"vnts_version"`
	WrapperVersion       string     `json:"wrapper_version"`
	ServerKeyFingerprint string     `json:"server_key_fingerprint"`
	SupportedTransports  []string   `json:"supported_transports"`
	MaxRooms             int        `json:"max_rooms"`
	ReportedSessions     int        `json:"reported_sessions"`
	ActiveRooms          int        `json:"active_rooms"`
	VersionCompatible    bool       `json:"version_compatible"`
	CredentialExpiresAt  *time.Time `json:"credential_expires_at"`
	CredentialLastUsedAt *time.Time `json:"credential_last_used_at"`
	CredentialRevokedAt  *time.Time `json:"credential_revoked_at"`
	LastHeartbeatAt      *time.Time `json:"last_heartbeat_at"`
	LastReachableAt      *time.Time `json:"last_reachable_at"`
	CreatedAt            time.Time  `json:"created_at"`
	UpdatedAt            time.Time  `json:"updated_at"`
	RetiredAt            *time.Time `json:"retired_at"`
}

type AdminListFilter struct {
	State         string
	Region        string
	OwnerPlayerID string
	Cursor        string
	Limit         int
}

type AdminListResult struct {
	Items      []AdminNode
	NextCursor string
}

type AdminNode struct {
	Node
	OwnerSteamID             string
	OwnerPersonaName         string
	OwnerAccountStatus       string
	CredentialExpiresAt      *time.Time
	CredentialLastUsedAt     *time.Time
	CredentialRevokedAt      *time.Time
	VersionCompatible        bool
	ReferencedRooms          []AdminRoomReference
	ReferencedRoomsTruncated bool
}

type AdminRoomReference struct {
	RoomID         string    `json:"room_id"`
	RoomState      string    `json:"room_state"`
	SessionState   string    `json:"session_state"`
	Generation     int       `json:"generation"`
	FailureReason  string    `json:"failure_reason"`
	ExpiresAt      time.Time `json:"expires_at"`
	SessionUpdated time.Time `json:"session_updated_at"`
}

func (n AdminNode) Owned() OwnedNode {
	return OwnedNode{
		NodeID: n.ID, Host: n.AdvertisedHost, Port: n.Port, Region: n.Region,
		Location: n.Location, State: n.State, VNTSVersion: n.VNTSVersion,
		WrapperVersion: n.WrapperVersion, ServerKeyFingerprint: n.ServerKeyFingerprint,
		SupportedTransports: append([]string(nil), n.SupportedTransports...),
		MaxRooms:            n.MaxRooms, ReportedSessions: n.ReportedSessions, ActiveRooms: n.ActiveRooms,
		VersionCompatible: n.VersionCompatible, CredentialExpiresAt: n.CredentialExpiresAt,
		CredentialLastUsedAt: n.CredentialLastUsedAt, CredentialRevokedAt: n.CredentialRevokedAt,
		LastHeartbeatAt: n.LastHeartbeatAt, LastReachableAt: n.LastReachableAt,
		CreatedAt: n.CreatedAt, UpdatedAt: n.UpdatedAt, RetiredAt: n.RetiredAt,
	}
}

type PublicNode struct {
	NodeID               string     `json:"node_id"`
	Host                 string     `json:"host"`
	Port                 int        `json:"port"`
	Region               string     `json:"region"`
	Location             string     `json:"location"`
	Status               string     `json:"status"`
	VNTSVersion          string     `json:"vnts_version"`
	WrapperVersion       string     `json:"wrapper_version"`
	ServerKeyFingerprint string     `json:"server_key_fingerprint"`
	SupportedTransports  []string   `json:"supported_transports"`
	CapacityAvailable    int        `json:"capacity_available"`
	VersionCompatible    bool       `json:"version_compatible"`
	LastReachableAt      *time.Time `json:"last_reachable_at"`
}

func (n Node) Public(versionCompatible bool) PublicNode {
	return PublicNode{
		NodeID: n.ID, Host: n.AdvertisedHost, Port: n.Port,
		Region: n.Region, Location: n.Location, Status: n.State,
		VNTSVersion: n.VNTSVersion, WrapperVersion: n.WrapperVersion,
		ServerKeyFingerprint: n.ServerKeyFingerprint,
		SupportedTransports:  append([]string(nil), n.SupportedTransports...),
		CapacityAvailable:    max(0, n.MaxRooms-n.ActiveRooms),
		VersionCompatible:    versionCompatible, LastReachableAt: n.LastReachableAt,
	}
}

func ValidState(value string) bool { return validState(value) }
