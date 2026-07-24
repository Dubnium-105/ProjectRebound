package invite

import "time"

type Code struct {
	ID          string
	BatchName   string
	MaxUses     int
	UsedCount   int
	ExpiresAt   *time.Time
	Enabled     bool
	Permissions map[string]any
	CreatedBy   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	RevokedAt   *time.Time
}

type CreateInput struct {
	BatchName   string
	Quantity    int
	MaxUses     int
	ExpiresAt   *time.Time
	Permissions map[string]any
	Reason      string
}

type CreateResult struct {
	Code      Code
	Plaintext string
	Items     []CreatedCode
}

type CreatedCode struct {
	Code      Code
	Plaintext string
}

type Patch struct {
	BatchName   *string
	MaxUses     *int
	ExpiresAt   *time.Time
	ClearExpiry bool
	Enabled     *bool
	Permissions map[string]any
	Reason      string
}

type ListResult struct {
	Items      []Code
	NextCursor string
}

type Use struct {
	ID           string
	InviteCodeID string
	PlayerID     string
	SteamID      string
	IPAddress    string
	UsedAt       time.Time
	Result       string
}

type UseListResult struct {
	Items      []Use
	NextCursor string
}

type RequestMeta struct {
	AdminID   string
	RequestID string
	IPAddress string
	UserAgent string
}
