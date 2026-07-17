package p2proom

import (
	"time"

	"github.com/projectrebound/matchserver/internal/player"
)

type State string

const (
	StateLobby      State = "LOBBY"
	StateConnecting State = "CONNECTING"
	StateRunning    State = "RUNNING"
	StateStale      State = "STALE"
	StateClosed     State = "CLOSED"
)

type Actor struct {
	PlayerID      string
	AccountStatus player.AccountStatus
}

type Room struct {
	ID              string
	HostPlayerID    string
	HostTokenHash   []byte
	DisplayName     string
	Region          string
	Mode            string
	Version         string
	MaxPlayers      int
	PlayerCount     int
	State           State
	LastHeartbeatAt time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
	ClosedAt        *time.Time
}

type Member struct {
	RoomID   string
	PlayerID string
	Role     string
	Status   string
	JoinedAt time.Time
	LeftAt   *time.Time
}

type CreateInput struct {
	DisplayName string
	Region      string
	Mode        string
	Version     string
	MaxPlayers  int
}

type CreateResult struct {
	Room              Room
	HostToken         string
	HeartbeatInterval int
}

type ListFilter struct {
	Region   string
	Mode     string
	Version  string
	HasSlots *bool
	State    State
	Cursor   string
	Limit    int
}

type ListResult struct {
	Items      []Room
	NextCursor string
}
