package connection

import (
	"encoding/json"
	"time"

	"github.com/projectrebound/matchserver/internal/player"
)

type State string

const (
	StateCreated             State = "CREATED"
	StateGatheringCandidates State = "GATHERING_CANDIDATES"
	StateCheckingDirect      State = "CHECKING_DIRECT"
	StateAllocatingRelay     State = "ALLOCATING_RELAY"
	StateRelayBinding        State = "RELAY_BINDING"
	StateConnected           State = "CONNECTED"
	StateFailed              State = "FAILED"
	StateExpired             State = "EXPIRED"
	StateClosed              State = "CLOSED"
)

type Path string

const (
	PathLAN         Path = "LAN"
	PathIPv6        Path = "IPV6"
	PathUDPPunch    Path = "UDP_PUNCH"
	PathUDPRelay    Path = "UDP_RELAY"
	PathTCPTLSRelay Path = "TCP_TLS_RELAY"
)

type CandidateType string

const (
	CandidateLAN   CandidateType = "LAN"
	CandidateIPv6  CandidateType = "IPV6"
	CandidateSRFLX CandidateType = "SRFLX"
)

type Actor struct {
	PlayerID      string
	AccountStatus player.AccountStatus
}

type Connection struct {
	ID            string
	RoomID        string
	HostPlayerID  string
	PeerPlayerID  string
	State         State
	SelectedPath  Path
	FailureReason string
	ExpiresAt     time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
	ClosedAt      *time.Time
	Candidates    []Candidate
}

type Candidate struct {
	ID            string        `json:"candidate_id"`
	ConnectionID  string        `json:"connection_id"`
	PlayerID      string        `json:"player_id"`
	Foundation    string        `json:"foundation"`
	CandidateType CandidateType `json:"candidate_type"`
	Protocol      string        `json:"protocol"`
	Address       string        `json:"address"`
	Port          int           `json:"port"`
	Priority      int           `json:"priority"`
	CreatedAt     time.Time     `json:"created_at"`
}

type CreateInput struct {
	RoomID       string
	PeerPlayerID string
}

type CandidateInput struct {
	ConnectionID  string        `json:"connection_id"`
	Foundation    string        `json:"foundation"`
	CandidateType CandidateType `json:"candidate_type"`
	Protocol      string        `json:"protocol"`
	Address       string        `json:"address"`
	Port          int           `json:"port"`
	Priority      int           `json:"priority"`
}

type CheckResultInput struct {
	ConnectionID string `json:"connection_id"`
	Success      bool   `json:"success"`
	Path         Path   `json:"path"`
	LatencyMS    int    `json:"latency_ms"`
	Reason       string `json:"reason"`
}

type Event struct {
	Type      string    `json:"type"`
	Payload   any       `json:"payload"`
	CreatedAt time.Time `json:"created_at"`
}

type IncomingEvent struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}
