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
	ID                             string
	InstanceID                     string
	OwnerPlayerID                  string
	DisplayName                    string
	Region                         string
	Mode                           string
	Version                        string
	PublicHost                     string
	PublicPort                     int
	MaxPlayers                     int
	PlayerCount                    int
	State                          State
	ServerTokenHash                []byte
	PreviousServerTokenHash        []byte
	RegistrationIssuer             string
	TokenExpiresAt                 time.Time
	PreviousTokenExpiresAt         *time.Time
	TokenRevokedAt                 *time.Time
	CredentialGeneration           int64
	CertificateFingerprint         string
	CertificatePublicKey           []byte
	CertificateSerial              string
	CertificateExpiresAt           *time.Time
	PreviousCertificateFingerprint string
	PreviousCertificatePublicKey   []byte
	PreviousCertificateExpiresAt   *time.Time
	LegacyAuthExpiresAt            *time.Time
	BannedAt                       *time.Time
	BannedBy                       string
	BanReason                      string
	DeletedAt                      *time.Time
	DeletedBy                      string
	DeleteReason                   string
	LastHeartbeatAt                time.Time
	CreatedAt                      time.Time
	UpdatedAt                      time.Time
	ActiveMatch                    *MatchAssignment
}

// MatchAssignment is transient control-plane state returned only to the
// authenticated server that owns the active strict-roster attempt. It is not
// persisted in game_servers or exposed by the public server directory.
type MatchAssignment struct {
	AttemptID       string `json:"attempt_id"`
	State           string `json:"state"`
	RouteGeneration int    `json:"route_generation"`
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
	CSRPEM      string
}

type RegistrationResult struct {
	Server            Server
	ServerToken       string
	HeartbeatInterval int
	CertificatePEM    string
	CACertificatePEM  string
}

type RegistrationCredentialInput struct {
	InstanceID string
	PlayerID   string
	SteamID    string
	InviteCode string
	IPAddress  string
}

type RegistrationCredentialResult struct {
	Credential gameserverregistration.Credential
	Plaintext  string
}

type CredentialRotationResult struct {
	ServerID               string
	ServerToken            string
	TokenExpiresAt         time.Time
	PreviousValidUntil     time.Time
	CredentialGeneration   int64
	CertificatePEM         string
	CACertificatePEM       string
	CertificateFingerprint string
	CertificateExpiresAt   time.Time
}

type SignedRequestInput struct {
	ServerID               string
	ServerToken            string
	CertificateFingerprint string
	Timestamp              int64
	Nonce                  string
	CredentialGeneration   int64
	Signature              string
	Method                 string
	RequestTarget          string
	Body                   []byte
}

type SignedRequestPrincipal struct {
	ServerID             string
	CredentialGeneration int64
	Legacy               bool
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
