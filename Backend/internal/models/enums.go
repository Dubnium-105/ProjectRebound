package models

type PlayerStatus string

const (
	PlayerStatusActive   PlayerStatus = "Active"
	PlayerStatusDisabled PlayerStatus = "Disabled"
)

type HostProbeStatus string

const (
	HostProbePending   HostProbeStatus = "Pending"
	HostProbeSucceeded HostProbeStatus = "Succeeded"
	HostProbeExpired   HostProbeStatus = "Expired"
	HostProbeFailed    HostProbeStatus = "Failed"
)

type RoomState string

const (
	RoomStateProbing  RoomState = "Probing"
	RoomStateOpen     RoomState = "Open"
	RoomStateStarting RoomState = "Starting"
	RoomStateInGame   RoomState = "InGame"
	RoomStateEnded    RoomState = "Ended"
	RoomStateExpired  RoomState = "Expired"
)

type RoomPlayerStatus string

const (
	RoomPlayerReserved RoomPlayerStatus = "Reserved"
	RoomPlayerJoined   RoomPlayerStatus = "Joined"
	RoomPlayerLeft     RoomPlayerStatus = "Left"
	RoomPlayerExpired  RoomPlayerStatus = "Expired"
)

type MatchTicketState string

const (
	MatchTicketWaiting      MatchTicketState = "Waiting"
	MatchTicketHostAssigned MatchTicketState = "HostAssigned"
	MatchTicketMatched      MatchTicketState = "Matched"
	MatchTicketFailed       MatchTicketState = "Failed"
	MatchTicketCanceled     MatchTicketState = "Canceled"
	MatchTicketExpired      MatchTicketState = "Expired"
)

type PunchTicketState string

const (
	PunchTicketPending   PunchTicketState = "Pending"
	PunchTicketActive    PunchTicketState = "Active"
	PunchTicketCompleted PunchTicketState = "Completed"
	PunchTicketExpired   PunchTicketState = "Expired"
	PunchTicketFailed    PunchTicketState = "Failed"
)
