package connection

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/api"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/auth"
	"github.com/go-chi/chi/v5"
)

type HTTPService interface {
	Create(context.Context, Actor, CreateInput) (Connection, error)
	Get(context.Context, Actor, string) (Connection, error)
	Close(context.Context, Actor, string) (Connection, error)
}

type HTTPHandler struct {
	service HTTPService
	logger  *slog.Logger
}

func NewHTTPHandler(service HTTPService, logger *slog.Logger) *HTTPHandler {
	return &HTTPHandler{service: service, logger: logger}
}

type createRequest struct {
	RoomID       string `json:"room_id"`
	PeerPlayerID string `json:"peer_player_id"`
}

type connectionResponse struct {
	ConnectionID  string      `json:"connection_id"`
	RoomID        string      `json:"room_id"`
	HostPlayerID  string      `json:"host_player_id"`
	PeerPlayerID  string      `json:"peer_player_id"`
	State         State       `json:"state"`
	SelectedPath  Path        `json:"selected_path,omitempty"`
	FailureReason string      `json:"failure_reason,omitempty"`
	ExpiresAt     time.Time   `json:"expires_at"`
	CreatedAt     time.Time   `json:"created_at"`
	UpdatedAt     time.Time   `json:"updated_at"`
	Candidates    []Candidate `json:"candidates"`
}

func (h *HTTPHandler) Create(w http.ResponseWriter, r *http.Request) {
	var request createRequest
	if err := api.DecodeJSON(r, &request); err != nil {
		api.WriteError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request.", map[string]any{"body": err.Error()})
		return
	}
	item, err := h.service.Create(r.Context(), actorFromRequest(r), CreateInput{
		RoomID: request.RoomID, PeerPlayerID: request.PeerPlayerID,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusCreated, toResponse(item))
}

func (h *HTTPHandler) Get(w http.ResponseWriter, r *http.Request) {
	item, err := h.service.Get(r.Context(), actorFromRequest(r), chi.URLParam(r, "connection_id"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusOK, toResponse(item))
}

func (h *HTTPHandler) Delete(w http.ResponseWriter, r *http.Request) {
	item, err := h.service.Close(r.Context(), actorFromRequest(r), chi.URLParam(r, "connection_id"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusOK, toResponse(item))
}

func (h *HTTPHandler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, message, details := errorDetails(err)
	if status >= 500 {
		h.logger.ErrorContext(r.Context(), "connection request failed", "code", code, "error", err)
	}
	api.WriteError(w, r, status, code, message, details)
}

func actorFromRequest(r *http.Request) Actor {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		return Actor{}
	}
	return Actor{PlayerID: principal.Player.ID, AccountStatus: principal.Player.AccountStatus}
}

func toResponse(item Connection) connectionResponse {
	candidates := item.Candidates
	if candidates == nil {
		candidates = make([]Candidate, 0)
	}
	return connectionResponse{
		ConnectionID: item.ID, RoomID: item.RoomID,
		HostPlayerID: item.HostPlayerID, PeerPlayerID: item.PeerPlayerID,
		State: item.State, SelectedPath: item.SelectedPath, FailureReason: item.FailureReason,
		ExpiresAt: item.ExpiresAt, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
		Candidates: candidates,
	}
}
