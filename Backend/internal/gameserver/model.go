package gameserver

import "time"

type State string

const (
	StateStarting  State = "STARTING"
	StateReady     State = "READY"
	StateReserved  State = "RESERVED"
	StateRunning   State = "RUNNING"
	StateDraining  State = "DRAINING"
	StateUnhealthy State = "UNHEALTHY"
	StateOffline   State = "OFFLINE"
)

type Server struct {
	ID                 string
	InstanceID         string
	DisplayName        string
	Region             string
	Mode               string
	Version            string
	PublicHost         string
	PublicPort         int
	MaxPlayers         int
	PlayerCount        int
	State              State
	ServerTokenHash    []byte
	RegistrationIssuer string
	TokenExpiresAt     time.Time
	TokenRevokedAt     *time.Time
	LastHeartbeatAt    time.Time
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

type RegistrationInput struct {
	InstanceID  string
	DisplayName string
	Region      string
	Mode        string
	Version     string
	PublicHost  string
	PublicPort  int
	MaxPlayers  int
}

type RegistrationResult struct {
	Server            Server
	ServerToken       string
	HeartbeatInterval int
}

type HeartbeatInput struct {
	State       State
	PlayerCount int
}

type ListFilter struct {
	Region  string
	Mode    string
	Version string
	State   State
	Cursor  string
	Limit   int
}

type ListResult struct {
	Items      []Server
	NextCursor string
}
