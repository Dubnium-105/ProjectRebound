package http

import (
	"database/sql"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/projectrebound/matchserver/internal/models"
	"github.com/projectrebound/matchserver/internal/store"
)

func handleGuestAuth(database *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req models.GuestAuthRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
			return
		}

		now := time.Now()
		token := req.DeviceToken
		if token == "" {
			token = store.NewToken(32)
		}
		tokenHash := store.SHA256Hash(token)

		displayName := req.DisplayName
		if displayName == "" {
			displayName = "Guest"
		}
		if len(displayName) > 32 {
			displayName = displayName[:32]
		}

		var playerID string
		var existingName string
		var createdAt, lastSeenAt int64
		err := database.QueryRowContext(r.Context(),
			"SELECT player_id, display_name, created_at, last_seen_at FROM players WHERE device_token_hash = ?",
			tokenHash,
		).Scan(&playerID, &existingName, &createdAt, &lastSeenAt)

		if err == sql.ErrNoRows {
			playerID = uuid.New().String()
			nowMs := now.UnixMilli()
			_, err = database.ExecContext(r.Context(),
				"INSERT INTO players (player_id, display_name, device_token_hash, created_at, last_seen_at, status) VALUES (?, ?, ?, ?, ?, ?)",
				playerID, displayName, tokenHash, nowMs, nowMs, models.PlayerStatusActive,
			)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to create player")
				return
			}
		} else if err != nil {
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "database error")
			return
		} else {
			nowMs := now.UnixMilli()
			_, _ = database.ExecContext(r.Context(),
				"UPDATE players SET display_name = ?, last_seen_at = ? WHERE player_id = ?",
				displayName, nowMs, playerID,
			)
		}

		writeJSON(w, http.StatusOK, models.GuestAuthResponse{
			PlayerID:    playerID,
			DisplayName: displayName,
			AccessToken: token,
		})
	}
}
