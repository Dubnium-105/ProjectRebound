package admin

import (
	"time"

	"github.com/projectrebound/matchserver/internal/player"
)

type RequestMeta struct {
	AdminID   string
	RequestID string
	IPAddress string
	UserAgent string
}

type PlayerPatch struct {
	AccountStatus  *player.AccountStatus
	IsVIP          *bool
	RevokeSessions bool
	Reason         string
	InternalNote   string
}

type PatchResult struct {
	Player          player.Player
	RevokedSessions int64
}

type ListResult struct {
	Items      []player.Player
	NextCursor string
}

type AuditLog struct {
	ID         string
	AdminID    string
	Action     string
	TargetType string
	TargetID   string
	OldValue   map[string]any
	NewValue   map[string]any
	Reason     string
	RequestID  string
	IPAddress  string
	UserAgent  string
	Result     string
	CreatedAt  time.Time
}
