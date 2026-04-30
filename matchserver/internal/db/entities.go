package db

import (
	"time"

	"github.com/projectrebound/matchserver/internal/models"
)

type Player struct {
	PlayerID        string              `db:"player_id"`
	DisplayName     string              `db:"display_name"`
	DeviceTokenHash string              `db:"device_token_hash"`
	CreatedAt       time.Time           `db:"created_at"`
	LastSeenAt      time.Time           `db:"last_seen_at"`
	Status          models.PlayerStatus `db:"status"`
}

type HostProbe struct {
	ProbeID   string                `db:"probe_id"`
	PlayerID  string                `db:"player_id"`
	PublicIP  string                `db:"public_ip"`
	Port      int                   `db:"port"`
	Nonce     string                `db:"nonce"`
	Status    models.HostProbeStatus `db:"status"`
	CreatedAt time.Time             `db:"created_at"`
	ExpiresAt time.Time             `db:"expires_at"`
}

type Room struct {
	RoomID                 string            `db:"room_id"`
	HostPlayerID           string            `db:"host_player_id"`
	HostProbeID            string            `db:"host_probe_id"`
	HostTokenHash          string            `db:"host_token_hash"`
	Name                   string            `db:"name"`
	Region                 string            `db:"region"`
	Map                    string            `db:"map"`
	Mode                   string            `db:"mode"`
	Version                string            `db:"version"`
	Endpoint               string            `db:"endpoint"`
	Port                   int               `db:"port"`
	MaxPlayers             int               `db:"max_players"`
	PlayerCount            int               `db:"player_count"`
	ServerState            string            `db:"server_state"`
	HostLoadoutSnapshotJSON string           `db:"host_loadout_snapshot_json"`
	State                  models.RoomState  `db:"state"`
	CreatedAt              time.Time         `db:"created_at"`
	LastSeenAt             time.Time         `db:"last_seen_at"`
	EndedReason            string            `db:"ended_reason"`
}

type RoomPlayer struct {
	RoomPlayerID     string                 `db:"room_player_id"`
	RoomID           string                 `db:"room_id"`
	PlayerID         string                 `db:"player_id"`
	JoinTicketHash   string                 `db:"join_ticket_hash"`
	LoadoutSnapshotJSON string              `db:"loadout_snapshot_json"`
	Status           models.RoomPlayerStatus `db:"status"`
	JoinedAt         time.Time              `db:"joined_at"`
	ExpiresAt        time.Time              `db:"expires_at"`
}

type MatchTicket struct {
	TicketID       string                 `db:"ticket_id"`
	PlayerID       string                 `db:"player_id"`
	Region         string                 `db:"region"`
	Map            string                 `db:"map"`
	Mode           string                 `db:"mode"`
	Version        string                 `db:"version"`
	CanHost        bool                   `db:"can_host"`
	ProbeID        string                 `db:"probe_id"`
	RoomName       string                 `db:"room_name"`
	MaxPlayers     int                    `db:"max_players"`
	State          models.MatchTicketState `db:"state"`
	AssignedRoomID string                 `db:"assigned_room_id"`
	HostTokenPlain string                 `db:"host_token_plain"`
	JoinTicketPlain string               `db:"join_ticket_plain"`
	FailureReason  string                 `db:"failure_reason"`
	CreatedAt      time.Time              `db:"created_at"`
	ExpiresAt      time.Time              `db:"expires_at"`
	// MetaServer matchmaking fields
	MetaServerMode bool   `db:"metaserver_mode"`
	QoSDataJSON    string `db:"qos_data_json"`
}

type LegacyServer struct {
	ServerID    string    `db:"server_id"`
	SourceKey   string    `db:"source_key"`
	Name        string    `db:"name"`
	Region      string    `db:"region"`
	Mode        string    `db:"mode"`
	Map         string    `db:"map"`
	Endpoint    string    `db:"endpoint"`
	Port        int       `db:"port"`
	PlayerCount int       `db:"player_count"`
	ServerState string    `db:"server_state"`
	Version     string    `db:"version"`
	LastSeenAt  time.Time `db:"last_seen_at"`
}
