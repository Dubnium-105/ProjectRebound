package matchlobby

import (
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/player"
)

type HostingKind string
type State string
type AttemptState string
type TransportKind string

const (
	HostingDedicated HostingKind = "DEDICATED"
	HostingP2P       HostingKind = "P2P"

	TransportLegacy TransportKind = "LEGACY_RELAY"
	TransportVNT    TransportKind = "VNT"

	StateOpen         State = "OPEN"
	StateFrozen       State = "FROZEN"
	StateProvisioning State = "PROVISIONING"
	StateConnecting   State = "CONNECTING"
	StateRunning      State = "RUNNING"
	StateCompleted    State = "COMPLETED"
	StateAborted      State = "ABORTED"

	AttemptFrozen       AttemptState = "FROZEN"
	AttemptProvisioning AttemptState = "PROVISIONING"
	AttemptConnecting   AttemptState = "CONNECTING"
	AttemptRunning      AttemptState = "RUNNING"
	AttemptCompleted    AttemptState = "COMPLETED"
	AttemptAborted      AttemptState = "ABORTED"
)

type Actor struct {
	PlayerID      string
	AccountStatus player.AccountStatus
	AuthLevel     string
	SteamVerified bool
}

type Lobby struct {
	ID               string
	OwnerPlayerID    string
	P2PRoomID        string
	DisplayName      string
	HostingKind      HostingKind
	TransportKind    TransportKind
	Mode             string
	Region           string
	ClientVersion    string
	ProtocolVersion  int
	TeamOneCapacity  int
	TeamTwoCapacity  int
	State            State
	RosterRevision   int64
	CurrentAttemptID string
	IdempotencyKey   string
	IdempotencyHash  []byte
	CreatedAt        time.Time
	UpdatedAt        time.Time
	ClosedAt         *time.Time
}

type Member struct {
	PlayerID        string    `json:"player_id"`
	DisplayName     string    `json:"display_name"`
	Role            string    `json:"role"`
	TeamID          int       `json:"team_id"`
	TeamSlot        int       `json:"team_slot"`
	Ready           bool      `json:"ready"`
	PresenceState   string    `json:"presence_state"`
	PresenceExpires time.Time `json:"presence_expires_at"`
	JoinedAt        time.Time `json:"joined_at"`
}

type TeamView struct {
	TeamID   int      `json:"team_id"`
	Capacity int      `json:"capacity"`
	Members  []Member `json:"members"`
}

type LocalCapabilities struct {
	IsMember      bool `json:"is_member"`
	IsOwner       bool `json:"is_owner"`
	CanJoin       bool `json:"can_join"`
	CanSwitchTeam bool `json:"can_switch_team"`
	CanSetReady   bool `json:"can_set_ready"`
	CanStart      bool `json:"can_start"`
	CanLeave      bool `json:"can_leave"`
	CanRetry      bool `json:"can_retry_connection"`
}

type AttemptView struct {
	AttemptID          string       `json:"attempt_id"`
	AttemptNumber      int          `json:"attempt_number"`
	State              AttemptState `json:"state"`
	RosterRevision     int64        `json:"roster_revision"`
	RouteGeneration    int          `json:"route_generation"`
	PayloadInstalled   bool         `json:"payload_installed"`
	EndpointHost       string       `json:"endpoint_host,omitempty"`
	EndpointPort       int          `json:"endpoint_port,omitempty"`
	ConnectionDeadline *time.Time   `json:"connection_deadline,omitempty"`
	FailureCode        string       `json:"failure_code,omitempty"`
}

type Snapshot struct {
	LobbyID         string            `json:"lobby_id"`
	OwnerPlayerID   string            `json:"owner_player_id"`
	P2PRoomID       string            `json:"p2p_room_id,omitempty"`
	DisplayName     string            `json:"display_name"`
	HostingKind     HostingKind       `json:"hosting_kind"`
	TransportKind   TransportKind     `json:"transport_kind,omitempty"`
	Mode            string            `json:"mode"`
	Region          string            `json:"region"`
	ClientVersion   string            `json:"client_version"`
	ProtocolVersion int               `json:"protocol_version"`
	State           State             `json:"state"`
	RosterRevision  int64             `json:"roster_revision"`
	Teams           []TeamView        `json:"teams"`
	Attempt         *AttemptView      `json:"attempt,omitempty"`
	Local           LocalCapabilities `json:"local"`
	CreatedAt       time.Time         `json:"created_at"`
	UpdatedAt       time.Time         `json:"updated_at"`
}

type Summary struct {
	LobbyID        string        `json:"lobby_id"`
	OwnerPlayerID  string        `json:"owner_player_id"`
	DisplayName    string        `json:"display_name"`
	HostingKind    HostingKind   `json:"hosting_kind"`
	TransportKind  TransportKind `json:"transport_kind,omitempty"`
	Mode           string        `json:"mode"`
	Region         string        `json:"region"`
	ClientVersion  string        `json:"client_version"`
	State          State         `json:"state"`
	PlayerCount    int           `json:"player_count"`
	Capacity       int           `json:"capacity"`
	RosterRevision int64         `json:"roster_revision"`
	CreatedAt      time.Time     `json:"created_at"`
}

