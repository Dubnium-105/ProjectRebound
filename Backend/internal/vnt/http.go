package vnt

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/api"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/auth"
	appmiddleware "github.com/Dubnium-105/ProjectRebound/Backend/internal/middleware"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/player"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/requestctx"
	"github.com/go-chi/chi/v5"
)

type HTTPService interface {
	CreateEnrollment(context.Context, Actor, string) (EnrollmentResult, error)
	Register(context.Context, string, RegisterInput) (RegisterResult, error)
	Recover(context.Context, string, string, RegisterInput) (RegisterResult, error)
	List(context.Context, ListFilter) (ListResult, error)
	ListOwned(context.Context, Actor, OwnedListFilter) (OwnedListResult, error)
	Heartbeat(context.Context, string, string, HeartbeatInput) error
	Retire(context.Context, string, string) (string, error)
	RetireOwned(context.Context, Actor, string) (string, error)
	RotateCredential(context.Context, string, string) (CredentialRotationResult, error)
}

type HTTPHandler struct {
	service             HTTPService
	logger              *slog.Logger
	accessAuthenticator auth.AccessAuthenticator
	trustProxyHeaders   bool
}

func NewHTTPHandler(service HTTPService, logger *slog.Logger) *HTTPHandler {
	return &HTTPHandler{service: service, logger: logger}
}

func (h *HTTPHandler) SetAccessAuthenticator(authenticator auth.AccessAuthenticator) {
	h.accessAuthenticator = authenticator
}

func (h *HTTPHandler) SetTrustProxyHeaders(value bool) { h.trustProxyHeaders = value }

func (h *HTTPHandler) CreateEnrollment(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Label string `json:"label"`
	}
	if err := api.DecodeJSON(r, &request); err != nil {
		h.badRequest(w, r, err)
		return
	}
	principal := auth.PrincipalFromContext(r.Context())
	actor := actorFromPrincipal(principal)
	result, err := h.service.CreateEnrollment(h.requestContext(r), actor, request.Label)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	api.WriteData(w, r, http.StatusCreated, map[string]any{"enrollment_code": result.Code, "expires_at": result.ExpiresAt})
}

func (h *HTTPHandler) Recover(w http.ResponseWriter, r *http.Request) {
	var request struct {
		AdvertisedHost       string   `json:"advertised_host"`
		Port                 int      `json:"port"`
		Region               string   `json:"region"`
		Location             string   `json:"location"`
		VNTSVersion          string   `json:"vnts_version"`
		WrapperVersion       string   `json:"wrapper_version"`
		ServerKeyFingerprint string   `json:"server_key_fingerprint"`
		SupportedTransports  []string `json:"supported_transports"`
		MaxRooms             int      `json:"max_rooms"`
	}
	if err := api.DecodeJSON(r, &request); err != nil {
		h.badRequest(w, r, err)
		return
	}
	result, err := h.service.Recover(
		h.requestContext(r), chi.URLParam(r, "node_id"), authorizationToken(r, "VNTEnrollment"),
		RegisterInput{
			AdvertisedHost: request.AdvertisedHost, Port: request.Port, Region: request.Region,
			Location: request.Location, VNTSVersion: request.VNTSVersion,
			WrapperVersion: request.WrapperVersion, ServerKeyFingerprint: request.ServerKeyFingerprint,
			SupportedTransports: request.SupportedTransports, MaxRooms: request.MaxRooms,
		},
	)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	api.WriteData(w, r, http.StatusOK, map[string]any{
		"node_id": result.NodeID, "node_token": result.NodeToken, "state": result.State,
		"heartbeat_interval_seconds": result.HeartbeatIntervalSeconds,
		"credential_expires_at":      result.CredentialExpiresAt,
	})
}

