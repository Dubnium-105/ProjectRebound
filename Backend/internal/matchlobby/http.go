package matchlobby

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/api"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/auth"
	"github.com/go-chi/chi/v5"
)

const transportHostTokenHeader = "X-Match-Transport-Host-Token"
const authoritySessionHeader = "X-Match-Authority-Session"

type HTTPService interface {
	Create(context.Context, Actor, CreateInput) (CreateResult, error)
	Get(context.Context, string, string) (Snapshot, error)
	List(context.Context, ListFilter) (ListResult, error)
	Join(context.Context, Actor, string, int, int64) (Snapshot, error)
	SelectTeam(context.Context, Actor, string, int, int64) (Snapshot, error)
	SetReady(context.Context, Actor, string, bool, int64) (Snapshot, error)
	Presence(context.Context, Actor, string, string, bool) (Snapshot, error)
	Leave(context.Context, Actor, string, string, int64) (Snapshot, error)
	Start(context.Context, Actor, string, int64) (Snapshot, error)
	P2PPayloadInstalled(context.Context, Actor, string, string, string, string, int) (Snapshot, error)
	P2PAuthorityReady(context.Context, Actor, string, string, string, string, int, int) (Snapshot, error)
	P2PHostAllocation(context.Context, Actor, string) (AllocationResult, error)
	DedicatedAllocation(context.Context, string, string) (AllocationResult, error)
	DedicatedPayloadInstalled(context.Context, string, string, string, string, string, int) (Snapshot, error)
	DedicatedAuthorityReady(context.Context, string, string, string) (Snapshot, error)
	JoinGrant(context.Context, Actor, string) (GrantResult, error)
	MarkConnected(context.Context, string, string, string, string, string, int) (Snapshot, error)
	P2PMarkConnected(context.Context, Actor, string, string, string, string, int) (Snapshot, error)
	MarkDisconnected(context.Context, string, string, string, string, int) (Snapshot, error)
	P2PMarkDisconnected(context.Context, Actor, string, string, string, int) (Snapshot, error)
	AuthorityHeartbeat(context.Context, string, string, string) error
	P2PAuthorityHeartbeat(context.Context, Actor, string, string) error
	Complete(context.Context, string, string, string, bool, string) (Snapshot, error)
	P2PComplete(context.Context, Actor, string, string, bool, string) (Snapshot, error)
}

type HTTPHandler struct {
	service HTTPService
	logger  *slog.Logger
}

func NewHTTPHandler(service HTTPService, logger *slog.Logger) *HTTPHandler {
	return &HTTPHandler{service: service, logger: logger}
}

type createRequest struct {
	DisplayName     string        `json:"display_name"`
	HostingKind     HostingKind   `json:"hosting_kind"`
	TransportKind   TransportKind `json:"transport_kind,omitempty"`
	Mode            string        `json:"mode"`
	Region          string        `json:"region"`
	ClientVersion   string        `json:"client_version"`
	ProtocolVersion int           `json:"protocol_version"`
	TeamCapacities  struct {
		TeamOne int `json:"team_1"`
		TeamTwo int `json:"team_2"`
	} `json:"team_capacities"`
	TeamID    int    `json:"team_id"`
	VNTNodeID string `json:"vnt_node_id,omitempty"`
}

type revisionRequest struct {
	ExpectedRevision int64 `json:"expected_revision"`
}

