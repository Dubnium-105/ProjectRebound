package p2proom

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

const hostTokenHeader = "X-Room-Host-Token"

type HTTPService interface {
	Create(context.Context, Actor, CreateInput) (CreateResult, error)
	Get(context.Context, string) (Room, error)
	List(context.Context, ListFilter) (ListResult, error)
	Join(context.Context, Actor, string, string) (Room, error)
	Leave(context.Context, Actor, string) (Room, error)
	Heartbeat(context.Context, Actor, string, string) (Room, error)
	Start(context.Context, Actor, string, string) (Room, error)
	Delete(context.Context, Actor, string, string) (Room, error)
	HeartbeatInterval() int
}

type HTTPHandler struct {
	service HTTPService
	logger  *slog.Logger
}

func NewHTTPHandler(service HTTPService, logger *slog.Logger) *HTTPHandler {
	return &HTTPHandler{service: service, logger: logger}
}

type createRequest struct {
	DisplayName string `json:"display_name"`
	Region      string `json:"region"`
	Mode        string `json:"mode"`
	Version     string `json:"version"`
	MaxPlayers  int    `json:"max_players"`
}

type joinRequest struct {
	Version string `json:"version"`
}

type publicRoomResponse struct {
	RoomID          string    `json:"room_id"`
	HostPlayerID    string    `json:"host_player_id"`
	DisplayName     string    `json:"display_name"`
	Region          string    `json:"region"`
	Mode            string    `json:"mode"`
	Version         string    `json:"version"`
	MaxPlayers      int       `json:"max_players"`
	PlayerCount     int       `json:"player_count"`
	State           State     `json:"state"`
	LastHeartbeatAt time.Time `json:"last_heartbeat_at"`
	CreatedAt       time.Time `json:"created_at"`
}

func (h *HTTPHandler) Create(w http.ResponseWriter, r *http.Request) {
	var request createRequest
	if err := api.DecodeJSON(r, &request); err != nil {
		api.WriteError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request.", map[string]any{"body": err.Error()})
		return
	}
	result, err := h.service.Create(r.Context(), actorFromRequest(r), CreateInput{
		DisplayName: request.DisplayName,
		Region:      request.Region,
		Mode:        request.Mode,
		Version:     request.Version,
		MaxPlayers:  request.MaxPlayers,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusCreated, map[string]any{
		"room":                       toPublicResponse(result.Room),
		"host_token":                 result.HostToken,
		"heartbeat_interval_seconds": result.HeartbeatInterval,
	})
}

func (h *HTTPHandler) Get(w http.ResponseWriter, r *http.Request) {
	room, err := h.service.Get(r.Context(), chi.URLParam(r, "room_id"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusOK, toPublicResponse(room))
}

func (h *HTTPHandler) List(w http.ResponseWriter, r *http.Request) {
	limit, err := strconv.Atoi(defaultValue(r.URL.Query().Get("limit"), "50"))
	if err != nil {
		api.WriteError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Invalid limit.", nil)
		return
	}
	var hasSlots *bool
	if raw := strings.TrimSpace(r.URL.Query().Get("has_slots")); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			api.WriteError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Invalid has_slots filter.", nil)
			return
		}
		hasSlots = &value
	}
	result, err := h.service.List(r.Context(), ListFilter{
		Region:   r.URL.Query().Get("region"),
		Mode:     r.URL.Query().Get("mode"),
		Version:  r.URL.Query().Get("version"),
		HasSlots: hasSlots,
		State:    State(r.URL.Query().Get("state")),
		Cursor:   r.URL.Query().Get("cursor"),
		Limit:    limit,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	items := make([]publicRoomResponse, 0, len(result.Items))
	for _, room := range result.Items {
		items = append(items, toPublicResponse(room))
	}
	api.WriteData(w, r, http.StatusOK, map[string]any{"items": items, "next_cursor": result.NextCursor})
}

func (h *HTTPHandler) Join(w http.ResponseWriter, r *http.Request) {
	var request joinRequest
	if err := api.DecodeJSON(r, &request); err != nil {
		api.WriteError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request.", map[string]any{"body": err.Error()})
		return
	}
	room, err := h.service.Join(r.Context(), actorFromRequest(r), chi.URLParam(r, "room_id"), request.Version)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusOK, toPublicResponse(room))
}

func (h *HTTPHandler) Leave(w http.ResponseWriter, r *http.Request) {
	room, err := h.service.Leave(r.Context(), actorFromRequest(r), chi.URLParam(r, "room_id"))
	h.writeRoomOperation(w, r, room, err)
}

func (h *HTTPHandler) Heartbeat(w http.ResponseWriter, r *http.Request) {
	room, err := h.service.Heartbeat(r.Context(), actorFromRequest(r), chi.URLParam(r, "room_id"), r.Header.Get(hostTokenHeader))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusOK, map[string]any{
		"room":                   toPublicResponse(room),
		"next_heartbeat_seconds": h.service.HeartbeatInterval(),
	})
}

func (h *HTTPHandler) Start(w http.ResponseWriter, r *http.Request) {
	room, err := h.service.Start(r.Context(), actorFromRequest(r), chi.URLParam(r, "room_id"), r.Header.Get(hostTokenHeader))
	h.writeRoomOperation(w, r, room, err)
}

func (h *HTTPHandler) Delete(w http.ResponseWriter, r *http.Request) {
	room, err := h.service.Delete(r.Context(), actorFromRequest(r), chi.URLParam(r, "room_id"), r.Header.Get(hostTokenHeader))
	h.writeRoomOperation(w, r, room, err)
}

func (h *HTTPHandler) writeRoomOperation(w http.ResponseWriter, r *http.Request, room Room, err error) {
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusOK, toPublicResponse(room))
}

func (h *HTTPHandler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, message, details := errorDetails(err)
	if status >= 500 {
		h.logger.ErrorContext(r.Context(), "P2P room request failed", "code", code, "error", err)
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

func toPublicResponse(room Room) publicRoomResponse {
	return publicRoomResponse{
		RoomID: room.ID, HostPlayerID: room.HostPlayerID, DisplayName: room.DisplayName,
		Region: room.Region, Mode: room.Mode, Version: room.Version,
		MaxPlayers: room.MaxPlayers, PlayerCount: room.PlayerCount, State: room.State,
		LastHeartbeatAt: room.LastHeartbeatAt, CreatedAt: room.CreatedAt,
	}
}

func defaultValue(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
