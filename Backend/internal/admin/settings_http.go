package admin

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/projectrebound/matchserver/internal/api"
	appmiddleware "github.com/projectrebound/matchserver/internal/middleware"
	"github.com/projectrebound/matchserver/internal/requestctx"
)

type SettingsHTTPService interface {
	List(context.Context) ([]AdminSetting, error)
	Features(context.Context) (map[string]bool, error)
	Capabilities(context.Context) (AdminCapabilities, error)
	Update(context.Context, map[string]any, string, RequestMeta) ([]AdminSetting, error)
}

type SettingsHTTPHandler struct {
	service    SettingsHTTPService
	logger     *slog.Logger
	trustProxy bool
}

func NewSettingsHTTPHandler(
	service SettingsHTTPService,
	logger *slog.Logger,
	trustProxy bool,
) *SettingsHTTPHandler {
	return &SettingsHTTPHandler{service: service, logger: logger, trustProxy: trustProxy}
}

type updateSettingsRequest struct {
	Values map[string]any `json:"values"`
	Reason string         `json:"reason"`
}

func (h *SettingsHTTPHandler) List(w http.ResponseWriter, r *http.Request) {
	items, err := h.service.List(r.Context())
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, 200, map[string]any{"items": items})
}

func (h *SettingsHTTPHandler) Features(w http.ResponseWriter, r *http.Request) {
	features, err := h.service.Features(r.Context())
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, 200, features)
}

func (h *SettingsHTTPHandler) Capabilities(w http.ResponseWriter, r *http.Request) {
	capabilities, err := h.service.Capabilities(r.Context())
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, 200, capabilities)
}

func (h *SettingsHTTPHandler) Update(w http.ResponseWriter, r *http.Request) {
	var request updateSettingsRequest
	if err := api.DecodeJSON(r, &request); err != nil {
		api.WriteError(w, r, 400, "INVALID_REQUEST", "Invalid request.", nil)
		return
	}
	items, err := h.service.Update(r.Context(), request.Values, request.Reason, h.requestMeta(r))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, 200, map[string]any{"items": items})
}

func (h *SettingsHTTPHandler) requestMeta(r *http.Request) RequestMeta {
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

func (h *SettingsHTTPHandler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, message, details := errorDetails(err)
	if status >= http.StatusInternalServerError {
		h.logger.ErrorContext(r.Context(), "administrator settings request failed", "code", code, "error", err)
	}
	api.WriteError(w, r, status, code, message, details)
}
