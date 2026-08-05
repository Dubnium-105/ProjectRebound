package metaserver

import (
	"encoding/json"
	"time"
)

type Profile struct {
	PlayerID   string          `json:"player_id"`
	Level      int             `json:"level"`
	Experience int64           `json:"experience"`
	Currencies json.RawMessage `json:"currencies"`
	Statistics json.RawMessage `json:"statistics"`
	Revision   int64           `json:"revision"`
	CreatedAt  time.Time       `json:"created_at"`
	UpdatedAt  time.Time       `json:"updated_at"`
}

type Loadout struct {
	PlayerID  string          `json:"player_id,omitempty"`
	RoleID    string          `json:"role_id"`
	Snapshot  json.RawMessage `json:"snapshot"`
	Revision  int64           `json:"revision"`
	UpdatedAt time.Time       `json:"updated_at"`
}

type P2PRoomMemberLoadouts struct {
	SchemaVersion int                  `json:"schema_version"`
	RoomID        string               `json:"room_id"`
	PlayerID      string               `json:"player_id"`
	Loadouts      []P2PRoomRoleLoadout `json:"loadouts"`
}

type P2PRoomRoleLoadout struct {
	RoleID        string                     `json:"role_id"`
	Revision      int64                      `json:"revision"`
	Snapshot      json.RawMessage            `json:"snapshot"`
	WeaponConfigs map[string]json.RawMessage `json:"weapon_configs"`
}

type Party struct {
	ID              string        `json:"id"`
	LeaderPlayerID  string        `json:"leader_player_id"`
	State           string        `json:"state"`
	Mode            string        `json:"mode"`
	Region          string        `json:"region"`
	ClientVersion   string        `json:"client_version"`
	ProtocolVersion int           `json:"protocol_version"`
	Revision        int64         `json:"revision"`
	Members         []PartyMember `json:"members"`
	CreatedAt       time.Time     `json:"created_at"`
	UpdatedAt       time.Time     `json:"updated_at"`
}

type PartyMember struct {
	PlayerID  string    `json:"player_id"`
	Role      string    `json:"role"`
	Ready     bool      `json:"ready"`
	Presence  string    `json:"presence"`
	JoinedAt  time.Time `json:"joined_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type MatchTicket struct {
	ID              string     `json:"id"`
	PlayerID        string     `json:"player_id,omitempty"`
	PartyID         string     `json:"party_id,omitempty"`
	Mode            string     `json:"mode"`
	Region          string     `json:"region"`
	ClientVersion   string     `json:"client_version"`
	ProtocolVersion int        `json:"protocol_version"`
	State           string     `json:"state"`
	FailureCode     string     `json:"failure_code,omitempty"`
	MatchID         string     `json:"match_id,omitempty"`
	Endpoint        string     `json:"endpoint,omitempty"`
	ExpiresAt       time.Time  `json:"expires_at"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`
}

type Region struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Endpoints []Endpoint `json:"qos_endpoints"`
}

type Endpoint struct {
	Protocol string `json:"protocol"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
}

type Playlist struct {
	ID          string          `json:"id"`
	Slug        string          `json:"slug"`
	DisplayName string          `json:"display_name"`
	Description string          `json:"description"`
	Mode        string          `json:"mode"`
	Definition  json.RawMessage `json:"definition"`
	SortOrder   int             `json:"sort_order"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

type Notification struct {
	ID        string     `json:"id"`
	Title     string     `json:"title"`
	Body      string     `json:"body"`
	Locale    string     `json:"locale"`
	Priority  int        `json:"priority"`
	StartsAt  *time.Time `json:"starts_at,omitempty"`
	EndsAt    *time.Time `json:"ends_at,omitempty"`
	UpdatedAt time.Time  `json:"updated_at"`
}

type MatchPlayerLoadout struct {
	MatchID  string    `json:"match_id"`
	PlayerID string    `json:"player_id"`
	Loadouts []Loadout `json:"loadouts"`
}
