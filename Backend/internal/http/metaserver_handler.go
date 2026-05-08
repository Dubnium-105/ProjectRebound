package http

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/projectrebound/matchserver/internal/models"
)

func handleMetaServerEnqueue(deps *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req models.MatchmakingEnqueueRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
			return
		}

		if req.UserID == "" {
			writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "userId is required")
			return
		}

		now := time.Now()
		ticketID := uuid.New().String()
		ttl := time.Duration(deps.Config.MatchServer.MetaserverMatchTimeoutSeconds) * time.Second
		expiresAt := now.Add(ttl)

		qosJSON := "{}"
		if req.QoSData != nil {
			if b, err := json.Marshal(req.QoSData); err == nil {
				qosJSON = string(b)
			}
		}

		_, err := deps.DB.ExecContext(r.Context(),
			`INSERT INTO match_tickets (ticket_id, player_id, region, mode, version, can_host, max_players,
			 state, created_at, expires_at, metaserver_mode, qos_data_json)
			 VALUES (?, ?, ?, ?, ?, 0, 12, ?, ?, ?, 1, ?)`,
			ticketID, req.UserID, req.RegionID, req.GameMode, "dev",
			models.MatchTicketWaiting, now.UnixMilli(), expiresAt.UnixMilli(), qosJSON,
		)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to create ticket")
			return
		}

		writeJSON(w, http.StatusOK, models.MatchmakingEnqueueResponse{
			TicketID: ticketID,
			Status:   "queued",
		})
	}
}

func handleMetaServerStatus(deps *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ticketID := r.PathValue("ticketId")

		var state string
		var assignedRoomID sql.NullString
		err := deps.DB.QueryRowContext(r.Context(),
			"SELECT state, assigned_room_id FROM match_tickets WHERE ticket_id = ? AND metaserver_mode = 1",
			ticketID,
		).Scan(&state, &assignedRoomID)
		if err != nil {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "ticket not found")
			return
		}

		status := metaserverStateToStatus(models.MatchTicketState(state))
		resp := models.MatchmakingStatusResponse{
			TicketID: ticketID,
			Status:   status,
		}

		if assignedRoomID.Valid && status == "found" {
			var endpoint string
			var port int
			err := deps.DB.QueryRowContext(r.Context(),
				"SELECT endpoint, port FROM rooms WHERE room_id = ?",
				assignedRoomID.String,
			).Scan(&endpoint, &port)
			if err == nil {
				resp.ServerIP = endpoint
				resp.ServerPort = port
			}
		}

		writeJSON(w, http.StatusOK, resp)
	}
}

func handleMetaServerCancel(database *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ticketID := r.PathValue("ticketId")

		_, err := database.ExecContext(r.Context(),
			"UPDATE match_tickets SET state = ? WHERE ticket_id = ? AND state = 'Waiting' AND metaserver_mode = 1",
			models.MatchTicketCanceled, ticketID,
		)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "database error")
			return
		}

		writeJSON(w, http.StatusOK, models.MatchmakingCancelResponse{
			TicketID: ticketID,
			Status:   "cancelled",
		})
	}
}

func metaserverStateToStatus(state models.MatchTicketState) string {
	switch state {
	case models.MatchTicketWaiting:
		return "queued"
	case models.MatchTicketMatched, models.MatchTicketHostAssigned:
		return "found"
	case models.MatchTicketCanceled:
		return "cancelled"
	case models.MatchTicketExpired:
		return "timeout"
	case models.MatchTicketFailed:
		return "failed"
	default:
		return "queued"
	}
}
