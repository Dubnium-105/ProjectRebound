package admin

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/api"
	appmiddleware "github.com/Dubnium-105/ProjectRebound/Backend/internal/middleware"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/requestctx"
	"github.com/go-chi/chi/v5"
)

type SecurityHTTPService interface {
	Summary(context.Context) (DashboardSummary, error)
	Timeseries(context.Context, string) ([]DashboardPoint, error)
	Alerts(context.Context) ([]DashboardAlert, error)
	ListRiskEvents(context.Context, RiskEventFilter) ([]AdminRiskEvent, string, error)
	GetRiskEvent(context.Context, string) (AdminRiskEvent, error)
	ResolveRiskEvent(context.Context, string, string, RequestMeta) (AdminRiskEvent, error)
	ListAudit(context.Context, AuditFilter) ([]AuditEntry, string, error)
	GetAudit(context.Context, string) (AuditEntry, error)
	ListLoginAudit(context.Context, LoginAuditFilter) ([]LoginAuditEntry, string, error)
	PlayerSessions(context.Context, string) ([]PlayerSessionEntry, error)
	PlayerRiskEvents(context.Context, string) ([]AdminRiskEvent, error)
	PlayerLoginEvents(context.Context, string) ([]PlayerLoginEventEntry, error)
}

type SecurityHTTPHandler struct {
	service    SecurityHTTPService
	logger     *slog.Logger
	trustProxy bool
}

func NewSecurityHTTPHandler(service SecurityHTTPService, logger *slog.Logger, trustProxy bool) *SecurityHTTPHandler {
	return &SecurityHTTPHandler{service: service, logger: logger, trustProxy: trustProxy}
}

func (h *SecurityHTTPHandler) Summary(w http.ResponseWriter, r *http.Request) {
	result, err := h.service.Summary(r.Context())
	h.writeData(w, r, result, err)
}

func (h *SecurityHTTPHandler) Timeseries(w http.ResponseWriter, r *http.Request) {
	items, err := h.service.Timeseries(r.Context(), r.URL.Query().Get("period"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, 200, map[string]any{"items": items, "period": defaultString(r.URL.Query().Get("period"), "24h")})
}

func (h *SecurityHTTPHandler) Alerts(w http.ResponseWriter, r *http.Request) {
	items, err := h.service.Alerts(r.Context())
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, 200, map[string]any{"items": items})
}

func (h *SecurityHTTPHandler) ListRiskEvents(w http.ResponseWriter, r *http.Request) {
	limit, ok := queryLimit(w, r)
	if !ok {
		return
	}
	unresolvedOnly, err := strconv.ParseBool(defaultString(r.URL.Query().Get("unresolved_only"), "false"))
	if err != nil {
		api.WriteError(w, r, 400, "INVALID_REQUEST", "Invalid unresolved_only value.", nil)
		return
	}
	items, nextCursor, err := h.service.ListRiskEvents(r.Context(), RiskEventFilter{
		Cursor: r.URL.Query().Get("cursor"), PlayerID: r.URL.Query().Get("player_id"),
		SteamID: r.URL.Query().Get("steam_id"), EventType: r.URL.Query().Get("event_type"),
		Severity: r.URL.Query().Get("severity"), UnresolvedOnly: unresolvedOnly, Limit: limit,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, 200, map[string]any{"items": items, "next_cursor": nextCursor})
}

func (h *SecurityHTTPHandler) GetRiskEvent(w http.ResponseWriter, r *http.Request) {
	result, err := h.service.GetRiskEvent(r.Context(), chi.URLParam(r, "event_id"))
	h.writeData(w, r, result, err)
}

func (h *SecurityHTTPHandler) ResolveRiskEvent(w http.ResponseWriter, r *http.Request) {
	var request reasonRequest
	if err := api.DecodeJSON(r, &request); err != nil {
		api.WriteError(w, r, 400, "INVALID_REQUEST", "Invalid request.", map[string]any{"body": err.Error()})
		return
	}
	result, err := h.service.ResolveRiskEvent(
		r.Context(), chi.URLParam(r, "event_id"), request.Reason, h.requestMeta(r),
	)
	h.writeData(w, r, result, err)
}

func (h *SecurityHTTPHandler) ListAudit(w http.ResponseWriter, r *http.Request) {
	limit, ok := queryLimit(w, r)
	if !ok {
		return
	}
	items, nextCursor, err := h.service.ListAudit(r.Context(), AuditFilter{
		Cursor: r.URL.Query().Get("cursor"), AdminID: r.URL.Query().Get("admin_id"),
		Action: r.URL.Query().Get("action"), TargetType: r.URL.Query().Get("target_type"),
		TargetID: r.URL.Query().Get("target_id"), Limit: limit,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, 200, map[string]any{"items": items, "next_cursor": nextCursor})
}

func (h *SecurityHTTPHandler) GetAudit(w http.ResponseWriter, r *http.Request) {
	result, err := h.service.GetAudit(r.Context(), chi.URLParam(r, "audit_id"))
	h.writeData(w, r, result, err)
}

func (h *SecurityHTTPHandler) ListLoginAudit(w http.ResponseWriter, r *http.Request) {
	limit, ok := queryLimit(w, r)
	if !ok {
		return
	}
	items, nextCursor, err := h.service.ListLoginAudit(r.Context(), LoginAuditFilter{
		Cursor: r.URL.Query().Get("cursor"), AdminID: r.URL.Query().Get("admin_id"),
		Result: strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("result"))), Limit: limit,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, 200, map[string]any{"items": items, "next_cursor": nextCursor})
}

func (h *SecurityHTTPHandler) PlayerSessions(w http.ResponseWriter, r *http.Request) {
	items, err := h.service.PlayerSessions(r.Context(), chi.URLParam(r, "player_id"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, 200, map[string]any{"items": items})
}

func (h *SecurityHTTPHandler) PlayerRiskEvents(w http.ResponseWriter, r *http.Request) {
	items, err := h.service.PlayerRiskEvents(r.Context(), chi.URLParam(r, "player_id"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, 200, map[string]any{"items": items, "next_cursor": ""})
}

func (h *SecurityHTTPHandler) PlayerLoginEvents(w http.ResponseWriter, r *http.Request) {
	items, err := h.service.PlayerLoginEvents(r.Context(), chi.URLParam(r, "player_id"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, 200, map[string]any{"items": items})
}

func (h *SecurityHTTPHandler) requestMeta(r *http.Request) RequestMeta {
	principal := PrincipalFromContext(r.Context())
	adminID := ""
	if principal != nil {
		adminID = principal.AdminID
	}
	return RequestMeta{
		AdminID: adminID, RequestID: requestctx.RequestID(r.Context()),
		IPAddress: appmiddleware.ClientIP(r, h.trustProxy), UserAgent: r.UserAgent(),
	}
}

func (h *SecurityHTTPHandler) writeData(w http.ResponseWriter, r *http.Request, data any, err error) {
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusOK, data)
}

func (h *SecurityHTTPHandler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, message, details := errorDetails(err)
	if status >= 500 {
		h.logger.ErrorContext(r.Context(), "administrator security request failed", "code", code, "error", err)
	}
	api.WriteError(w, r, status, code, message, details)
}

func queryLimit(w http.ResponseWriter, r *http.Request) (int, bool) {
	limit, err := strconv.Atoi(defaultString(r.URL.Query().Get("limit"), "50"))
	if err != nil {
		api.WriteError(w, r, 400, "INVALID_REQUEST", "Invalid limit.", nil)
		return 0, false
	}
	return limit, true
}
