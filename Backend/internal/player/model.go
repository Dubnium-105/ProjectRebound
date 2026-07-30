package player

import "time"

type AccountStatus string

const (
	AccountStatusActive  AccountStatus = "ACTIVE"
	AccountStatusBanned  AccountStatus = "BANNED"
	AccountStatusDeleted AccountStatus = "DELETED"
)

const (
	AuthProviderSteamClientAsserted = "steam_client_asserted"
	AuthProviderSteamTicket         = "steam_ticket"
	AuthLevelUnverified             = "unverified"
	AuthLevelVerified               = "verified"
	AuthLevelTrusted                = "trusted"
)

type Player struct {
	ID            string
	SteamID       string
	PersonaName   string
	AccountStatus AccountStatus
	IsVIP         bool
	AuthProvider  string
	AuthLevel     string
	LastLoginAt   time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type AdministrativePatch struct {
	AccountStatus *AccountStatus
	IsVIP         *bool
}
