package relayregistry

import "time"

type State string

type LoadState string

const (
	StateBootstrapping State = "BOOTSTRAPPING"
	StateConnecting    State = "CONNECTING"
	StateReady         State = "READY"
	StateDraining      State = "DRAINING"
	StateUnhealthy     State = "UNHEALTHY"
	StateOffline       State = "OFFLINE"
	StateRevoked       State = "REVOKED"
)

const (
	LoadStateNormal    LoadState = "NORMAL"
	LoadStateDegraded  LoadState = "DEGRADED"
	LoadStateRejectNew LoadState = "REJECT_NEW"
	LoadStateDraining  LoadState = "DRAINING"
)

type Endpoint struct {
	Protocol string `json:"protocol"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
}

type Node struct {
	ID                     string
	DisplayName            string
	Region                 string
	Zone                   string
	Provider               string
	State                  State
	LoadState              LoadState
	SoftwareVersion        string
	ProtocolVersion        int
	PublicEndpoints        []Endpoint
	SupportedProtocols     []string
	MaxAllocations         int
	MaxEgressBPS           int64
	ActiveAllocations      int
	CurrentEgressBPS       int64
	CurrentIngressBPS      int64
	CertificateFingerprint string
	CertificateSerial      string
	CertificateIssuedAt    time.Time
	CertificateNotBefore   time.Time
	CertificateExpiresAt   time.Time
	NodeTokenHash          []byte
	ConfigVersion          int64
	LastHeartbeatAt        *time.Time
	LeaseExpiresAt         *time.Time
	DrainDeadline          *time.Time
	DrainMigrateExisting   bool
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

type DrainInput struct {
	DeadlineSeconds int
	MigrateExisting bool
}

type EnrollInput struct {
	DisplayName        string
	Region             string
	Zone               string
	Provider           string
	SoftwareVersion    string
	ProtocolVersion    int
	PublicEndpoints    []Endpoint
	SupportedProtocols []string
	MaxAllocations     int
	MaxEgressBPS       int64
	CSRPEM             string
}

type EnrollResult struct {
	Node                 Node
	NodeToken            string
	CertificatePEM       string
	CACertificatePEM     string
	CertificateExpiresAt time.Time
	Keyset               Keyset
}

type HeartbeatInput struct {
	ActiveAllocations int
	CurrentEgressBPS  int64
	CurrentIngressBPS int64
	LoadState         LoadState
}

type ListFilter struct {
	Region   string
	Zone     string
	Provider string
	State    State
	Cursor   string
	Limit    int
}

type ListResult struct {
	Items      []Node
	NextCursor string
}

type Allocation struct {
	ID            string
	ConnectionID  string
	RoomID        string
	RelayNodeID   string
	State         string
	Protocol      string
	MaxBPS        int64
	MaxPPS        int
	MaxTotalBytes int64
	ExpiresAt     time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
	ClosedAt      *time.Time
}

type Migration struct {
	ID              string
	ConnectionID    string
	RoomID          string
	OldAllocationID string
	NewAllocationID string
	OldRelayNodeID  string
	NewRelayNodeID  string
	NewAllocation   Allocation
	NewNode         Node
	State           string
	Reason          string
	Attempt         int
	BindDeadline    *time.Time
	FailureReason   string
	DispatchedAt    *time.Time
	CompletedAt     *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type AdminMeta struct {
	ActorID   string
	RequestID string
	IPAddress string
	UserAgent string
	Reason    string
}

type Keyset struct {
	Version     int64       `json:"version"`
	GeneratedAt time.Time   `json:"generated_at"`
	SignedBy    string      `json:"signed_by"`
	Keys        []PublicKey `json:"keys"`
	Signature   string      `json:"signature"`
}

type PublicKey struct {
	KeyID     string `json:"kid"`
	Algorithm string `json:"alg"`
	PublicKey string `json:"public_key"`
}
