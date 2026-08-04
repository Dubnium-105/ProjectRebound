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

type VNTHTTPService interface {
	VNTBootstrap(context.Context, Actor, string) (VNTBootstrap, error)
	UpdateVNTPresence(context.Context, Actor, string, VNTPresenceInput) (Room, error)
	VNTHostReady(context.Context, Actor, string, string, int, string) (Room, error)
	VNTRebind(context.Context, Actor, string, string, string) (Room, error)
}

type HTTPHandler struct {
	service HTTPService
	logger  *slog.Logger
}

func NewHTTPHandler(service HTTPService, logger *slog.Logger) *HTTPHandler {
	return &HTTPHandler{service: service, logger: logger}
}

type createRequest struct {
	DisplayName   string        `json:"display_name"`
	Region        string        `json:"region"`
	Mode          string        `json:"mode"`
	Version       string        `json:"version"`
	MaxPlayers    int           `json:"max_players"`
	TransportKind TransportKind `json:"transport_kind,omitempty"`
	VNTNodeID     string        `json:"vnt_node_id,omitempty"`
}

type joinRequest struct {
	Version string `json:"version"`
}

type publicRoomResponse struct {
	RoomID          string        `json:"room_id"`
	HostPlayerID    string        `json:"host_player_id"`
	DisplayName     string        `json:"display_name"`
	Region          string        `json:"region"`
	Mode            string        `json:"mode"`
	Version         string        `json:"version"`
	MaxPlayers      int           `json:"max_players"`
	PlayerCount     int           `json:"player_count"`
	State           State         `json:"state"`
	LastHeartbeatAt time.Time     `json:"last_heartbeat_at"`
	CreatedAt       time.Time     `json:"created_at"`
	TransportKind   TransportKind `json:"transport_kind"`
	VNTNodeID       string        `json:"vnt_node_id,omitempty"`
	VNTHost         string        `json:"vnt_host,omitempty"`
	VNTPort         int           `json:"vnt_port,omitempty"`
	VNTRegion       string        `json:"vnt_region,omitempty"`
	VNTLocation     string        `json:"vnt_location,omitempty"`
	VNTState        string        `json:"vnt_state,omitempty"`
	Generation      int           `json:"generation,omitempty"`
	ExpiresAt       time.Time     `json:"expires_at"`
}

func (h *HTTPHandler) Create(w http.ResponseWriter, r *http.Request) {
	var request createRequest
	if err := api.DecodeJSON(r, &request); err != nil {
		api.WriteError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request.", map[string]any{"body": err.Error()})
		return
	}
	result, err := h.service.Create(r.Context(), actorFromRequest(r), CreateInput{
		DisplayName:    request.DisplayName,
		Region:         request.Region,
		Mode:           request.Mode,
		Version:        request.Version,
		MaxPlayers:     request.MaxPlayers,
		TransportKind:  request.TransportKind,
		VNTNodeID:      request.VNTNodeID,
		IdempotencyKey: r.Header.Get("Idempotency-Key"),
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
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

func (h *HTTPHandler) VNTBootstrap(w http.ResponseWriter, r *http.Request) {
	service, ok := h.service.(VNTHTTPService)
	if !ok {
		h.writeError(w, r, conflict("VNT_FEATURE_DISABLED", "VNT rooms are not available."))
		return
	}
	result, err := service.VNTBootstrap(r.Context(), actorFromRequest(r), chi.URLParam(r, "room_id"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	api.WriteData(w, r, http.StatusOK, result)
}

func (h *HTTPHandler) UpdateVNTPresence(w http.ResponseWriter, r *http.Request) {
	service, ok := h.service.(VNTHTTPService)
	if !ok {
		h.writeError(w, r, conflict("VNT_FEATURE_DISABLED", "VNT rooms are not available."))
		return
	}
	var request struct {
		Generation   int    `json:"generation"`
		State        string `json:"state"`
		VirtualIP    string `json:"virtual_ip"`
		ObservedPath string `json:"observed_path,omitempty"`
		ReasonCode   string `json:"reason_code,omitempty"`
	}
	if err := api.DecodeJSON(r, &request); err != nil {
		api.WriteError(w, r, 400, "INVALID_REQUEST", "Invalid request.", map[string]any{"body": err.Error()})
		return
	}
	room, err := service.UpdateVNTPresence(r.Context(), actorFromRequest(r), chi.URLParam(r, "room_id"), VNTPresenceInput{
		Generation: request.Generation, State: request.State, VirtualIP: request.VirtualIP,
		ObservedPath: request.ObservedPath, ReasonCode: request.ReasonCode,
	})
	h.writeRoomOperation(w, r, room, err)
}

func (h *HTTPHandler) VNTHostReady(w http.ResponseWriter, r *http.Request) {
	service, ok := h.service.(VNTHTTPService)
	if !ok {
		h.writeError(w, r, conflict("VNT_FEATURE_DISABLED", "VNT rooms are not available."))
		return
	}
	var request struct {
		Generation int    `json:"generation"`
		VirtualIP  string `json:"virtual_ip"`
	}
	if err := api.DecodeJSON(r, &request); err != nil {
		api.WriteError(w, r, 400, "INVALID_REQUEST", "Invalid request.", map[string]any{"body": err.Error()})
		return
	}
	room, err := service.VNTHostReady(r.Context(), actorFromRequest(r), chi.URLParam(r, "room_id"), r.Header.Get(hostTokenHeader), request.Generation, request.VirtualIP)
	h.writeRoomOperation(w, r, room, err)
}

func (h *HTTPHandler) VNTRebind(w http.ResponseWriter, r *http.Request) {
	service, ok := h.service.(VNTHTTPService)
	if !ok {
		h.writeError(w, r, conflict("VNT_FEATURE_DISABLED", "VNT rooms are not available."))
		return
	}
	var request struct {
		VNTNodeID string `json:"vnt_node_id"`
	}
	if err := api.DecodeJSON(r, &request); err != nil {
		api.WriteError(w, r, 400, "INVALID_REQUEST", "Invalid request.", map[string]any{"body": err.Error()})
		return
	}
	room, err := service.VNTRebind(r.Context(), actorFromRequest(r), chi.URLParam(r, "room_id"), r.Header.Get(hostTokenHeader), request.VNTNodeID)
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
		TransportKind: room.TransportKind, VNTNodeID: room.VNTNodeID,
		VNTHost: room.VNTHost, VNTPort: room.VNTPort, VNTRegion: room.VNTRegion,
		VNTLocation: room.VNTLocation, VNTState: room.VNTState,
		Generation: room.VNTGeneration, ExpiresAt: room.ExpiresAt,
	}
}

func defaultValue(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
