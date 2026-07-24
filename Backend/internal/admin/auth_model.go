package admin

import "time"

const (
	AdminStatusActive   = "ACTIVE"
	AdminStatusDisabled = "DISABLED"
)

type AdminUser struct {
	ID           string
	Username     string
	DisplayName  string
	PasswordHash string
	Status       string
	MFARequired  bool
	LastLoginAt  *time.Time
	CreatedAt    time.Time
	UpdatedAt    time.Time
	DisabledAt   *time.Time
}

type LoginInput struct {
	Username       string
	Password       string
	TurnstileToken string
}

type LoginResult struct {
	MFARequired    bool
	ChallengeToken string
	ExpiresAt      time.Time
}

type MFAVerifyInput struct {
	ChallengeToken string
	Code           string
}

type AdminTokens struct {
	AccessToken          string
	AccessTokenExpiresAt time.Time
	RefreshToken         string
	RefreshExpiresAt     time.Time
}

type MFAVerifyResult struct {
	Tokens AdminTokens
	Admin  CurrentAdmin
}

type StepUpResult struct {
	Token     string
	ExpiresAt time.Time
}

type RefreshAdminResult struct {
	Tokens AdminTokens
	Admin  CurrentAdmin
}

type AdminSession struct {
	ID                       string
	AdminID                  string
	RefreshTokenHash         []byte
	PreviousRefreshTokenHash []byte
	TokenVersion             int
	IPAddress                string
	UserAgent                string
	CreatedAt                time.Time
	LastUsedAt               *time.Time
	ExpiresAt                time.Time
	RevokedAt                *time.Time
	RevokeReason             string
}

type LoginChallenge struct {
	ID               string
	Admin            AdminUser
	TokenHash        []byte
	SecretCiphertext []byte
	Attempts         int
	RequestID        string
	IPAddress        string
	UserAgent        string
	ExpiresAt        time.Time
	CreatedAt        time.Time
}

type LoginAudit struct {
	ID                       string
	AdminID                  string
	UsernameHash             string
	EventType                string
	Result                   string
	ReasonCode               string
	RequestID                string
	IPAddress                string
	UserAgent                string
	TurnstileSuccess         *bool
	TurnstileErrorCodes      []string
	TurnstileHostname        string
	TurnstileAction          string
	TurnstileVerifyLatencyMS *int
	CreatedAt                time.Time
}

type CurrentAdmin struct {
	User        AdminUser
	SessionID   string
	Roles       []string
	Permissions []string
}

type SessionListItem struct {
	ID         string
	IPAddress  string
	UserAgent  string
	CreatedAt  time.Time
	LastUsedAt *time.Time
	ExpiresAt  time.Time
	IsCurrent  bool
}
