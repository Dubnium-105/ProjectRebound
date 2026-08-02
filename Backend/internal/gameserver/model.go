package gameserver

import (
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/gameserverregistration"
)

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
	ID                      string
	InstanceID              string
	OwnerPlayerID           string
	DisplayName             string
	Region                  string
	Mode                    string
	Version                 string
	PublicHost              string
	PublicPort              int
	MaxPlayers              int
	PlayerCount             int
	State                   State
	ServerTokenHash         []byte
	PreviousServerTokenHash []byte
	RegistrationIssuer      string
	TokenExpiresAt          time.Time
	PreviousTokenExpiresAt  *time.Time
	TokenRevokedAt          *time.Time
	CredentialGeneration    int64
	LastHeartbeatAt         time.Time
	CreatedAt               time.Time
	UpdatedAt               time.Time
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

type RegistrationCredentialInput struct {
	InstanceID string
	PlayerID   string
}

type RegistrationCredentialResult struct {
	Credential gameserverregistration.Credential
	Plaintext  string
}

type CredentialRotationResult struct {
	ServerID             string
	ServerToken          string
	TokenExpiresAt       time.Time
	PreviousValidUntil   time.Time
	CredentialGeneration int64
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
