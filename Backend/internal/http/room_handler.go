package http

import (
	"database/sql"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/projectrebound/matchserver/internal/models"
	"github.com/projectrebound/matchserver/internal/store"
)

func handleCreateRoom(deps *Deps) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		player := PlayerFromContext(r.Context())
		if player == nil {
			writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
			return
		}

		var req models.CreateRoomRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
			return
		}

		if req.MaxPlayers < 1 {
			req.MaxPlayers = 12
		}
		if req.Name == "" {
			req.Name = "Custom Room"
		}

		now := time.Now()
		roomID := uuid.New().String()
		hostToken := store.NewToken(32)
		hostTokenHash := store.SHA256Hash(hostToken)

		endpoint := ""
		port := 0

		if req.ProbeID != "" {
			var publicIP string
			var probePort int
			var status string
			err := deps.DB.QueryRowContext(r.Context(),
				"SELECT public_ip, port, status FROM host_probes WHERE probe_id = ? AND player_id = ?",
				req.ProbeID, player.PlayerID,
			).Scan(&publicIP, &probePort, &status)
			if err == nil && status == string(models.HostProbeSucceeded) {
				endpoint = publicIP
				port = probePort
			}
		}
		if endpoint == "" && req.BindingToken != "" {
			endpoint = "pending"
		}

		nowMs := now.UnixMilli()
		_, err := deps.DB.ExecContext(r.Context(),
			`INSERT INTO rooms (room_id, host_player_id, host_probe_id, host_token_hash, name, region, map, mode, version,
			 endpoint, port, max_players, player_count, state, created_at, last_seen_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?)`,
			roomID, player.PlayerID, req.ProbeID, hostTokenHash, req.Name, req.Region, req.Map, req.Mode,
			req.Version, endpoint, port, req.MaxPlayers, models.RoomStateOpen, nowMs, nowMs,
		)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to create room")
			return
		}

		writeJSON(w, http.StatusOK, models.CreateRoomResponse{
			RoomID:           roomID,
			HostToken:        hostToken,
			HeartbeatSeconds: deps.Config.MatchServer.HeartbeatSeconds,
		})
	}
}

func handleListRooms(database *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		page, _ := strconv.Atoi(q.Get("page"))
		if page < 1 {
			page = 1
		}
		pageSize, _ := strconv.Atoi(q.Get("pageSize"))
		if pageSize < 1 || pageSize > 100 {
			pageSize = 20
		}

		where := []string{"(state = 'Open' OR state = 'Starting')"}
		args := []any{}

		if region := q.Get("region"); region != "" {
			where = append(where, "region = ?")
			args = append(args, region)
		}
		if m := q.Get("map"); m != "" {
			where = append(where, "map = ?")
			args = append(args, m)
		}
		if mode := q.Get("mode"); mode != "" {
			where = append(where, "mode = ?")
			args = append(args, mode)
		}
		if ver := q.Get("version"); ver != "" {
			where = append(where, "version = ?")
			args = append(args, ver)
		}

		whereClause := strings.Join(where, " AND ")

		var total int
		err := database.QueryRowContext(r.Context(),
			"SELECT COUNT(*) FROM rooms WHERE "+whereClause, args...,
		).Scan(&total)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "database error")
			return
		}

		offset := (page - 1) * pageSize
		queryArgs := append(args, pageSize, offset)
		rows, err := database.QueryContext(r.Context(),
			`SELECT room_id, host_player_id, name, region, map, mode, version, endpoint, port,
			 player_count, max_players, server_state, state, last_seen_at, ended_reason
			 FROM rooms WHERE `+whereClause+` ORDER BY player_count DESC, last_seen_at DESC LIMIT ? OFFSET ?`,
			queryArgs...,
		)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "database error")
			return
		}
		defer rows.Close()

		items := make([]models.RoomSummary, 0)
		for rows.Next() {
			var s models.RoomSummary
			var hostID sql.NullString
			var srvState sql.NullString
			var endedReason sql.NullString
			var lastSeen int64
			if err := rows.Scan(&s.RoomID, &hostID, &s.Name, &s.Region, &s.Map, &s.Mode, &s.Version,
				&s.Endpoint, &s.Port, &s.PlayerCount, &s.MaxPlayers, &srvState, &s.State, &lastSeen, &endedReason,
			); err != nil {
				continue
			}
			s.HostPlayerID = hostID.String
			s.ServerState = srvState.String
			s.EndedReason = endedReason.String
			s.LastSeenAt = time.UnixMilli(lastSeen)
			items = append(items, s)
		}

		writeJSON(w, http.StatusOK, models.PagedRoomsResponse{
			Items:    items,
			Page:     page,
			PageSize: pageSize,
			Total:    total,
		})
	}
}

func handleGetRoom(database *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		roomID := r.PathValue("roomId")

		var s models.RoomSummary
		var hostID sql.NullString
		var srvState sql.NullString
		var endedReason sql.NullString
		var lastSeen int64

		err := database.QueryRowContext(r.Context(),
			`SELECT room_id, host_player_id, name, region, map, mode, version, endpoint, port,
			 player_count, max_players, server_state, state, last_seen_at, ended_reason
			 FROM rooms WHERE room_id = ?`, roomID,
		).Scan(&s.RoomID, &hostID, &s.Name, &s.Region, &s.Map, &s.Mode, &s.Version,
			&s.Endpoint, &s.Port, &s.PlayerCount, &s.MaxPlayers, &srvState, &s.State, &lastSeen, &endedReason,
		)
		if err != nil {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "room not found")
			return
		}
		s.HostPlayerID = hostID.String
		s.ServerState = srvState.String
		s.EndedReason = endedReason.String
		s.LastSeenAt = time.UnixMilli(lastSeen)

		writeJSON(w, http.StatusOK, s)
	}
}

