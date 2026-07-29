package auth

import (
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/player"
)

type Session struct {
	ID                  string
	PlayerID            string
	RefreshTokenHash    []byte
	TokenFamilyID       string
	TokenVersion        int
	DeviceIDHash        []byte
	DeviceIDSuffix      string
	DeviceFingerprintID string
	IPAddress           string
	UserAgent           string
	ExpiresAt           time.Time
	RevokedAt           *time.Time
	RevokedReason       string
	ReplacedBySessionID string
	ReuseDetectedAt     *time.Time
	CreatedAt           time.Time
	LastUsedAt          *time.Time
}

type SessionTokens struct {
	AccessToken           string
	AccessTokenExpiresAt  time.Time
	RefreshToken          string
	RefreshTokenExpiresAt time.Time
	SessionID             string
}

type BindInput struct {
	SteamID     string
	PersonaName string
	DeviceID    string
	InviteCode  string
}

type RequestMeta struct {
	RequestID           string
	IPAddress           string
	UserAgent           string
	DeviceID            string
	DeviceFingerprintID string
}

type BindResult struct {
	Player      player.Player
	Tokens      SessionTokens
	IsNewPlayer bool
}

type RefreshResult struct {
	Tokens SessionTokens
}

type Principal struct {
	Player    player.Player
	SessionID string
}

type AuditEvent struct {
	ID          string
	PlayerID    string
	SteamID     string
	Event       string
	Success     bool
	FailureCode string
	RequestID   string
	IPAddress   string
	UserAgent   string
	CreatedAt   time.Time
}

type RiskEvent struct {
	ID                  string
	PlayerID            string
	SteamID             string
	DeviceIDHash        []byte
	DeviceFingerprintID string
	IPAddress           string
	EventType           string
	Severity            string
	Details             map[string]any
	CreatedAt           time.Time
	ResolvedAt          *time.Time
}

type LoginEvent struct {
	ID                  string
	PlayerID            string
	SteamID             string
	SessionID           string
	DeviceIDHash        []byte
	DeviceFingerprintID string
	IPAddress           string
	UserAgent           string
	Result              string
	FailureCode         string
	CreatedAt           time.Time
}

type DeviceFingerprint struct {
	ID               string
	FormatVersion    int16
	DigestKeyID      string
	CompositeDigest  []byte
	SMBIOSUUIDDigest []byte
	DiskSerialDigest []byte
	CPUIDDigest      []byte
	FactorMask       int16
	FirstSeenAt      time.Time
	LastSeenAt       time.Time
}

type UserSession struct {
	ID             string
	DeviceIDSuffix string
	IPAddress      string
	CreatedAt      time.Time
	LastUsedAt     *time.Time
	IsCurrent      bool
}

type RiskEventList struct {
	Items      []RiskEvent
	NextCursor string
}
