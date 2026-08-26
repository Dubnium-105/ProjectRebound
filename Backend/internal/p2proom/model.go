package p2proom

import (
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/player"
)

type State string

type TransportKind string

const (
	StateLobby      State = "LOBBY"
	StateConnecting State = "CONNECTING"
	StateRunning    State = "RUNNING"
	StateStale      State = "STALE"
	StateClosed     State = "CLOSED"
)

const (
	TransportLegacy TransportKind = "LEGACY_RELAY"
	TransportVNT    TransportKind = "VNT"
)

type Actor struct {
	PlayerID      string
	AccountStatus player.AccountStatus
}

type Room struct {
	ID                     string
	HostPlayerID           string
	HostTokenHash          []byte
	DisplayName            string
	Region                 string
	Mode                   string
	Version                string
	MaxPlayers             int
	PlayerCount            int
	State                  State
	LastHeartbeatAt        time.Time
	CreatedAt              time.Time
	UpdatedAt              time.Time
	ClosedAt               *time.Time
	TransportKind          TransportKind
	ExpiresAt              time.Time
	VNTNodeID              string
	VNTHost                string
	VNTPort                int
	VNTRegion              string
	VNTLocation            string
	VNTState               string
	VNTGeneration          int
	IdempotencyKey         string
	IdempotencyRequestHash []byte
	HostTokenCiphertext    []byte
	HostTokenNonce         []byte
	HostTokenKeyID         string
	ManagedLobbyID         string
}

type Member struct {
	RoomID   string
	PlayerID string
	Role     string
	Status   string
	JoinedAt time.Time
	LeftAt   *time.Time
}

type CreateInput struct {
	DisplayName    string
	Region         string
	Mode           string
	Version        string
	MaxPlayers     int
	TransportKind  TransportKind
	VNTNodeID      string
	IdempotencyKey string
	ManagedLobbyID string
}

type VNTSession struct {
	RoomID                 string
	NodeID                 string
	Generation             int
	State                  string
	NodeHost               string
	NodePort               int
	NodeRegion             string
	NodeLocation           string
	NodeFingerprint        string
	NodeTransports         []string
	NetworkTokenCiphertext []byte
	NetworkTokenNonce      []byte
	E2EPasswordCiphertext  []byte
	E2EPasswordNonce       []byte
	SecretKeyID            string
	HostVirtualIP          string
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

type VNTMemberSession struct {
	RoomID        string
	Generation    int
	PlayerID      string
	DeviceID      string
	VirtualIP     string
	State         string
	ObservedPath  string
	FailureReason string
	CreatedAt     time.Time
}

type VNTBootstrap struct {
	RoomID        string            `json:"room_id"`
	Generation    int               `json:"generation"`
	ExpiresAt     time.Time         `json:"expires_at"`
	Server        VNTServerEndpoint `json:"server"`
	NetworkToken  string            `json:"network_token"`
	E2EPassword   string            `json:"e2e_password"`
	CipherModel   string            `json:"cipher_model"`
	ServerEncrypt bool              `json:"server_encrypt"`
	DeviceID      string            `json:"device_id"`
	DeviceName    string            `json:"device_name"`
	VirtualIP     string            `json:"virtual_ip"`
	HostVirtualIP *string           `json:"host_virtual_ip"`
	MTU           int               `json:"mtu"`
}

type VNTServerEndpoint struct {
	Address              string   `json:"address"`
	ServerKeyFingerprint string   `json:"server_key_fingerprint"`
	SupportedTransports  []string `json:"supported_transports"`
}

type VNTPresenceInput struct {
	Generation   int
	State        string
	VirtualIP    string
	ObservedPath string
	ReasonCode   string
}

type CreateResult struct {
	Room              Room
	HostToken         string
	HeartbeatInterval int
}

type ListFilter struct {
	Region   string
	Mode     string
	Version  string
	HasSlots *bool
	State    State
	Cursor   string
	Limit    int
}

type ListResult struct {
	Items      []Room
	NextCursor string
}