func handleJoinRoom(deps *Deps) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		player := PlayerFromContext(r.Context())
		roomID := r.PathValue("roomId")

		var req models.JoinRoomRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
			return
		}

		var roomState string
		var roomVersion string
		var hostPlayerID sql.NullString
		var endpoint string
		var port int
		var maxPlayers, playerCount int
		err := deps.DB.QueryRowContext(r.Context(),
			"SELECT state, version, host_player_id, endpoint, port, max_players, player_count FROM rooms WHERE room_id = ?",
			roomID,
		).Scan(&roomState, &roomVersion, &hostPlayerID, &endpoint, &port, &maxPlayers, &playerCount)
		if err != nil {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "room not found")
			return
		}

		if roomState != string(models.RoomStateOpen) && roomState != string(models.RoomStateStarting) {
			writeError(w, http.StatusBadRequest, "ROOM_CLOSED", "room is not accepting players")
			return
		}
		if req.Version != "" && req.Version != roomVersion {
			writeError(w, http.StatusBadRequest, "VERSION_MISMATCH", "game version mismatch")
			return
		}
		if playerCount >= maxPlayers {
			writeError(w, http.StatusBadRequest, "ROOM_FULL", "room is full")
			return
		}

		now := time.Now()
		joinTicket := store.NewToken(32)
		joinTicketHash := store.SHA256Hash(joinTicket)
		reservationID := uuid.New().String()
		ttl := time.Duration(deps.Config.MatchServer.JoinTicketSeconds) * time.Second
		expiresAt := now.Add(ttl)

		_, err = deps.DB.ExecContext(r.Context(),
			`INSERT INTO room_players (room_player_id, room_id, player_id, join_ticket_hash, status, joined_at, expires_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			reservationID, roomID, player.PlayerID, joinTicketHash, models.RoomPlayerReserved,
			now.UnixMilli(), expiresAt.UnixMilli(),
		)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to create reservation")
			return
		}

		_, _ = deps.DB.ExecContext(r.Context(),
			"UPDATE rooms SET player_count = (SELECT COUNT(*) FROM room_players WHERE room_id = ? AND status = 'Reserved') WHERE room_id = ?",
			roomID, roomID,
		)

		connect := endpoint + ":" + strconv.Itoa(port)
		writeJSON(w, http.StatusOK, models.JoinRoomResponse{
			Connect:   connect,
			JoinTicket: joinTicket,
			ExpiresAt:  expiresAt,
		})
	}
}

func handleLeaveRoom(database *sql.DB) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		player := PlayerFromContext(r.Context())
		roomID := r.PathValue("roomId")

		var req models.LeaveRoomRequest
		decodeJSON(r, &req)

		if req.JoinTicket != "" {
			joinTicketHash := store.SHA256Hash(req.JoinTicket)
			_, _ = database.ExecContext(r.Context(),
				"UPDATE room_players SET status = ? WHERE room_id = ? AND join_ticket_hash = ?",
				models.RoomPlayerLeft, roomID, joinTicketHash,
			)
		} else {
			_, _ = database.ExecContext(r.Context(),
				"UPDATE room_players SET status = ? WHERE room_id = ? AND player_id = ? AND status = 'Reserved'",
				models.RoomPlayerLeft, roomID, player.PlayerID,
			)
		}

		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func handleHeartbeatRoom(deps *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		roomID := r.PathValue("roomId")

		var req models.RoomHeartbeatRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
			return
		}

		hostTokenHash := store.SHA256Hash(req.HostToken)

		var storedHash string
		var state string
		err := deps.DB.QueryRowContext(r.Context(),
			"SELECT host_token_hash, state FROM rooms WHERE room_id = ?", roomID,
		).Scan(&storedHash, &state)

		if err != nil || !store.FixedTimeEquals(storedHash, hostTokenHash) {
			writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid host token")
			return
		}

		now := time.Now()
		_, _ = deps.DB.ExecContext(r.Context(),
			"UPDATE rooms SET last_seen_at = ?, player_count = ?, server_state = ? WHERE room_id = ?",
			now.UnixMilli(), req.PlayerCount, req.ServerState, roomID,
		)

		writeJSON(w, http.StatusOK, models.RoomHeartbeatResponse{
			Ok:                  true,
			NextHeartbeatSeconds: deps.Config.MatchServer.HeartbeatSeconds,
		})
	}
}

func handleStartRoom(database *sql.DB) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		player := PlayerFromContext(r.Context())
		roomID := r.PathValue("roomId")

		_, _ = database.ExecContext(r.Context(),
			"UPDATE rooms SET state = ? WHERE room_id = ? AND host_player_id = ?",
			models.RoomStateInGame, roomID, player.PlayerID,
		)

		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func handleEndRoom(database *sql.DB) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		player := PlayerFromContext(r.Context())
		roomID := r.PathValue("roomId")

		_, _ = database.ExecContext(r.Context(),
			"UPDATE rooms SET state = ?, ended_reason = ? WHERE room_id = ? AND host_player_id = ?",
			models.RoomStateEnded, "host_ended", roomID, player.PlayerID,
		)

		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}
