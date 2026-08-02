package gameserver

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/api"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/auth"
	"github.com/go-chi/chi/v5"
)

type HTTPService interface {
	IssueRegistrationCredential(context.Context, RegistrationCredentialInput) (RegistrationCredentialResult, error)
	Register(context.Context, RegistrationInput, string) (RegistrationResult, error)
	Heartbeat(context.Context, string, string, HeartbeatInput) (Server, error)
	RotateCredential(context.Context, string, string) (CredentialRotationResult, error)
	Deregister(context.Context, string, string) error
	Get(context.Context, string) (Server, error)
	List(context.Context, ListFilter) (ListResult, error)
	HeartbeatInterval() int
}

type HTTPHandler struct {
	service HTTPService
	logger  *slog.Logger
}

func NewHTTPHandler(service HTTPService, logger *slog.Logger) *HTTPHandler {
	return &HTTPHandler{service: service, logger: logger}
}

type registrationRequest struct {
	InstanceID  string `json:"instance_id"`
	DisplayName string `json:"display_name"`
	Region      string `json:"region"`
	Mode        string `json:"mode"`
	Version     string `json:"version"`
	PublicHost  string `json:"public_host"`
	PublicPort  int    `json:"public_port"`
	MaxPlayers  int    `json:"max_players"`
}

type registrationCredentialRequest struct {
	InstanceID string `json:"instance_id"`
}

type heartbeatRequest struct {
	State       State `json:"state"`
	PlayerCount int   `json:"player_count"`
}

type endpointResponse struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

type publicServerResponse struct {
	ServerID        string           `json:"server_id"`
	DisplayName     string           `json:"display_name"`
	Region          string           `json:"region"`
	Mode            string           `json:"mode"`
	Version         string           `json:"version"`
	Endpoint        endpointResponse `json:"endpoint"`
	MaxPlayers      int              `json:"max_players"`
	PlayerCount     int              `json:"player_count"`
	State           State            `json:"state"`
	LastHeartbeatAt time.Time        `json:"last_heartbeat_at"`
}

func (h *HTTPHandler) IssueRegistrationCredential(w http.ResponseWriter, r *http.Request) {
	var request registrationCredentialRequest
	if err := api.DecodeJSON(r, &request); err != nil {
		api.WriteError(w, r, 400, "INVALID_REQUEST", "Invalid request.", map[string]any{"body": err.Error()})
		return
	}
	principal := auth.PrincipalFromContext(r.Context())
	playerID := ""
	if principal != nil {
		playerID = principal.Player.ID
	}
	result, err := h.service.IssueRegistrationCredential(r.Context(), RegistrationCredentialInput{
		InstanceID: request.InstanceID, PlayerID: playerID,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	api.WriteData(w, r, http.StatusCreated, map[string]any{
		"registration_id":    result.Credential.ID,
		"instance_id":        result.Credential.InstanceID,
		"registration_token": result.Plaintext,
		"expires_at":         result.Credential.ExpiresAt,
	})
}

func (h *HTTPHandler) Register(w http.ResponseWriter, r *http.Request) {
	var request registrationRequest
	if err := api.DecodeJSON(r, &request); err != nil {
		api.WriteError(w, r, 400, "INVALID_REQUEST", "Invalid request.", map[string]any{"body": err.Error()})
		return
	}
	result, err := h.service.Register(r.Context(), RegistrationInput{
		InstanceID: request.InstanceID, DisplayName: request.DisplayName,
		Region: request.Region, Mode: request.Mode, Version: request.Version,
		PublicHost: request.PublicHost, PublicPort: request.PublicPort, MaxPlayers: request.MaxPlayers,
	}, bearerToken(r))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusCreated, map[string]any{
		"server_id":                  result.Server.ID,
		"server_token":               result.ServerToken,
		"heartbeat_interval_seconds": result.HeartbeatInterval,
		"token_expires_at":           result.Server.TokenExpiresAt,
		"credential_generation":      result.Server.CredentialGeneration,
	})
}

func (h *HTTPHandler) Heartbeat(w http.ResponseWriter, r *http.Request) {
	var request heartbeatRequest
	if err := api.DecodeJSON(r, &request); err != nil {
		api.WriteError(w, r, 400, "INVALID_REQUEST", "Invalid request.", map[string]any{"body": err.Error()})
		return
	}
	updated, err := h.service.Heartbeat(r.Context(), chi.URLParam(r, "server_id"), bearerToken(r), HeartbeatInput{
		State: request.State, PlayerCount: request.PlayerCount,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, 200, map[string]any{
		"server":                 toPublicResponse(updated),
		"next_heartbeat_seconds": h.service.HeartbeatInterval(),
	})
}

func (h *HTTPHandler) RotateCredential(w http.ResponseWriter, r *http.Request) {
	result, err := h.service.RotateCredential(
		r.Context(), chi.URLParam(r, "server_id"), bearerToken(r),
	)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	api.WriteData(w, r, http.StatusOK, map[string]any{
		"server_id":             result.ServerID,
		"server_token":          result.ServerToken,
		"token_expires_at":      result.TokenExpiresAt,
		"previous_valid_until":  result.PreviousValidUntil,
		"credential_generation": result.CredentialGeneration,
	})
}

func (h *HTTPHandler) Deregister(w http.ResponseWriter, r *http.Request) {
	if err := h.service.Deregister(r.Context(), chi.URLParam(r, "server_id"), bearerToken(r)); err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, 200, map[string]bool{"deregistered": true})
}

func (h *HTTPHandler) Get(w http.ResponseWriter, r *http.Request) {
	item, err := h.service.Get(r.Context(), chi.URLParam(r, "server_id"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, 200, toPublicResponse(item))
}

func (h *HTTPHandler) List(w http.ResponseWriter, r *http.Request) {
	limit, err := strconv.Atoi(defaultString(r.URL.Query().Get("limit"), "50"))
	if err != nil {
		api.WriteError(w, r, 400, "INVALID_REQUEST", "Invalid limit.", nil)
		return
	}
	result, err := h.service.List(r.Context(), ListFilter{
		Region: r.URL.Query().Get("region"), Mode: r.URL.Query().Get("mode"),
		Version: r.URL.Query().Get("version"), State: State(r.URL.Query().Get("state")),
		Cursor: r.URL.Query().Get("cursor"), Limit: limit,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	items := make([]publicServerResponse, 0, len(result.Items))
	for _, item := range result.Items {
		items = append(items, toPublicResponse(item))
	}
	api.WriteData(w, r, 200, map[string]any{"items": items, "next_cursor": result.NextCursor})
}

func (h *HTTPHandler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, message, details := errorDetails(err)
	if status >= 500 {
		h.logger.ErrorContext(r.Context(), "game server request failed", "code", code, "error", err)
	}
	api.WriteError(w, r, status, code, message, details)
}

func bearerToken(r *http.Request) string {
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
}

func toPublicResponse(item Server) publicServerResponse {
	return publicServerResponse{
		ServerID: item.ID, DisplayName: item.DisplayName, Region: item.Region,
		Mode: item.Mode, Version: item.Version,
		Endpoint:   endpointResponse{Host: item.PublicHost, Port: item.PublicPort},
		MaxPlayers: item.MaxPlayers, PlayerCount: item.PlayerCount,
		State: item.State, LastHeartbeatAt: item.LastHeartbeatAt,
	}
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
