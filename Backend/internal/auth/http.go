package auth

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/api"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/middleware"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/requestctx"
	"github.com/go-chi/chi/v5"
)

type HTTPService interface {
	Bind(context.Context, BindInput, RequestMeta) (BindResult, error)
	Refresh(context.Context, string, RequestMeta) (RefreshResult, error)
	Logout(context.Context, string) error
	ListUserSessions(context.Context, string, string) ([]UserSession, error)
	RevokeUserSession(context.Context, string, string) error
	RevokeOtherUserSessions(context.Context, string, string) (int64, error)
	ListRiskEvents(context.Context, string, string, string, string, bool, int) (RiskEventList, error)
	AuditBindDecodeFailure(context.Context, RequestMeta)
}

type HTTPHandler struct {
	service          HTTPService
	logger           *slog.Logger
	trustProxyHeader bool
}

func NewHTTPHandler(service HTTPService, logger *slog.Logger, trustProxyHeader bool) *HTTPHandler {
	return &HTTPHandler{service: service, logger: logger, trustProxyHeader: trustProxyHeader}
}

type bindRequest struct {
	SteamID         string `json:"steam_id"`
	PersonaName     string `json:"persona_name"`
	DeviceID        string `json:"device_id,omitempty"`
	InviteCode      string `json:"invite_code,omitempty"`
	EncryptedTicket string `json:"encrypted_ticket,omitempty"`
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

type profileResponse struct {
	SteamID     string `json:"steam_id"`
	PersonaName string `json:"persona_name"`
}

type sessionResponse struct {
	AccessToken           string    `json:"access_token"`
	AccessTokenExpiresAt  time.Time `json:"access_token_expires_at"`
	RefreshToken          string    `json:"refresh_token"`
	RefreshTokenExpiresAt time.Time `json:"refresh_token_expires_at"`
}

type bindResponse struct {
	PlayerID           string             `json:"player_id"`
	AccountStatus      string             `json:"account_status"`
	IsVIP              bool               `json:"is_vip"`
	Profile            profileResponse    `json:"profile"`
	Session            sessionResponse    `json:"session"`
	IsNewPlayer        bool               `json:"is_new_player"`
	AuthLevel          string             `json:"auth_level"`
	SteamVerified      bool               `json:"steam_verified"`
	IntegrityChallenge IntegrityChallenge `json:"integrity_challenge"`
}

type refreshResponse struct {
	Session sessionResponse `json:"session"`
}

type meResponse struct {
	PlayerID      string    `json:"player_id"`
	SteamID       string    `json:"steam_id"`
	PersonaName   string    `json:"persona_name"`
	AccountStatus string    `json:"account_status"`
	IsVIP         bool      `json:"is_vip"`
	LastLoginAt   time.Time `json:"last_login_at"`
	CreatedAt     time.Time `json:"created_at"`
}

type userSessionResponse struct {
	SessionID      string     `json:"session_id"`
	DeviceIDSuffix string     `json:"device_id_suffix"`
	IPAddress      string     `json:"ip_address"`
	CreatedAt      time.Time  `json:"created_at"`
	LastUsedAt     *time.Time `json:"last_used_at"`
	IsCurrent      bool       `json:"is_current"`
}

type riskEventResponse struct {
	ID         string         `json:"id"`
	PlayerID   string         `json:"player_id,omitempty"`
	SteamID    string         `json:"steam_id,omitempty"`
	IPAddress  string         `json:"ip_address,omitempty"`
	EventType  string         `json:"event_type"`
	Severity   string         `json:"severity"`
	Details    map[string]any `json:"details"`
	CreatedAt  time.Time      `json:"created_at"`
	ResolvedAt *time.Time     `json:"resolved_at"`
}

func (h *HTTPHandler) Bind(w http.ResponseWriter, r *http.Request) {
	var request bindRequest
	if err := api.DecodeJSON(r, &request); err != nil {
		h.service.AuditBindDecodeFailure(r.Context(), h.requestMeta(r))
		api.WriteError(w, r, http.StatusBadRequest, CodeInvalidRequest, "Invalid request.", map[string]any{"body": err.Error()})
		return
	}
	result, err := h.service.Bind(r.Context(), BindInput{
		SteamID: request.SteamID, PersonaName: request.PersonaName,
		DeviceID: request.DeviceID, InviteCode: request.InviteCode,
		EncryptedTicket: request.EncryptedTicket,
	}, h.requestMeta(r))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusOK, bindResponse{
		PlayerID:      result.Player.ID,
		AccountStatus: string(result.Player.AccountStatus),
		IsVIP:         result.Player.IsVIP,
		Profile: profileResponse{
			SteamID:     result.Player.SteamID,
			PersonaName: result.Player.PersonaName,
		},
		Session:            sessionToResponse(result.Tokens),
		IsNewPlayer:        result.IsNewPlayer,
		AuthLevel:          result.AuthLevel,
		SteamVerified:      result.SteamVerified,
		IntegrityChallenge: result.IntegrityChallenge,
	})
}

func (h *HTTPHandler) Refresh(w http.ResponseWriter, r *http.Request) {
	var request refreshRequest
	if err := api.DecodeJSON(r, &request); err != nil {
		api.WriteError(w, r, http.StatusBadRequest, CodeInvalidRequest, "Invalid request.", map[string]any{"body": err.Error()})
		return
	}
	result, err := h.service.Refresh(r.Context(), strings.TrimSpace(request.RefreshToken), h.requestMeta(r))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusOK, refreshResponse{Session: sessionToResponse(result.Tokens)})
}