func (h *HTTPHandler) Register(w http.ResponseWriter, r *http.Request) {
	var request struct {
		AdvertisedHost       string   `json:"advertised_host"`
		Port                 int      `json:"port"`
		Region               string   `json:"region"`
		Location             string   `json:"location"`
		VNTSVersion          string   `json:"vnts_version"`
		WrapperVersion       string   `json:"wrapper_version"`
		ServerKeyFingerprint string   `json:"server_key_fingerprint"`
		SupportedTransports  []string `json:"supported_transports"`
		MaxRooms             int      `json:"max_rooms"`
	}
	if err := api.DecodeJSON(r, &request); err != nil {
		h.badRequest(w, r, err)
		return
	}
	code := authorizationToken(r, "VNTEnrollment")
	result, err := h.service.Register(h.requestContext(r), code, RegisterInput{
		AdvertisedHost: request.AdvertisedHost, Port: request.Port, Region: request.Region,
		Location: request.Location, VNTSVersion: request.VNTSVersion,
		WrapperVersion: request.WrapperVersion, ServerKeyFingerprint: request.ServerKeyFingerprint,
		SupportedTransports: request.SupportedTransports, MaxRooms: request.MaxRooms,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	api.WriteData(w, r, http.StatusCreated, map[string]any{
		"node_id": result.NodeID, "node_token": result.NodeToken, "state": result.State,
		"heartbeat_interval_seconds": result.HeartbeatIntervalSeconds,
		"credential_expires_at":      result.CredentialExpiresAt,
	})
}

func (h *HTTPHandler) List(w http.ResponseWriter, r *http.Request) {
	limit, err := strconv.Atoi(defaultValue(r.URL.Query().Get("limit"), "100"))
	if err != nil {
		h.badRequest(w, r, err)
		return
	}
	result, serviceErr := h.service.List(h.requestContext(r), ListFilter{
		Status: r.URL.Query().Get("status"), Region: r.URL.Query().Get("region"),
		Cursor: r.URL.Query().Get("cursor"), Limit: limit,
	})
	if serviceErr != nil {
		h.writeError(w, r, serviceErr)
		return
	}
	api.WriteData(w, r, http.StatusOK, map[string]any{"items": result.Items, "next_cursor": result.NextCursor})
}

func (h *HTTPHandler) ListOwned(w http.ResponseWriter, r *http.Request) {
	limit, err := strconv.Atoi(defaultValue(r.URL.Query().Get("limit"), "50"))
	if err != nil {
		h.badRequest(w, r, err)
		return
	}
	result, serviceErr := h.service.ListOwned(h.requestContext(r), actorFromPrincipal(auth.PrincipalFromContext(r.Context())), OwnedListFilter{
		Status: r.URL.Query().Get("status"), Cursor: r.URL.Query().Get("cursor"), Limit: limit,
	})
	if serviceErr != nil {
		h.writeError(w, r, serviceErr)
		return
	}
	api.WriteData(w, r, http.StatusOK, map[string]any{"items": result.Items, "next_cursor": result.NextCursor})
}

func (h *HTTPHandler) Heartbeat(w http.ResponseWriter, r *http.Request) {
	var request struct {
		WrapperVersion       string `json:"wrapper_version"`
		VNTSVersion          string `json:"vnts_version"`
		UptimeSeconds        int64  `json:"uptime_seconds"`
		ReportedSessions     int    `json:"reported_sessions"`
		ServerProcessHealthy bool   `json:"server_process_healthy"`
	}
	if err := api.DecodeJSON(r, &request); err != nil {
		h.badRequest(w, r, err)
		return
	}
	if err := h.service.Heartbeat(h.requestContext(r), chi.URLParam(r, "node_id"), authorizationToken(r, "Bearer"), HeartbeatInput{
		WrapperVersion: request.WrapperVersion, VNTSVersion: request.VNTSVersion,
		UptimeSeconds: request.UptimeSeconds, ReportedSessions: request.ReportedSessions,
		ServerProcessHealthy: request.ServerProcessHealthy,
	}); err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusOK, map[string]any{"accepted": true})
}

func (h *HTTPHandler) Retire(w http.ResponseWriter, r *http.Request) {
	token := authorizationToken(r, "Bearer")
	var state string
	var err error
	if strings.HasPrefix(token, "vnn_") {
		state, err = h.service.Retire(h.requestContext(r), chi.URLParam(r, "node_id"), token)
	} else {
		if h.accessAuthenticator == nil {
			h.writeError(w, r, serviceError(http.StatusUnauthorized, "VNT_NODE_UNAUTHORIZED", "A VNT node or player credential is required."))
			return
		}
		principal, authErr := h.accessAuthenticator.AuthenticateAccess(r.Context(), token)
		if authErr != nil {
			status, code, message, details := auth.ErrorDetails(authErr)
			api.WriteError(w, r, status, code, message, details)
			return
		}
		state, err = h.service.RetireOwned(h.requestContext(r), actorFromPrincipal(&principal), chi.URLParam(r, "node_id"))
	}
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusOK, map[string]any{"state": state})
}

func actorFromPrincipal(principal *auth.Principal) Actor {
	if principal == nil {
		return Actor{}
	}
	return Actor{
		PlayerID: principal.Player.ID, AccountStatus: string(principal.Player.AccountStatus),
		SteamVerified: principal.SteamVerified, IntegrityTrusted: principal.AuthLevel == player.AuthLevelTrusted,
	}
}

func (h *HTTPHandler) requestContext(r *http.Request) context.Context {
	return WithRequestMeta(r.Context(), RequestMeta{
		RequestID: requestctx.RequestID(r.Context()),
		IPAddress: appmiddleware.ClientIP(r, h.trustProxyHeaders),
		UserAgent: r.UserAgent(),
	})
}

func (h *HTTPHandler) RotateCredential(w http.ResponseWriter, r *http.Request) {
	result, err := h.service.RotateCredential(h.requestContext(r), chi.URLParam(r, "node_id"), authorizationToken(r, "Bearer"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	api.WriteData(w, r, http.StatusOK, map[string]any{
		"node_token": result.NodeToken, "credential_expires_at": result.CredentialExpiresAt,
		"previous_valid_until": result.PreviousValidUntil,
	})
}

func (h *HTTPHandler) badRequest(w http.ResponseWriter, r *http.Request, err error) {
	api.WriteError(w, r, 400, "INVALID_REQUEST", "Invalid request.", map[string]any{"body": err.Error()})
}
func (h *HTTPHandler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, message, details := errorDetails(err)
	if status == http.StatusTooManyRequests && details != nil {
		if retryAfter, ok := details["retry_after_seconds"].(int); ok && retryAfter > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
		}
	}
	if status >= 500 {
		h.logger.ErrorContext(r.Context(), "VNT request failed", "code", code, "error", err)
	}
	api.WriteError(w, r, status, code, message, details)
}
func authorizationToken(r *http.Request, scheme string) string {
	parts := strings.Fields(r.Header.Get("Authorization"))
	if len(parts) == 2 && strings.EqualFold(parts[0], scheme) {
		return parts[1]
	}
	return ""
}
func defaultValue(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
