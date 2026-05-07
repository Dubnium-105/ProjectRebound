package models

import "time"

type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type GuestAuthRequest struct {
	DisplayName string `json:"displayName"`
	DeviceToken string `json:"deviceToken"`
}

type GuestAuthResponse struct {
	PlayerID    string `json:"playerId"`
	DisplayName string `json:"displayName"`
	AccessToken string `json:"accessToken"`
}

type CreateHostProbeRequest struct {
	Port int `json:"port"`
}

type CreateHostProbeResponse struct {
	ProbeID   string    `json:"probeId"`
	PublicIP  string    `json:"publicIp"`
	Port      int       `json:"port"`
	Nonce     string    `json:"nonce"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type ConfirmHostProbeRequest struct {
	Nonce string `json:"nonce"`
}

type HostProbeResponse struct {
	ProbeID   string          `json:"probeId"`
	Status    HostProbeStatus `json:"status"`
	PublicIP  string          `json:"publicIp"`
	Port      int             `json:"port"`
	ExpiresAt time.Time       `json:"expiresAt"`
}

type CreateRoomRequest struct {
	ProbeID      string `json:"probeId"`
	Name         string `json:"name"`
	Region       string `json:"region"`
	Map          string `json:"map"`
	Mode         string `json:"mode"`
	Version      string `json:"version"`
	MaxPlayers   int    `json:"maxPlayers"`
	BindingToken string `json:"bindingToken,omitempty"`
}

type CreateRoomResponse struct {
	RoomID           string `json:"roomId"`
	HostToken        string `json:"hostToken"`
	HeartbeatSeconds int    `json:"heartbeatSeconds"`
}

type RoomSummary struct {
	RoomID       string    `json:"roomId"`
	HostPlayerID string    `json:"hostPlayerId"`
	Name         string    `json:"name"`
	Region       string    `json:"region"`
	Map          string    `json:"map"`
	Mode         string    `json:"mode"`
	Version      string    `json:"version"`
	Endpoint     string    `json:"endpoint"`
	Port         int       `json:"port"`
	PlayerCount  int       `json:"playerCount"`
	MaxPlayers   int       `json:"maxPlayers"`
	ServerState  string    `json:"serverState"`
	State        RoomState `json:"state"`
	LastSeenAt   time.Time `json:"lastSeenAt"`
	EndedReason  string    `json:"endedReason"`
}

type PagedRoomsResponse struct {
	Items    []RoomSummary `json:"items"`
	Page     int           `json:"page"`
	PageSize int           `json:"pageSize"`
	Total    int           `json:"total"`
}

type JoinRoomRequest struct {
	Version string `json:"version"`
}

type JoinRoomResponse struct {
	Connect   string    `json:"connect"`
	JoinTicket string   `json:"joinTicket"`
	ExpiresAt  time.Time `json:"expiresAt"`
}

type LeaveRoomRequest struct {
	JoinTicket string `json:"joinTicket"`
}

type RoomHeartbeatRequest struct {
	HostToken   string `json:"hostToken"`
	PlayerCount int    `json:"playerCount"`
	ServerState string `json:"serverState"`
}

type RoomLifecycleRequest struct {
	HostToken string `json:"hostToken"`
}

type RoomHeartbeatResponse struct {
	Ok                  bool `json:"ok"`
	NextHeartbeatSeconds int  `json:"nextHeartbeatSeconds"`
}

type CreateMatchTicketRequest struct {
	Region     string `json:"region"`
	Map        string `json:"map"`
	Mode       string `json:"mode"`
	Version    string `json:"version"`
	CanHost    bool   `json:"canHost"`
	ProbeID    string `json:"probeId"`
	RoomName   string `json:"roomName"`
	MaxPlayers int    `json:"maxPlayers"`
}

type CreateMatchTicketResponse struct {
	TicketID  string           `json:"ticketId"`
	State     MatchTicketState `json:"state"`
	ExpiresAt time.Time        `json:"expiresAt"`
}

type MatchTicketResponse struct {
	TicketID       string           `json:"ticketId"`
	State          MatchTicketState `json:"state"`
	AssignedRoomID string           `json:"assignedRoomId"`
	Room           *RoomSummary     `json:"room"`
	HostToken      string           `json:"hostToken"`
	Connect        string           `json:"connect"`
	JoinTicket     string           `json:"joinTicket"`
	FailureReason  string           `json:"failureReason"`
	ExpiresAt      time.Time        `json:"expiresAt"`
}

type LegacyServerStatusRequest struct {
	Name        string `json:"name"`
	Region      string `json:"region"`
	Mode        string `json:"mode"`
	Map         string `json:"map"`
	Port        int    `json:"port"`
	PlayerCount int    `json:"playerCount"`
	ServerState string `json:"serverState"`
	RoomID      string `json:"roomId"`
	HostToken   string `json:"hostToken"`
	Version     string `json:"version"`
}

type LegacyServerStatusResponse struct {
	Ok                  bool   `json:"ok"`
	ServerID            string `json:"serverId"`
	NextHeartbeatSeconds int   `json:"nextHeartbeatSeconds"`
}

type LegacyServerSummary struct {
	ServerID    string    `json:"serverId"`
	Name        string    `json:"name"`
	Region      string    `json:"region"`
	Mode        string    `json:"mode"`
	Map         string    `json:"map"`
	Endpoint    string    `json:"endpoint"`
	Port        int       `json:"port"`
	PlayerCount int       `json:"playerCount"`
	ServerState string    `json:"serverState"`
	Version     string    `json:"version"`
	LastSeenAt  time.Time `json:"lastSeenAt"`
}

type CreateNatBindingRequest struct {
	LocalPort int    `json:"localPort"`
	Role      string `json:"role"`
	RoomID    string `json:"roomId"`
}

type CreateNatBindingResponse struct {
	BindingToken string    `json:"bindingToken"`
	UDPHost      string    `json:"udpHost"`
	UDPPort      int       `json:"udpPort"`
	ExpiresAt    time.Time `json:"expiresAt"`
}

type ConfirmNatBindingResponse struct {
	BindingToken string    `json:"bindingToken"`
	PublicIP     string    `json:"publicIp"`
	PublicPort   int       `json:"publicPort"`
	LocalPort    int       `json:"localPort"`
	Role         string    `json:"role"`
	RoomID       string    `json:"roomId"`
	ExpiresAt    time.Time `json:"expiresAt"`
}

type CreatePunchTicketRequest struct {
	JoinTicket        string `json:"joinTicket"`
	BindingToken      string `json:"bindingToken"`
	ClientLocalEndpoint string `json:"clientLocalEndpoint"`
}

type CreatePunchTicketResponse struct {
	TicketID           string           `json:"ticketId"`
	State              PunchTicketState `json:"state"`
	Nonce              string           `json:"nonce"`
	HostEndpoint       string           `json:"hostEndpoint"`
	HostLocalEndpoint  string           `json:"hostLocalEndpoint"`
	ClientEndpoint     string           `json:"clientEndpoint"`
	ClientLocalEndpoint string          `json:"clientLocalEndpoint"`
	ExpiresAt          time.Time        `json:"expiresAt"`
}

type PunchTicketResponse struct {
	TicketID           string           `json:"ticketId"`
	State              PunchTicketState `json:"state"`
	Nonce              string           `json:"nonce"`
	HostEndpoint       string           `json:"hostEndpoint"`
	HostLocalEndpoint  string           `json:"hostLocalEndpoint"`
	ClientEndpoint     string           `json:"clientEndpoint"`
	ClientLocalEndpoint string          `json:"clientLocalEndpoint"`
	ExpiresAt          time.Time        `json:"expiresAt"`
}

type ListPunchTicketsResponse struct {
	Items []PunchTicketResponse `json:"items"`
}

type CreateRelayAllocationRequest struct {
	RoomID     string `json:"roomId"`
	Role       string `json:"role"`
	HostToken  string `json:"hostToken"`
	JoinTicket string `json:"joinTicket"`
}

type CreateRelayAllocationResponse struct {
	SessionID  string    `json:"sessionId"`
	Role       string    `json:"role"`
	Secret     string    `json:"secret"`
	RelayHost  string    `json:"relayHost"`
	RelayPort  int       `json:"relayPort"`
	ExpiresAt  time.Time `json:"expiresAt"`
}

// MetaServer matchmaking contracts
type MatchmakingEnqueueRequest struct {
	UserID   string      `json:"userId"`
	RegionID string      `json:"regionId"`
	GameMode string      `json:"gameMode"`
	QoSData  interface{} `json:"qosData,omitempty"`
}

type MatchmakingEnqueueResponse struct {
	TicketID string `json:"ticketId"`
	Status   string `json:"status"`
}

type MatchmakingStatusResponse struct {
	TicketID   string `json:"ticketId"`
	Status     string `json:"status"`
	ServerIP   string `json:"serverIp,omitempty"`
	ServerPort int    `json:"serverPort,omitempty"`
}

type MatchmakingCancelResponse struct {
	TicketID string `json:"ticketId"`
	Status   string `json:"status"`
}
