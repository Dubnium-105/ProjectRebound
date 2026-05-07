package http

import (
	"net/http"
	"time"

	"github.com/projectrebound/matchserver/internal/models"
)

func handleCreateNatBinding(deps *Deps) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		player := PlayerFromContext(r.Context())

		var req models.CreateNatBindingRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
			return
		}

		if req.LocalPort < 1 || req.LocalPort > 65535 {
			writeError(w, http.StatusBadRequest, "VALIDATION_ERROR", "localPort must be between 1 and 65535")
			return
		}

		ttl := time.Duration(deps.Config.MatchServer.NatBindingSeconds) * time.Second
		binding := deps.NatStore.CreateBinding(player.PlayerID, req.LocalPort, req.Role, req.RoomID, ttl)

		writeJSON(w, http.StatusOK, models.CreateNatBindingResponse{
			BindingToken: binding.BindingToken,
			UDPHost:      "",
			UDPPort:      deps.Config.UDPRendezvousPort,
			ExpiresAt:    binding.ExpiresAt,
		})
	}
}

func handleConfirmNatBinding(deps *Deps) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		player := PlayerFromContext(r.Context())
		bindingToken := r.PathValue("bindingToken")

		binding := deps.NatStore.GetBinding(bindingToken, player.PlayerID)
		if binding == nil {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "binding not found or not confirmed via UDP")
			return
		}

		writeJSON(w, http.StatusOK, models.ConfirmNatBindingResponse{
			BindingToken: binding.BindingToken,
			PublicIP:     binding.PublicIP,
			PublicPort:   binding.PublicPort,
			LocalPort:    binding.LocalPort,
			Role:         binding.Role,
			RoomID:       binding.RoomID,
			ExpiresAt:    binding.ExpiresAt,
		})
	}
}
