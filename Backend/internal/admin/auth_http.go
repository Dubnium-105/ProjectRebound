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
	"github.com/projectrebound/matchserver/internal/config"
	appmiddleware "github.com/projectrebound/matchserver/internal/middleware"
	"github.com/projectrebound/matchserver/internal/requestctx"
)

const adminRefreshCookieName = "admin_refresh_token"

type AdminAuthHTTPService interface {
	TurnstileSiteKey() string
	TurnstileConfigured() bool
	Login(context.Context, LoginInput, RequestMeta) (LoginResult, error)
	VerifyMFA(context.Context, MFAVerifyInput, RequestMeta) (MFAVerifyResult, error)
	StepUp(context.Context, string, string, string, RequestMeta) (StepUpResult, error)
	Refresh(context.Context, string, RequestMeta) (RefreshAdminResult, error)
	Logout(context.Context, string) error
	ListSessions(context.Context, string, string) ([]SessionListItem, error)
	RevokeOwnedSession(context.Context, string, string) error
}

type AdminAuthHTTPHandler struct {
	service      AdminAuthHTTPService
	logger       *slog.Logger
	cfg          config.AdminConfig
	secureCookie bool
	trustProxy   bool
}

func NewAdminAuthHTTPHandler(
	service AdminAuthHTTPService,
	logger *slog.Logger,
	cfg config.AdminConfig,
	environment string,
	trustProxy bool,
) *AdminAuthHTTPHandler {
	return &AdminAuthHTTPHandler{
		service:      service,
		logger:       logger,
		cfg:          cfg,
		secureCookie: strings.EqualFold(environment, "production"),
		trustProxy:   trustProxy,
	}
}

type adminLoginRequest struct {
	Username       string `json:"username"`
	Password       string `json:"password"`
	TurnstileToken string `json:"turnstile_token"`
}

type adminMFAVerifyRequest struct {
	ChallengeToken string `json:"challenge_token"`
	Code           string `json:"code"`
}

type adminStepUpRequest struct {
	Code string `json:"code"`
}

type adminAccessResponse struct {
	AccessToken          string               `json:"access_token"`
	AccessTokenExpiresAt time.Time            `json:"access_token_expires_at"`
	Admin                currentAdminResponse `json:"admin"`
}

type currentAdminResponse struct {
	AdminID     string   `json:"admin_id"`
	Username    string   `json:"username"`
	DisplayName string   `json:"display_name"`
	Roles       []string `json:"roles"`
	Permissions []string `json:"permissions"`
}

