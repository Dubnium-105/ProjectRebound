package http

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/projectrebound/matchserver/internal/db"
	"github.com/projectrebound/matchserver/internal/models"
	"github.com/projectrebound/matchserver/internal/store"
)

type contextKey string

const playerKey contextKey = "player"

type AuthPlayer struct {
	PlayerID    string
	DisplayName string
}

func RequirePlayer(database *sql.DB, next func(w http.ResponseWriter, r *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth == "" || !strings.HasPrefix(auth, "Bearer ") {
			writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Bearer player token is required.")
			return
		}
		token := auth[7:]
		tokenHash := store.SHA256Hash(token)

		var p db.Player
		var createdAt, lastSeenAt int64
		err := database.QueryRowContext(r.Context(),
			"SELECT player_id, display_name, device_token_hash, created_at, last_seen_at, status FROM players WHERE device_token_hash = ?",
			tokenHash,
		).Scan(&p.PlayerID, &p.DisplayName, &p.DeviceTokenHash, &createdAt, &lastSeenAt, &p.Status)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid token")
				return
			}
			slog.Error("auth db error", "error", err)
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "internal server error")
			return
		}
		p.CreatedAt = time.UnixMilli(createdAt)
		p.LastSeenAt = time.UnixMilli(lastSeenAt)

		if p.Status != models.PlayerStatusActive {
			writeError(w, http.StatusForbidden, "DISABLED", "player account is disabled")
			return
		}

		player := &AuthPlayer{PlayerID: p.PlayerID, DisplayName: p.DisplayName}
		ctx := context.WithValue(r.Context(), playerKey, player)
		next(w, r.WithContext(ctx))
	}
}

func PlayerFromContext(ctx context.Context) *AuthPlayer {
	p, _ := ctx.Value(playerKey).(*AuthPlayer)
	return p
}

func generatePlayerID() string {
	return uuid.New().String()
}
