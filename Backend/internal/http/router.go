package http

import (
	"net/http"
)

func RegisterRoutes(mux *http.ServeMux, deps *Deps) {
	mux.HandleFunc("GET /health", handleHealth())

	// Auth
	mux.HandleFunc("POST /v1/auth/guest", handleGuestAuth(deps.DB))

	// Host probes
	mux.HandleFunc("POST /v1/host-probes", RequirePlayer(deps.DB, handleCreateHostProbe(deps)))
	mux.HandleFunc("POST /v1/host-probes/{probeId}/confirm", RequirePlayer(deps.DB, handleConfirmHostProbe(deps.DB)))

	// NAT bindings
	mux.HandleFunc("POST /v1/nat/bindings", RequirePlayer(deps.DB, handleCreateNatBinding(deps)))
	mux.HandleFunc("POST /v1/nat/bindings/{bindingToken}/confirm", RequirePlayer(deps.DB, handleConfirmNatBinding(deps)))

	// Rooms
	mux.HandleFunc("POST /v1/rooms", RequirePlayer(deps.DB, handleCreateRoom(deps)))
	mux.HandleFunc("GET /v1/rooms", handleListRooms(deps.DB))
	mux.HandleFunc("GET /v1/rooms/{roomId}", handleGetRoom(deps.DB))
	mux.HandleFunc("POST /v1/rooms/{roomId}/join", RequirePlayer(deps.DB, handleJoinRoom(deps)))
	mux.HandleFunc("POST /v1/rooms/{roomId}/leave", RequirePlayer(deps.DB, handleLeaveRoom(deps.DB)))
	mux.HandleFunc("POST /v1/rooms/{roomId}/heartbeat", handleHeartbeatRoom(deps))
	mux.HandleFunc("POST /v1/rooms/{roomId}/start", RequirePlayer(deps.DB, handleStartRoom(deps.DB)))
	mux.HandleFunc("POST /v1/rooms/{roomId}/end", RequirePlayer(deps.DB, handleEndRoom(deps.DB)))

	// Punch tickets
	mux.HandleFunc("POST /v1/rooms/{roomId}/punch-tickets", RequirePlayer(deps.DB, handleCreatePunchTicket(deps)))
	mux.HandleFunc("GET /v1/rooms/{roomId}/punch-tickets", handleGetPunchTickets(deps))
	mux.HandleFunc("POST /v1/rooms/{roomId}/punch-tickets/{ticketId}/complete", handleCompletePunchTicket(deps.NatStore))

	// Relay
	mux.HandleFunc("POST /v1/relay/allocations", RequirePlayer(deps.DB, handleCreateRelayAllocation(deps)))

	// P2P Matchmaking
	mux.HandleFunc("POST /v1/matchmaking/tickets", RequirePlayer(deps.DB, handleCreateMatchTicket(deps)))
	mux.HandleFunc("GET /v1/matchmaking/tickets/{ticketId}", RequirePlayer(deps.DB, handleGetMatchTicket(deps.DB)))
	mux.HandleFunc("DELETE /v1/matchmaking/tickets/{ticketId}", RequirePlayer(deps.DB, handleDeleteMatchTicket(deps.DB)))

	// Legacy
	mux.HandleFunc("POST /server/status", handleLegacyServerStatus(deps))
	mux.HandleFunc("GET /v1/servers", handleLegacyServerList(deps.DB))

	// MetaServer matchmaking
	mux.HandleFunc("POST /matchmaking/enqueue", handleMetaServerEnqueue(deps))
	mux.HandleFunc("GET /matchmaking/status/{ticketId}", handleMetaServerStatus(deps))
	mux.HandleFunc("POST /matchmaking/cancel/{ticketId}", handleMetaServerCancel(deps.DB))

	// Stub for host migration
	mux.HandleFunc("POST /v1/rooms/{roomId}/host-migration/", handleNotImplemented())
}
