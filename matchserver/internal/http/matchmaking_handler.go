package http

import (
	"database/sql"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/projectrebound/matchserver/internal/models"
)

func handleCreateMatchTicket(deps *Deps) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		player := PlayerFromContext(r.Context())

		var req models.CreateMatchTicketRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
			return
		}

		if req.MaxPlayers < 1 {
			req.MaxPlayers = 12
		}

		now := time.Now()
		ticketID := uuid.New().String()
		ttl := time.Duration(deps.Config.MatchServer.MatchTicketSeconds) * time.Second
		expiresAt := now.Add(ttl)

		_, err := deps.DB.ExecContext(r.Context(),
			`INSERT INTO match_tickets (ticket_id, player_id, region, map, mode, version, can_host, probe_id, room_name,
			 max_players, state, created_at, expires_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			ticketID, player.PlayerID, req.Region, req.Map, req.Mode, req.Version,
			req.CanHost, req.ProbeID, req.RoomName, req.MaxPlayers,
			models.MatchTicketWaiting, now.UnixMilli(), expiresAt.UnixMilli(),
		)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to create ticket")
			return
		}

		writeJSON(w, http.StatusOK, models.CreateMatchTicketResponse{
			TicketID:  ticketID,
			State:     models.MatchTicketWaiting,
			ExpiresAt: expiresAt,
		})
	}
}

func handleGetMatchTicket(database *sql.DB) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		player := PlayerFromContext(r.Context())
		ticketID := r.PathValue("ticketId")

		var t models.MatchTicketResponse
		var state string
		var assignedRoomID, hostToken, joinTicket, failureReason sql.NullString
		var createdAt, expiresAt int64

		err := database.QueryRowContext(r.Context(),
			`SELECT ticket_id, state, assigned_room_id, host_token_plain, join_ticket_plain, failure_reason, created_at, expires_at
			 FROM match_tickets WHERE ticket_id = ? AND player_id = ?`,
			ticketID, player.PlayerID,
		).Scan(&t.TicketID, &state, &assignedRoomID, &hostToken, &joinTicket, &failureReason, &createdAt, &expiresAt)
		if err != nil {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "ticket not found")
			return
		}

		t.State = models.MatchTicketState(state)
		t.AssignedRoomID = assignedRoomID.String
		t.HostToken = hostToken.String
		t.JoinTicket = joinTicket.String
		t.FailureReason = failureReason.String
		t.ExpiresAt = time.UnixMilli(expiresAt)

		// If matched, fetch room info
		if t.AssignedRoomID != "" && (t.State == models.MatchTicketMatched || t.State == models.MatchTicketHostAssigned) {
			var room models.RoomSummary
			var hostID sql.NullString
			var srvState sql.NullString
			var endedReason sql.NullString
			var lastSeen int64
			rowErr := database.QueryRowContext(r.Context(),
				`SELECT room_id, host_player_id, name, region, map, mode, version, endpoint, port,
				 player_count, max_players, server_state, state, last_seen_at, ended_reason
				 FROM rooms WHERE room_id = ?`, t.AssignedRoomID,
			).Scan(&room.RoomID, &hostID, &room.Name, &room.Region, &room.Map, &room.Mode, &room.Version,
				&room.Endpoint, &room.Port, &room.PlayerCount, &room.MaxPlayers, &srvState, &room.State, &lastSeen, &endedReason,
			)
			if rowErr == nil {
				room.HostPlayerID = hostID.String
				room.ServerState = srvState.String
				room.EndedReason = endedReason.String
				room.LastSeenAt = time.UnixMilli(lastSeen)
				t.Room = &room
				t.Connect = room.Endpoint + ":" + itoa(room.Port)
			}
		}

		writeJSON(w, http.StatusOK, t)
	}
}

func handleDeleteMatchTicket(database *sql.DB) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		player := PlayerFromContext(r.Context())
		ticketID := r.PathValue("ticketId")

		result, err := database.ExecContext(r.Context(),
			"UPDATE match_tickets SET state = ? WHERE ticket_id = ? AND player_id = ? AND state = 'Waiting'",
			models.MatchTicketCanceled, ticketID, player.PlayerID,
		)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "database error")
			return
		}
		if n, _ := result.RowsAffected(); n == 0 {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "ticket not found or not cancellable")
			return
		}

		writeJSON(w, http.StatusOK, map[string]string{"ticketId": ticketID, "status": "canceled"})
	}
}
