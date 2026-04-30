package http

import (
	"database/sql"
	"net/http"
	"time"

	"github.com/projectrebound/matchserver/internal/models"
	"github.com/projectrebound/matchserver/internal/store"
)

func handleCreatePunchTicket(deps *Deps) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		player := PlayerFromContext(r.Context())
		roomID := r.PathValue("roomId")

		var req models.CreatePunchTicketRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
			return
		}

		// Validate join ticket
		joinTicketHash := store.SHA256Hash(req.JoinTicket)
		var roomPlayerID, dbRoomID string
		err := deps.DB.QueryRowContext(r.Context(),
			"SELECT room_player_id, room_id FROM room_players WHERE join_ticket_hash = ? AND player_id = ? AND status = 'Reserved'",
			joinTicketHash, player.PlayerID,
		).Scan(&roomPlayerID, &dbRoomID)
		if err != nil || dbRoomID != roomID {
			writeError(w, http.StatusBadRequest, "INVALID_JOIN_TICKET", "join ticket is invalid or expired")
			return
		}

		// Validate binding
		clientBinding := deps.NatStore.GetBinding(req.BindingToken, player.PlayerID)
		if clientBinding == nil {
			writeError(w, http.StatusBadRequest, "INVALID_BINDING", "NAT binding not found or not confirmed")
			return
		}
		clientEndpoint := clientBinding.PublicIP + ":" + itoa(clientBinding.PublicPort)

		// Get host endpoint from room
		var hostPlayerID, endpoint string
		var port int
		err = deps.DB.QueryRowContext(r.Context(),
			"SELECT host_player_id, endpoint, port FROM rooms WHERE room_id = ?",
			roomID,
		).Scan(&hostPlayerID, &endpoint, &port)
		if err != nil {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "room not found")
			return
		}
		hostEndpoint := endpoint + ":" + itoa(port)

		ttl := time.Duration(deps.Config.MatchServer.PunchTicketSeconds) * time.Second
		ticket := deps.NatStore.CreatePunchTicket(
			roomID, hostPlayerID, player.PlayerID,
			hostEndpoint, "", clientEndpoint, req.ClientLocalEndpoint,
			ttl,
		)

		writeJSON(w, http.StatusOK, models.CreatePunchTicketResponse{
			TicketID:            ticket.TicketID,
			State:               ticket.State,
			Nonce:               ticket.Nonce,
			HostEndpoint:        ticket.HostEndpoint,
			HostLocalEndpoint:   ticket.HostLocalEndpoint,
			ClientEndpoint:      ticket.ClientEndpoint,
			ClientLocalEndpoint: ticket.ClientLocalEndpoint,
			ExpiresAt:           ticket.ExpiresAt,
		})
	}
}

func handleGetPunchTickets(deps *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		roomID := r.PathValue("roomId")
		hostToken := r.URL.Query().Get("hostToken")
		if hostToken == "" {
			writeError(w, http.StatusBadRequest, "MISSING_HOST_TOKEN", "hostToken query parameter is required")
			return
		}

		hostTokenHash := store.SHA256Hash(hostToken)
		var hostPlayerID sql.NullString
		var storedHash string
		err := deps.DB.QueryRowContext(r.Context(),
			"SELECT host_player_id, host_token_hash FROM rooms WHERE room_id = ?",
			roomID,
		).Scan(&hostPlayerID, &storedHash)
		if err != nil || !store.FixedTimeEquals(storedHash, hostTokenHash) {
			writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid host token")
			return
		}

		tickets := deps.NatStore.GetHostTickets(roomID, hostPlayerID.String)
		items := make([]models.PunchTicketResponse, 0, len(tickets))
		for _, t := range tickets {
			items = append(items, models.PunchTicketResponse{
				TicketID:            t.TicketID,
				State:               t.State,
				Nonce:               t.Nonce,
				HostEndpoint:        t.HostEndpoint,
				HostLocalEndpoint:   t.HostLocalEndpoint,
				ClientEndpoint:      t.ClientEndpoint,
				ClientLocalEndpoint: t.ClientLocalEndpoint,
				ExpiresAt:           t.ExpiresAt,
			})
		}

		writeJSON(w, http.StatusOK, models.ListPunchTicketsResponse{Items: items})
	}
}

func handleCompletePunchTicket(natStore *store.NatTraversalStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ticketID := r.PathValue("ticketId")
		natStore.CompletePunchTicket(ticketID)
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}