type CreateInput struct {
	DisplayName     string
	HostingKind     HostingKind
	TransportKind   TransportKind
	Mode            string
	Region          string
	ClientVersion   string
	ProtocolVersion int
	TeamOneCapacity int
	TeamTwoCapacity int
	TeamID          int
	VNTNodeID       string
	IdempotencyKey  string
}

type CreateResult struct {
	Snapshot           Snapshot
	TransportHostToken string
}

type ListFilter struct {
	HostingKind   HostingKind
	Region        string
	Mode          string
	ClientVersion string
	Cursor        string
	Limit         int
}

type ListResult struct {
	Items      []Summary `json:"items"`
	NextCursor string    `json:"next_cursor"`
}

type FrozenRosterMember struct {
	PlayerID             string `json:"player_id"`
	PlatformID           string `json:"platform_id"`
	DisplayName          string `json:"display_name"`
	RoomRole             string `json:"room_role"`
	TeamID               int    `json:"team_id"`
	TeamSlot             int    `json:"team_slot"`
	LogicalSlot          int    `json:"logical_slot"`
	ConnectionGeneration int    `json:"connection_generation"`
}

type AllocationClaims struct {
	Issuer             string               `json:"iss"`
	Audience           string               `json:"aud"`
	KeyID              string               `json:"kid"`
	TokenID            string               `json:"jti"`
	AttemptID          string               `json:"attempt_id"`
	LobbyID            string               `json:"lobby_id"`
	HostingKind        HostingKind          `json:"hosting_kind"`
	AuthorityID        string               `json:"authority_id"`
	AuthoritySessionID string               `json:"authority_session_id"`
	RosterRevision     int64                `json:"roster_revision"`
	RouteGeneration    int                  `json:"route_generation"`
	ConnectionDeadline int64                `json:"connection_deadline"`
	ConnectionWindow   int                  `json:"initial_connection_window_seconds"`
	Roster             []FrozenRosterMember `json:"roster"`
	NotBefore          int64                `json:"nbf"`
	ExpiresAt          int64                `json:"exp"`
}

type JoinGrantClaims struct {
	Issuer               string      `json:"iss"`
	Audience             string      `json:"aud"`
	KeyID                string      `json:"kid"`
	TokenID              string      `json:"jti"`
	AttemptID            string      `json:"attempt_id"`
	LobbyID              string      `json:"lobby_id"`
	HostingKind          HostingKind `json:"hosting_kind"`
	AuthorityID          string      `json:"authority_id"`
	AuthoritySessionID   string      `json:"authority_session_id"`
	PlayerID             string      `json:"player_id"`
	PlatformID           string      `json:"platform_id"`
	RosterRevision       int64       `json:"roster_revision"`
	TeamID               int         `json:"team_id"`
	TeamSlot             int         `json:"team_slot"`
	LogicalSlot          int         `json:"logical_slot"`
	ConnectionGeneration int         `json:"connection_generation"`
	RouteGeneration      int         `json:"route_generation"`
	NotBefore            int64       `json:"nbf"`
	ExpiresAt            int64       `json:"exp"`
}

type GrantResult struct {
	AttemptID            string    `json:"attempt_id"`
	GrantJTI             string    `json:"grant_jti"`
	EndpointHost         string    `json:"endpoint_host"`
	EndpointPort         int       `json:"endpoint_port"`
	Grant                string    `json:"join_grant"`
	ExpiresAt            time.Time `json:"expires_at"`
	ConnectionGeneration int       `json:"connection_generation"`
}

// AuthorityAdmission is a short-lived grant delivered only to the scoped
// match authority. It is never included in public lobby snapshots or UI DTOs.
type AuthorityAdmission struct {
	AttemptID            string    `json:"attempt_id"`
	PlayerID             string    `json:"player_id"`
	PlatformID           string    `json:"platform_id"`
	GrantJTI             string    `json:"grant_jti"`
	JoinGrant            string    `json:"join_grant"`
	ConnectionGeneration int       `json:"connection_generation"`
	RouteGeneration      int       `json:"route_generation"`
	ExpiresAt            time.Time `json:"expires_at"`
}

type AuthorityAdmissionList struct {
	Items []AuthorityAdmission `json:"items"`
}

type GrantDeliveryStatus struct {
	AttemptID   string     `json:"attempt_id"`
	GrantJTI    string     `json:"grant_jti"`
	Delivered   bool       `json:"delivered"`
	DeliveredAt *time.Time `json:"delivered_at,omitempty"`
	ExpiresAt   time.Time  `json:"expires_at"`
}

type AllocationResult struct {
	AttemptID          string    `json:"attempt_id"`
	Allocation         string    `json:"allocation"`
	AdmissionKeyID     string    `json:"admission_key_id"`
	AdmissionPublicKey string    `json:"admission_public_key_base64"`
	ExpiresAt          time.Time `json:"expires_at"`
}
