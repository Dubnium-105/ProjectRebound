package http

import (
	"net/http"
	"time"

	"github.com/projectrebound/matchserver/internal/models"
	"github.com/projectrebound/matchserver/internal/store"
)

func handleCreateRelayAllocation(deps *Deps) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		player := PlayerFromContext(r.Context())

		var req models.CreateRelayAllocationRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
			return
		}

		// Validate room exists
		var hostPlayerID, roomIDStr string
		err := deps.DB.QueryRowContext(r.Context(),
			"SELECT host_player_id, CAST(room_id AS TEXT) FROM rooms WHERE CAST(room_id AS TEXT) = ?",
			req.RoomID,
		).Scan(&hostPlayerID, &roomIDStr)
		if err != nil {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "room not found")
			return
		}
		roomID := roomIDStr

		ttl := time.Duration(deps.Config.MatchServer.RelayAllocationSeconds) * time.Second

		switch req.Role {
		case "host":
			hostTokenHash := store.SHA256Hash(req.HostToken)
			var storedHash string
			err := deps.DB.QueryRowContext(r.Context(),
				"SELECT host_token_hash FROM rooms WHERE CAST(room_id AS TEXT) = ?", roomID,
			).Scan(&storedHash)
			if err != nil || !store.FixedTimeEquals(storedHash, hostTokenHash) {
				writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid host token")
				return
			}

			alloc := deps.RelayStore.CreateHostAllocation(roomID, player.PlayerID, ttl)
			writeJSON(w, http.StatusOK, models.CreateRelayAllocationResponse{
				SessionID: alloc.SessionID,
				Role:      "host",
				Secret:    alloc.Secret,
				RelayHost: "",
				RelayPort: deps.Config.UDPRelayPort,
				ExpiresAt: alloc.ExpiresAt,
			})

		case "client":
			joinTicketHash := store.SHA256Hash(req.JoinTicket)
			var foundRoomID string
			err := deps.DB.QueryRowContext(r.Context(),
				"SELECT room_id FROM room_players WHERE join_ticket_hash = ? AND player_id = ? AND status = 'Reserved'",
				joinTicketHash, player.PlayerID,
			).Scan(&foundRoomID)
			if err != nil || foundRoomID != roomID {
				writeError(w, http.StatusBadRequest, "INVALID_JOIN_TICKET", "join ticket is invalid or expired")
				return
			}

			alloc := deps.RelayStore.CreateClientAllocation(roomID, hostPlayerID, player.PlayerID, ttl)
			writeJSON(w, http.StatusOK, models.CreateRelayAllocationResponse{
				SessionID: alloc.SessionID,
				Role:      "client",
				Secret:    alloc.Secret,
				RelayHost: "",
				RelayPort: deps.Config.UDPRelayPort,
				ExpiresAt: alloc.ExpiresAt,
			})

		default:
			writeError(w, http.StatusBadRequest, "INVALID_ROLE", "role must be 'host' or 'client'")
		}
	}
}
