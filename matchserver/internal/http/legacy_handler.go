package http

import (
	"database/sql"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/projectrebound/matchserver/internal/models"
	"github.com/projectrebound/matchserver/internal/store"
)

func handleLegacyServerStatus(deps *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req models.LegacyServerStatusRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
			return
		}

		now := time.Now()

		// If roomId + hostToken provided, treat as heartbeat
		if req.RoomID != "" && req.HostToken != "" {
			hostTokenHash := store.SHA256Hash(req.HostToken)
			var storedHash string
			err := deps.DB.QueryRowContext(r.Context(),
				"SELECT host_token_hash FROM rooms WHERE room_id = ?", req.RoomID,
			).Scan(&storedHash)
			if err == nil && store.FixedTimeEquals(storedHash, hostTokenHash) {
				deps.DB.ExecContext(r.Context(),
					"UPDATE rooms SET last_seen_at = ?, player_count = ?, server_state = ? WHERE room_id = ?",
					now.UnixMilli(), req.PlayerCount, req.ServerState, req.RoomID,
				)
				writeJSON(w, http.StatusOK, models.LegacyServerStatusResponse{
					Ok:                  true,
					NextHeartbeatSeconds: deps.Config.MatchServer.HeartbeatSeconds,
				})
				return
			}
		}

		// Legacy server entry
		sourceKey := req.Name + ":" + getPublicIP(r)
		sourceKeyHash := store.SHA256Hash(sourceKey)

		var existingID string
		err := deps.DB.QueryRowContext(r.Context(),
			"SELECT server_id FROM legacy_servers WHERE source_key = ?", sourceKeyHash,
		).Scan(&existingID)

		serverID := uuid.New().String()
		if err == nil {
			serverID = existingID
		}

		_, _ = deps.DB.ExecContext(r.Context(),
			`INSERT OR REPLACE INTO legacy_servers (server_id, source_key, name, region, mode, map, endpoint, port, player_count, server_state, version, last_seen_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			serverID, sourceKeyHash, req.Name, req.Region, req.Mode, req.Map, getPublicIP(r), req.Port,
			req.PlayerCount, req.ServerState, req.Version, now.UnixMilli(),
		)

		writeJSON(w, http.StatusOK, models.LegacyServerStatusResponse{
			Ok:                  true,
			ServerID:            serverID,
			NextHeartbeatSeconds: deps.Config.MatchServer.HeartbeatSeconds,
		})
	}
}

func handleLegacyServerList(database *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cutoff := time.Now().Add(-45 * time.Second).UnixMilli()

		rows, err := database.QueryContext(r.Context(),
			`SELECT server_id, name, region, mode, map, endpoint, port, player_count, server_state, version, last_seen_at
			 FROM legacy_servers WHERE last_seen_at > ? ORDER BY last_seen_at DESC LIMIT 100`, cutoff,
		)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "database error")
			return
		}
		defer rows.Close()

		var items []models.LegacyServerSummary
		for rows.Next() {
			var s models.LegacyServerSummary
			var ver sql.NullString
			var lastSeen int64
			if err := rows.Scan(&s.ServerID, &s.Name, &s.Region, &s.Mode, &s.Map,
				&s.Endpoint, &s.Port, &s.PlayerCount, &s.ServerState, &ver, &lastSeen,
			); err != nil {
				continue
			}
			s.Version = ver.String
			s.LastSeenAt = time.UnixMilli(lastSeen)
			items = append(items, s)
		}

		if items == nil {
			items = []models.LegacyServerSummary{}
		}
		writeJSON(w, http.StatusOK, items)
	}
}