func (h *HTTPHandler) Logout(w http.ResponseWriter, r *http.Request) {
	principal := PrincipalFromContext(r.Context())
	if principal == nil {
		api.WriteError(w, r, http.StatusUnauthorized, CodeUnauthorized, "Authentication is required.", nil)
		return
	}
	if err := h.service.Logout(r.Context(), principal.SessionID); err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusOK, map[string]bool{"logged_out": true})
}

func (h *HTTPHandler) Me(w http.ResponseWriter, r *http.Request) {
	principal := PrincipalFromContext(r.Context())
	if principal == nil {
		api.WriteError(w, r, http.StatusUnauthorized, CodeUnauthorized, "Authentication is required.", nil)
		return
	}
	item := principal.Player
	api.WriteData(w, r, http.StatusOK, meResponse{
		PlayerID:      item.ID,
		SteamID:       item.SteamID,
		PersonaName:   item.PersonaName,
		AccountStatus: string(item.AccountStatus),
		IsVIP:         item.IsVIP,
		LastLoginAt:   item.LastLoginAt,
		CreatedAt:     item.CreatedAt,
	})
}

func (h *HTTPHandler) ListSessions(w http.ResponseWriter, r *http.Request) {
	principal := PrincipalFromContext(r.Context())
	if principal == nil {
		api.WriteError(w, r, http.StatusUnauthorized, CodeUnauthorized, "Authentication is required.", nil)
		return
	}
	items, err := h.service.ListUserSessions(r.Context(), principal.Player.ID, principal.SessionID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	responses := make([]userSessionResponse, 0, len(items))
	for _, item := range items {
		responses = append(responses, userSessionResponse{
			SessionID: item.ID, DeviceIDSuffix: item.DeviceIDSuffix, IPAddress: item.IPAddress,
			CreatedAt: item.CreatedAt, LastUsedAt: item.LastUsedAt, IsCurrent: item.IsCurrent,
		})
	}
	api.WriteData(w, r, http.StatusOK, map[string]any{"items": responses})
}

func (h *HTTPHandler) RevokeSession(w http.ResponseWriter, r *http.Request) {
	principal := PrincipalFromContext(r.Context())
	if principal == nil {
		api.WriteError(w, r, http.StatusUnauthorized, CodeUnauthorized, "Authentication is required.", nil)
		return
	}
	if err := h.service.RevokeUserSession(r.Context(), principal.Player.ID, chi.URLParam(r, "session_id")); err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusOK, map[string]bool{"revoked": true})
}

func (h *HTTPHandler) RevokeOtherSessions(w http.ResponseWriter, r *http.Request) {
	principal := PrincipalFromContext(r.Context())
	if principal == nil {
		api.WriteError(w, r, http.StatusUnauthorized, CodeUnauthorized, "Authentication is required.", nil)
		return
	}
	revoked, err := h.service.RevokeOtherUserSessions(r.Context(), principal.Player.ID, principal.SessionID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusOK, map[string]int64{"revoked_sessions": revoked})
}

func (h *HTTPHandler) ListRiskEvents(w http.ResponseWriter, r *http.Request) {
	limit, err := strconv.Atoi(defaultQuery(r.URL.Query().Get("limit"), "50"))
	if err != nil {
		api.WriteError(w, r, http.StatusBadRequest, CodeInvalidRequest, "Invalid limit.", nil)
		return
	}
	unresolvedOnly, err := strconv.ParseBool(defaultQuery(r.URL.Query().Get("unresolved_only"), "false"))
	if err != nil {
		api.WriteError(w, r, http.StatusBadRequest, CodeInvalidRequest, "Invalid unresolved_only value.", nil)
		return
	}
	result, err := h.service.ListRiskEvents(
		r.Context(), r.URL.Query().Get("cursor"), r.URL.Query().Get("player_id"),
		r.URL.Query().Get("event_type"), r.URL.Query().Get("severity"), unresolvedOnly, limit,
	)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	items := make([]riskEventResponse, 0, len(result.Items))
	for _, item := range result.Items {
		items = append(items, riskEventResponse{
			ID: item.ID, PlayerID: item.PlayerID, SteamID: item.SteamID, IPAddress: item.IPAddress,
			EventType: item.EventType, Severity: item.Severity, Details: item.Details,
			CreatedAt: item.CreatedAt, ResolvedAt: item.ResolvedAt,
		})
	}
	api.WriteData(w, r, http.StatusOK, map[string]any{"items": items, "next_cursor": result.NextCursor})
}

func (h *HTTPHandler) requestMeta(r *http.Request) RequestMeta {
	return RequestMeta{
		RequestID: requestctx.RequestID(r.Context()),
		IPAddress: middleware.ClientIP(r, h.trustProxyHeader),
		UserAgent: r.UserAgent(),
		DeviceID:  r.Header.Get("X-Device-Id"),
	}
}

func (h *HTTPHandler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, message, details := ErrorDetails(err)
	if code == CodeBindRateLimited {
		if seconds, ok := details["retry_after_seconds"].(int); ok {
			w.Header().Set("Retry-After", strconv.Itoa(seconds))
		}
	}
	if status >= 500 {
		h.logger.ErrorContext(r.Context(), "authentication request failed", "code", code, "error", err)
	}
	api.WriteError(w, r, status, code, message, details)
}

func sessionToResponse(tokens SessionTokens) sessionResponse {
	return sessionResponse{
		AccessToken:           tokens.AccessToken,
		AccessTokenExpiresAt:  tokens.AccessTokenExpiresAt,
		RefreshToken:          tokens.RefreshToken,
		RefreshTokenExpiresAt: tokens.RefreshTokenExpiresAt,
	}
}

func defaultQuery(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
