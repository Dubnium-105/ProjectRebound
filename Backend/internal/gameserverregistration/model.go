package gameserverregistration

import "time"

type Credential struct {
	ID                string
	InstanceID        string
	CreatedBy         string
	IssuedToPlayerID  string
	SourceInviteUseID string
	ExpiresAt         time.Time
	ConsumedAt        *time.Time
	ConsumedServerID  string
	RevokedAt         *time.Time
	CreatedAt         time.Time
}