type adminSessionResponse struct {
	SessionID  string     `json:"session_id"`
	IPAddress  string     `json:"ip_address"`
	UserAgent  string     `json:"user_agent"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	IsCurrent  bool       `json:"is_current"`
}

func (h *AdminAuthHTTPHandler) Config(w http.ResponseWriter, r *http.Request) {
	api.WriteData(w, r, http.StatusOK, map[string]any{
		"turnstile": map[string]any{
			"configured": h.service.TurnstileConfigured(),
			"site_key":   h.service.TurnstileSiteKey(),
			"action":     h.cfg.TurnstileExpectedAction,
		},
	})
}

func (h *AdminAuthHTTPHandler) Login(w http.ResponseWriter, r *http.Request) {
	var request adminLoginRequest
	if err := api.DecodeJSON(r, &request); err != nil {
		api.WriteError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request.", nil)
		return
	}
	result, err := h.service.Login(r.Context(), LoginInput{
		Username:       request.Username,
		Password:       request.Password,
		TurnstileToken: request.TurnstileToken,
	}, h.requestMeta(r))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusAccepted, map[string]any{
		"mfa_required":    result.MFARequired,
		"challenge_token": result.ChallengeToken,
		"expires_at":      result.ExpiresAt,
	})
}

func (h *AdminAuthHTTPHandler) VerifyMFA(w http.ResponseWriter, r *http.Request) {
	var request adminMFAVerifyRequest
	if err := api.DecodeJSON(r, &request); err != nil {
		api.WriteError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request.", nil)
		return
	}
	result, err := h.service.VerifyMFA(r.Context(), MFAVerifyInput{
		ChallengeToken: request.ChallengeToken,
		Code:           request.Code,
	}, h.requestMeta(r))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.setRefreshCookie(w, result.Tokens.RefreshToken, result.Tokens.RefreshExpiresAt)
	api.WriteData(w, r, http.StatusOK, toAdminAccessResponse(result.Tokens, result.Admin))
}

func (h *AdminAuthHTTPHandler) Refresh(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(adminRefreshCookieName)
	if err != nil || strings.TrimSpace(cookie.Value) == "" {
		api.WriteError(w, r, http.StatusUnauthorized, "ADMIN_UNAUTHORIZED", "Administrator authentication is required.", nil)
		return
	}
	result, err := h.service.Refresh(r.Context(), cookie.Value, h.requestMeta(r))
	if err != nil {
		h.clearRefreshCookie(w)
		h.writeError(w, r, err)
		return
	}
	h.setRefreshCookie(w, result.Tokens.RefreshToken, result.Tokens.RefreshExpiresAt)
	api.WriteData(w, r, http.StatusOK, toAdminAccessResponse(result.Tokens, result.Admin))
}

func (h *AdminAuthHTTPHandler) StepUp(w http.ResponseWriter, r *http.Request) {
	principal := PrincipalFromContext(r.Context())
	if principal == nil || principal.SessionID == "" {
		api.WriteError(w, r, http.StatusUnauthorized, "ADMIN_UNAUTHORIZED", "Administrator authentication is required.", nil)
		return
	}
	var request adminStepUpRequest
	if err := api.DecodeJSON(r, &request); err != nil {
		api.WriteError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request.", nil)
		return
	}
	result, err := h.service.StepUp(
		r.Context(), principal.AdminID, principal.SessionID, request.Code, h.requestMeta(r),
	)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusOK, map[string]any{
		"step_up_token": result.Token, "expires_at": result.ExpiresAt,
	})
}

func (h *AdminAuthHTTPHandler) Logout(w http.ResponseWriter, r *http.Request) {
	principal := PrincipalFromContext(r.Context())
	if principal == nil || principal.SessionID == "" {
		api.WriteError(w, r, http.StatusUnauthorized, "ADMIN_UNAUTHORIZED", "Administrator authentication is required.", nil)
		return
	}
	if err := h.service.Logout(r.Context(), principal.SessionID); err != nil {
		h.writeError(w, r, err)
		return
	}
	h.clearRefreshCookie(w)
	api.WriteData(w, r, http.StatusOK, map[string]bool{"logged_out": true})
}

func (h *AdminAuthHTTPHandler) Me(w http.ResponseWriter, r *http.Request) {
	principal := PrincipalFromContext(r.Context())
	if principal == nil || principal.SessionID == "" {
		api.WriteError(w, r, http.StatusUnauthorized, "ADMIN_UNAUTHORIZED", "Administrator authentication is required.", nil)
		return
	}
	api.WriteData(w, r, http.StatusOK, currentAdminResponse{
		AdminID:     principal.AdminID,
		Username:    principal.Username,
		DisplayName: principal.DisplayName,
		Roles:       principal.Roles,
		Permissions: principal.Permissions,
	})
}

func (h *AdminAuthHTTPHandler) Sessions(w http.ResponseWriter, r *http.Request) {
	principal := PrincipalFromContext(r.Context())
	if principal == nil || principal.SessionID == "" {
		api.WriteError(w, r, http.StatusUnauthorized, "ADMIN_UNAUTHORIZED", "Administrator authentication is required.", nil)
		return
	}
	items, err := h.service.ListSessions(r.Context(), principal.AdminID, principal.SessionID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	responses := make([]adminSessionResponse, 0, len(items))
	for _, item := range items {
		responses = append(responses, adminSessionResponse{
			SessionID:  item.ID,
			IPAddress:  item.IPAddress,
			UserAgent:  item.UserAgent,
			CreatedAt:  item.CreatedAt,
			LastUsedAt: item.LastUsedAt,
			ExpiresAt:  item.ExpiresAt,
			IsCurrent:  item.IsCurrent,
		})
	}
	api.WriteData(w, r, http.StatusOK, map[string]any{"items": responses})
}

func (h *AdminAuthHTTPHandler) RevokeSession(w http.ResponseWriter, r *http.Request) {
	principal := PrincipalFromContext(r.Context())
	if principal == nil || principal.SessionID == "" {
		api.WriteError(w, r, http.StatusUnauthorized, "ADMIN_UNAUTHORIZED", "Administrator authentication is required.", nil)
		return
	}
	sessionID := chi.URLParam(r, "session_id")
	if err := h.service.RevokeOwnedSession(r.Context(), principal.AdminID, sessionID); err != nil {
		h.writeError(w, r, err)
		return
	}
	if sessionID == principal.SessionID {
		h.clearRefreshCookie(w)
	}
	api.WriteData(w, r, http.StatusOK, map[string]bool{"revoked": true})
}

func (h *AdminAuthHTTPHandler) requestMeta(r *http.Request) RequestMeta {
	return RequestMeta{
		RequestID: requestctx.RequestID(r.Context()),
		IPAddress: appmiddleware.ClientIP(r, h.trustProxy),
		UserAgent: r.UserAgent(),
	}
}

func (h *AdminAuthHTTPHandler) setRefreshCookie(w http.ResponseWriter, token string, expiresAt time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     adminRefreshCookieName,
		Value:    token,
		Path:     "/v1/admin/auth",
		Expires:  expiresAt,
		MaxAge:   max(1, int(time.Until(expiresAt).Seconds())),
		HttpOnly: true,
		Secure:   h.secureCookie,
		SameSite: http.SameSiteStrictMode,
	})
}

func (h *AdminAuthHTTPHandler) clearRefreshCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     adminRefreshCookieName,
		Value:    "",
		Path:     "/v1/admin/auth",
		MaxAge:   -1,
		Expires:  time.Unix(1, 0),
		HttpOnly: true,
		Secure:   h.secureCookie,
		SameSite: http.SameSiteStrictMode,
	})
}

func (h *AdminAuthHTTPHandler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, message, details := errorDetails(err)
	if status == http.StatusTooManyRequests && details != nil {
		if retryAfter, ok := details["retry_after_seconds"].(int); ok {
			w.Header().Set("Retry-After", strconv.Itoa(max(1, retryAfter)))
		}
	}
	if status >= 500 {
		h.logger.ErrorContext(r.Context(), "administrator authentication request failed", "code", code, "error", err)
	}
	api.WriteError(w, r, status, code, message, details)
}

func toAdminAccessResponse(tokens AdminTokens, admin CurrentAdmin) adminAccessResponse {
	return adminAccessResponse{
		AccessToken:          tokens.AccessToken,
		AccessTokenExpiresAt: tokens.AccessTokenExpiresAt,
		Admin: currentAdminResponse{
			AdminID:     admin.User.ID,
			Username:    admin.User.Username,
			DisplayName: admin.User.DisplayName,
			Roles:       admin.Roles,
			Permissions: admin.Permissions,
		},
	}
}
