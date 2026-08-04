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
	PlayerID      string
	AccountStatus string
	SteamVerified bool
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
	Limit  int
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

func (n Node) Public() PublicNode {
	return PublicNode{
		NodeID: n.ID, Host: n.AdvertisedHost, Port: n.Port,
		Region: n.Region, Location: n.Location, Status: n.State,
		VNTSVersion: n.VNTSVersion, WrapperVersion: n.WrapperVersion,
		ServerKeyFingerprint: n.ServerKeyFingerprint,
		SupportedTransports:  append([]string(nil), n.SupportedTransports...),
		CapacityAvailable:    max(0, n.MaxRooms-n.ActiveRooms),
		VersionCompatible:    true, LastReachableAt: n.LastReachableAt,
	}
}