func (h *HTTPHandler) Create(w http.ResponseWriter, r *http.Request) {
	var request createRequest
	if err := api.DecodeJSON(r, &request); err != nil {
		api.WriteError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request.", map[string]any{"body": err.Error()})
		return
	}
	result, err := h.service.Create(r.Context(), actorFromRequest(r), CreateInput{
		DisplayName: request.DisplayName, HostingKind: request.HostingKind,
		TransportKind: request.TransportKind, Mode: request.Mode, Region: request.Region,
		ClientVersion: request.ClientVersion, ProtocolVersion: request.ProtocolVersion,
		TeamOneCapacity: request.TeamCapacities.TeamOne, TeamTwoCapacity: request.TeamCapacities.TeamTwo,
		TeamID: request.TeamID, VNTNodeID: request.VNTNodeID,
		IdempotencyKey: r.Header.Get("Idempotency-Key"),
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	payload := map[string]any{"lobby": result.Snapshot}
	if result.TransportHostToken != "" {
		payload["transport_host_token"] = result.TransportHostToken
	}
	api.WriteData(w, r, http.StatusCreated, payload)
}

func (h *HTTPHandler) Get(w http.ResponseWriter, r *http.Request) {
	snapshot, err := h.service.Get(r.Context(), chi.URLParam(r, "lobby_id"), actorFromRequest(r).PlayerID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusOK, snapshot)
}

func (h *HTTPHandler) List(w http.ResponseWriter, r *http.Request) {
	limit, err := strconv.Atoi(defaultValue(r.URL.Query().Get("limit"), "50"))
	if err != nil {
		api.WriteError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Invalid limit.", nil)
		return
	}
	result, err := h.service.List(r.Context(), ListFilter{
		HostingKind: HostingKind(r.URL.Query().Get("hosting_kind")), Region: r.URL.Query().Get("region"),
		Mode: r.URL.Query().Get("mode"), ClientVersion: r.URL.Query().Get("client_version"),
		Cursor: r.URL.Query().Get("cursor"), Limit: limit,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusOK, result)
}

func (h *HTTPHandler) Join(w http.ResponseWriter, r *http.Request) {
	var request struct {
		TeamID           int   `json:"team_id"`
		ExpectedRevision int64 `json:"expected_revision"`
	}
	if !h.decode(w, r, &request) {
		return
	}
	snapshot, err := h.service.Join(r.Context(), actorFromRequest(r), chi.URLParam(r, "lobby_id"), request.TeamID, request.ExpectedRevision)
	h.writeLobbySnapshot(w, r, chi.URLParam(r, "lobby_id"), snapshot, err)
}

func (h *HTTPHandler) SelectTeam(w http.ResponseWriter, r *http.Request) {
	var request struct {
		TeamID           int   `json:"team_id"`
		ExpectedRevision int64 `json:"expected_revision"`
	}
	if !h.decode(w, r, &request) {
		return
	}
	snapshot, err := h.service.SelectTeam(r.Context(), actorFromRequest(r), chi.URLParam(r, "lobby_id"), request.TeamID, request.ExpectedRevision)
	h.writeLobbySnapshot(w, r, chi.URLParam(r, "lobby_id"), snapshot, err)
}

func (h *HTTPHandler) SetReady(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Ready            bool  `json:"ready"`
		ExpectedRevision int64 `json:"expected_revision"`
	}
	if !h.decode(w, r, &request) {
		return
	}
	snapshot, err := h.service.SetReady(r.Context(), actorFromRequest(r), chi.URLParam(r, "lobby_id"), request.Ready, request.ExpectedRevision)
	h.writeLobbySnapshot(w, r, chi.URLParam(r, "lobby_id"), snapshot, err)
}

func (h *HTTPHandler) Presence(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Online bool `json:"online"`
	}
	if !h.decode(w, r, &request) {
		return
	}
	snapshot, err := h.service.Presence(r.Context(), actorFromRequest(r), chi.URLParam(r, "lobby_id"), r.Header.Get(transportHostTokenHeader), request.Online)
	h.writeLobbySnapshot(w, r, chi.URLParam(r, "lobby_id"), snapshot, err)
}

func (h *HTTPHandler) Leave(w http.ResponseWriter, r *http.Request) {
	var request revisionRequest
	if !h.decode(w, r, &request) {
		return
	}
	snapshot, err := h.service.Leave(r.Context(), actorFromRequest(r), chi.URLParam(r, "lobby_id"), r.Header.Get(transportHostTokenHeader), request.ExpectedRevision)
	h.writeLobbySnapshot(w, r, chi.URLParam(r, "lobby_id"), snapshot, err)
}

func (h *HTTPHandler) Start(w http.ResponseWriter, r *http.Request) {
	var request revisionRequest
	if !h.decode(w, r, &request) {
		return
	}
	snapshot, err := h.service.Start(r.Context(), actorFromRequest(r), chi.URLParam(r, "lobby_id"), request.ExpectedRevision)
	h.writeLobbySnapshot(w, r, chi.URLParam(r, "lobby_id"), snapshot, err)
}

func (h *HTTPHandler) JoinGrant(w http.ResponseWriter, r *http.Request) {
	result, err := h.service.JoinGrant(r.Context(), actorFromRequest(r), chi.URLParam(r, "attempt_id"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	api.WriteData(w, r, http.StatusCreated, result)
}

func (h *HTTPHandler) P2PHostAllocation(w http.ResponseWriter, r *http.Request) {
	result, err := h.service.P2PHostAllocation(r.Context(), actorFromRequest(r), chi.URLParam(r, "attempt_id"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	api.WriteData(w, r, http.StatusOK, result)
}

func (h *HTTPHandler) P2PAuthorityReady(w http.ResponseWriter, r *http.Request) {
	var request struct {
		EndpointHost    string `json:"endpoint_host"`
		EndpointPort    int    `json:"endpoint_port"`
		RouteGeneration int    `json:"route_generation"`
	}
	if !h.decode(w, r, &request) {
		return
	}
	snapshot, err := h.service.P2PAuthorityReady(
		r.Context(), actorFromRequest(r), chi.URLParam(r, "attempt_id"),
		r.Header.Get(authoritySessionHeader), r.Header.Get(transportHostTokenHeader), request.EndpointHost,
		request.EndpointPort, request.RouteGeneration,
	)
	h.writeSnapshot(w, r, snapshot, err)
}

func (h *HTTPHandler) P2PPayloadInstalled(w http.ResponseWriter, r *http.Request) {
	var request struct {
		PayloadVersion   string `json:"payload_version"`
		GameBinarySHA256 string `json:"game_binary_sha256"`
		RouteGeneration  int    `json:"route_generation"`
	}
	if !h.decode(w, r, &request) {
		return
	}
	snapshot, err := h.service.P2PPayloadInstalled(
		r.Context(), actorFromRequest(r), chi.URLParam(r, "attempt_id"),
		r.Header.Get(authoritySessionHeader), request.PayloadVersion, request.GameBinarySHA256,
		request.RouteGeneration,
	)
	h.writeSnapshot(w, r, snapshot, err)
}

func (h *HTTPHandler) P2PConnected(w http.ResponseWriter, r *http.Request) {
	var request struct {
		PlayerID             string `json:"player_id"`
		GrantJTI             string `json:"grant_jti"`
		ConnectionGeneration int    `json:"connection_generation"`
	}
	if !h.decode(w, r, &request) {
		return
	}
	snapshot, err := h.service.P2PMarkConnected(
		r.Context(), actorFromRequest(r), r.Header.Get(authoritySessionHeader), chi.URLParam(r, "attempt_id"),
		request.PlayerID, request.GrantJTI, request.ConnectionGeneration,
	)
	h.writeSnapshot(w, r, snapshot, err)
}

func (h *HTTPHandler) P2PDisconnected(w http.ResponseWriter, r *http.Request) {
	var request struct {
		PlayerID             string `json:"player_id"`
		ConnectionGeneration int    `json:"connection_generation"`
	}
	if !h.decode(w, r, &request) {
		return
	}
	snapshot, err := h.service.P2PMarkDisconnected(
		r.Context(), actorFromRequest(r), r.Header.Get(authoritySessionHeader), chi.URLParam(r, "attempt_id"),
		request.PlayerID, request.ConnectionGeneration,
	)
	h.writeSnapshot(w, r, snapshot, err)
}

func (h *HTTPHandler) P2PAuthorityHeartbeat(w http.ResponseWriter, r *http.Request) {
	if err := h.service.P2PAuthorityHeartbeat(r.Context(), actorFromRequest(r), r.Header.Get(authoritySessionHeader), chi.URLParam(r, "attempt_id")); err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusOK, map[string]bool{"accepted": true})
}

func (h *HTTPHandler) P2PComplete(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Success     bool   `json:"success"`
		FailureCode string `json:"failure_code,omitempty"`
	}
	if !h.decode(w, r, &request) {
		return
	}
	snapshot, err := h.service.P2PComplete(
		r.Context(), actorFromRequest(r), r.Header.Get(authoritySessionHeader), chi.URLParam(r, "attempt_id"),
		request.Success, request.FailureCode,
	)
	h.writeSnapshot(w, r, snapshot, err)
}

func (h *HTTPHandler) DedicatedPayloadInstalled(w http.ResponseWriter, r *http.Request) {
	var request struct {
		PayloadVersion   string `json:"payload_version"`
		GameBinarySHA256 string `json:"game_binary_sha256"`
		RouteGeneration  int    `json:"route_generation"`
	}
	if !h.decode(w, r, &request) {
		return
	}
	snapshot, err := h.service.DedicatedPayloadInstalled(
		r.Context(), chi.URLParam(r, "server_id"), chi.URLParam(r, "attempt_id"),
		r.Header.Get(authoritySessionHeader), request.PayloadVersion, request.GameBinarySHA256,
		request.RouteGeneration,
	)
	h.writeSnapshot(w, r, snapshot, err)
}

func (h *HTTPHandler) DedicatedAllocation(w http.ResponseWriter, r *http.Request) {
	result, err := h.service.DedicatedAllocation(r.Context(), chi.URLParam(r, "server_id"), chi.URLParam(r, "attempt_id"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	api.WriteData(w, r, http.StatusOK, result)
}

func (h *HTTPHandler) DedicatedAuthorityReady(w http.ResponseWriter, r *http.Request) {
	snapshot, err := h.service.DedicatedAuthorityReady(r.Context(), chi.URLParam(r, "server_id"), chi.URLParam(r, "attempt_id"), r.Header.Get(authoritySessionHeader))
	h.writeSnapshot(w, r, snapshot, err)
}

func (h *HTTPHandler) Connected(w http.ResponseWriter, r *http.Request) {
	var request struct {
		PlayerID             string `json:"player_id"`
		GrantJTI             string `json:"grant_jti"`
		ConnectionGeneration int    `json:"connection_generation"`
	}
	if !h.decode(w, r, &request) {
		return
	}
	snapshot, err := h.service.MarkConnected(r.Context(), chi.URLParam(r, "server_id"), r.Header.Get(authoritySessionHeader), chi.URLParam(r, "attempt_id"), request.PlayerID, request.GrantJTI, request.ConnectionGeneration)
	h.writeSnapshot(w, r, snapshot, err)
}

func (h *HTTPHandler) Disconnected(w http.ResponseWriter, r *http.Request) {
	var request struct {
		PlayerID             string `json:"player_id"`
		ConnectionGeneration int    `json:"connection_generation"`
	}
	if !h.decode(w, r, &request) {
		return
	}
	snapshot, err := h.service.MarkDisconnected(r.Context(), chi.URLParam(r, "server_id"), r.Header.Get(authoritySessionHeader), chi.URLParam(r, "attempt_id"), request.PlayerID, request.ConnectionGeneration)
	h.writeSnapshot(w, r, snapshot, err)
}

func (h *HTTPHandler) AuthorityHeartbeat(w http.ResponseWriter, r *http.Request) {
	if err := h.service.AuthorityHeartbeat(r.Context(), chi.URLParam(r, "server_id"), r.Header.Get(authoritySessionHeader), chi.URLParam(r, "attempt_id")); err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusOK, map[string]bool{"accepted": true})
}

func (h *HTTPHandler) Complete(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Success     bool   `json:"success"`
		FailureCode string `json:"failure_code,omitempty"`
	}
	if !h.decode(w, r, &request) {
		return
	}
	snapshot, err := h.service.Complete(r.Context(), chi.URLParam(r, "server_id"), r.Header.Get(authoritySessionHeader), chi.URLParam(r, "attempt_id"), request.Success, request.FailureCode)
	h.writeSnapshot(w, r, snapshot, err)
}

func (h *HTTPHandler) decode(w http.ResponseWriter, r *http.Request, target any) bool {
	if err := api.DecodeJSON(r, target); err != nil {
		api.WriteError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request.", map[string]any{"body": err.Error()})
		return false
	}
	return true
}

func (h *HTTPHandler) writeSnapshot(w http.ResponseWriter, r *http.Request, snapshot Snapshot, err error) {
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusOK, snapshot)
}

func (h *HTTPHandler) writeLobbySnapshot(w http.ResponseWriter, r *http.Request, lobbyID string, snapshot Snapshot, err error) {
	if err == nil {
		api.WriteData(w, r, http.StatusOK, snapshot)
		return
	}
	status, code, message, details := errorDetails(err)
	if status == http.StatusConflict {
		latest, snapshotErr := h.service.Get(r.Context(), lobbyID, actorFromRequest(r).PlayerID)
		if snapshotErr == nil {
			if details == nil {
				details = map[string]any{}
			}
			details["lobby"] = latest
		}
	}
	if status >= 500 {
		h.logger.ErrorContext(r.Context(), "match lobby request failed", "code", code, "error", err)
	}
	api.WriteError(w, r, status, code, message, details)
}

func (h *HTTPHandler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, message, details := errorDetails(err)
	if status >= 500 {
		h.logger.ErrorContext(r.Context(), "match lobby request failed", "code", code, "error", err)
	}
	api.WriteError(w, r, status, code, message, details)
}

func actorFromRequest(r *http.Request) Actor {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		return Actor{}
	}
	return Actor{
		PlayerID: principal.Player.ID, AccountStatus: principal.Player.AccountStatus,
		AuthLevel: principal.AuthLevel, SteamVerified: principal.SteamVerified,
	}
}

func defaultValue(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
