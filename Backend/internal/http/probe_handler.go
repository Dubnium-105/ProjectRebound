package http

import (
	"database/sql"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/projectrebound/matchserver/internal/models"
	"github.com/projectrebound/matchserver/internal/store"
)

func handleCreateHostProbe(deps *Deps) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		player := PlayerFromContext(r.Context())

		var req models.CreateHostProbeRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
			return
		}

		if req.Port < 1 || req.Port > 65535 {
			writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "port must be between 1 and 65535")
			return
		}

		now := time.Now()
		publicIP := getPublicIP(r)
		nonce := store.NewToken(18)
		probeID := uuid.New().String()
		ttl := time.Duration(deps.Config.MatchServer.HostProbeSeconds) * time.Second
		expiresAt := now.Add(ttl)

		_, err := deps.DB.ExecContext(r.Context(),
			`INSERT INTO host_probes (probe_id, player_id, public_ip, port, nonce, status, created_at, expires_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			probeID, player.PlayerID, publicIP, req.Port, nonce, models.HostProbePending,
			now.UnixMilli(), expiresAt.UnixMilli(),
		)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to create probe")
			return
		}

		if deps.ProbeSender != nil {
			deps.ProbeSender.Send(publicIP, req.Port, nonce)
		}

		writeJSON(w, http.StatusOK, models.CreateHostProbeResponse{
			ProbeID:   probeID,
			PublicIP:  publicIP,
			Port:      req.Port,
			Nonce:     nonce,
			ExpiresAt: expiresAt,
		})
	}
}

func handleConfirmHostProbe(database *sql.DB) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		player := PlayerFromContext(r.Context())
		probeID := r.PathValue("probeId")

		var req models.ConfirmHostProbeRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
			return
		}

		var storedNonce, publicIP string
		var port int
		var status string
		var createdAt, expiresAt int64
		err := database.QueryRowContext(r.Context(),
			"SELECT nonce, public_ip, port, status, created_at, expires_at FROM host_probes WHERE probe_id = ? AND player_id = ?",
			probeID, player.PlayerID,
		).Scan(&storedNonce, &publicIP, &port, &status, &createdAt, &expiresAt)
		if err != nil {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "probe not found")
			return
		}

		if status != string(models.HostProbePending) {
			writeError(w, http.StatusBadRequest, "PROBE_EXPIRED", "probe is no longer pending")
			return
		}

		if !store.FixedTimeEquals(storedNonce, req.Nonce) {
			writeError(w, http.StatusBadRequest, "NONCE_MISMATCH", "nonce does not match")
			return
		}

		_, _ = database.ExecContext(r.Context(),
			"UPDATE host_probes SET status = ? WHERE probe_id = ?",
			models.HostProbeSucceeded, probeID,
		)

		writeJSON(w, http.StatusOK, models.HostProbeResponse{
			ProbeID:   probeID,
			Status:    models.HostProbeSucceeded,
			PublicIP:  publicIP,
			Port:      port,
			ExpiresAt: time.UnixMilli(expiresAt),
		})
	}
}
