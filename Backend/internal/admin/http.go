package admin

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/projectrebound/matchserver/internal/api"
	appmiddleware "github.com/projectrebound/matchserver/internal/middleware"
	"github.com/projectrebound/matchserver/internal/player"
	"github.com/projectrebound/matchserver/internal/requestctx"
)

type HTTPService interface {
	ListPlayers(context.Context, string, string, int) (ListResult, error)
	GetPlayer(context.Context, string) (player.Player, error)
	PatchPlayer(context.Context, string, PlayerPatch, RequestMeta) (PatchResult, error)
	RevokePlayerSessions(context.Context, string, string, RequestMeta) (int64, error)
}

type HTTPHandler struct {
	service    HTTPService
	logger     *slog.Logger
	trustProxy bool
}

func NewHTTPHandler(service HTTPService, logger *slog.Logger, trustProxy bool) *HTTPHandler {
	return &HTTPHandler{service: service, logger: logger, trustProxy: trustProxy}
}

type patchPlayerRequest struct {
	AccountStatus  *player.AccountStatus `json:"account_status"`
	IsVIP          *bool                 `json:"is_vip"`
	RevokeSessions bool                  `json:"revoke_sessions"`
	Reason         string                `json:"reason"`
	InternalNote   string                `json:"internal_note"`
}

type reasonRequest struct {
	Reason string `json:"reason"`
}

type playerResponse struct {
	PlayerID      string               `json:"player_id"`
	SteamID       string               `json:"steam_id"`
	PersonaName   string               `json:"persona_name"`
	AccountStatus player.AccountStatus `json:"account_status"`
	IsVIP         bool                 `json:"is_vip"`
	AuthProvider  string               `json:"auth_provider"`
	AuthLevel     string               `json:"auth_level"`
	LastLoginAt   time.Time            `json:"last_login_at"`
	CreatedAt     time.Time            `json:"created_at"`
	UpdatedAt     time.Time            `json:"updated_at"`
}

func (h *HTTPHandler) ListPlayers(w http.ResponseWriter, r *http.Request) {
	limit, err := strconv.Atoi(defaultString(r.URL.Query().Get("limit"), "50"))
	if err != nil {
		api.WriteError(w, r, 400, "INVALID_REQUEST", "Invalid limit.", nil)
		return
	}
	result, err := h.service.ListPlayers(r.Context(), r.URL.Query().Get("cursor"), r.URL.Query().Get("account_status"), limit)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	items := make([]playerResponse, 0, len(result.Items))
	for _, item := range result.Items {
		items = append(items, toPlayerResponse(item))
	}
	api.WriteData(w, r, 200, map[string]any{"items": items, "next_cursor": result.NextCursor})
}

func (h *HTTPHandler) GetPlayer(w http.ResponseWriter, r *http.Request) {
	item, err := h.service.GetPlayer(r.Context(), chi.URLParam(r, "player_id"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, 200, toPlayerResponse(item))
}

func (h *HTTPHandler) PatchPlayer(w http.ResponseWriter, r *http.Request) {
	var request patchPlayerRequest
	if err := api.DecodeJSON(r, &request); err != nil {
		api.WriteError(w, r, 400, "INVALID_REQUEST", "Invalid request.", map[string]any{"body": err.Error()})
		return
	}
	principal := PrincipalFromContext(r.Context())
	if request.AccountStatus != nil && (principal == nil || !principal.HasPermission("players.update_status")) {
		api.WriteError(w, r, http.StatusForbidden, "ADMIN_FORBIDDEN", "Administrator permission is required.", nil)
		return
	}
	if request.IsVIP != nil && (principal == nil || !principal.HasPermission("players.update_vip")) {
		api.WriteError(w, r, http.StatusForbidden, "ADMIN_FORBIDDEN", "Administrator permission is required.", nil)
		return
	}
	if request.RevokeSessions && (principal == nil || !principal.HasPermission("players.revoke_sessions")) {
		api.WriteError(w, r, http.StatusForbidden, "ADMIN_FORBIDDEN", "Administrator permission is required.", nil)
		return
	}
	result, err := h.service.PatchPlayer(r.Context(), chi.URLParam(r, "player_id"), PlayerPatch{
		AccountStatus:  request.AccountStatus,
		IsVIP:          request.IsVIP,
		RevokeSessions: request.RevokeSessions,
		Reason:         request.Reason,
		InternalNote:   request.InternalNote,
	}, h.requestMeta(r))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, 200, map[string]any{
		"player":           toPlayerResponse(result.Player),
		"revoked_sessions": result.RevokedSessions,
	})
}

func (h *HTTPHandler) RevokeSessions(w http.ResponseWriter, r *http.Request) {
	principal := PrincipalFromContext(r.Context())
	if principal == nil || !principal.HasPermission("players.revoke_sessions") {
		api.WriteError(w, r, http.StatusForbidden, "ADMIN_FORBIDDEN", "Administrator permission is required.", nil)
		return
	}
	var request reasonRequest
	if err := api.DecodeJSON(r, &request); err != nil {
		api.WriteError(w, r, 400, "INVALID_REQUEST", "Invalid request.", map[string]any{"body": err.Error()})
		return
	}
	revoked, err := h.service.RevokePlayerSessions(r.Context(), chi.URLParam(r, "player_id"), request.Reason, h.requestMeta(r))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, 200, map[string]int64{"revoked_sessions": revoked})
}

func (h *HTTPHandler) requestMeta(r *http.Request) RequestMeta {
	principal := PrincipalFromContext(r.Context())
	adminID := ""
	if principal != nil {
		adminID = principal.AdminID
	}
	return RequestMeta{
		AdminID:   adminID,
		RequestID: requestctx.RequestID(r.Context()),
		IPAddress: appmiddleware.ClientIP(r, h.trustProxy),
		UserAgent: r.UserAgent(),
	}
}

func (h *HTTPHandler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, message, details := errorDetails(err)
	if status >= 500 {
		h.logger.ErrorContext(r.Context(), "admin request failed", "code", code, "error", err)
	}
	api.WriteError(w, r, status, code, message, details)
}

func toPlayerResponse(item player.Player) playerResponse {
	return playerResponse{
		PlayerID:      item.ID,
		SteamID:       item.SteamID,
		PersonaName:   item.PersonaName,
		AccountStatus: item.AccountStatus,
		IsVIP:         item.IsVIP,
		AuthProvider:  item.AuthProvider,
		AuthLevel:     item.AuthLevel,
		LastLoginAt:   item.LastLoginAt,
		CreatedAt:     item.CreatedAt,
		UpdatedAt:     item.UpdatedAt,
	}
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
