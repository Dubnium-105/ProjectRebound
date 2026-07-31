package diagnostic

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/api"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/auth"
)

type ReportStore interface {
	Store(context.Context, string, string) error
}

type HTTPHandler struct {
	reports ReportStore
	logger  *slog.Logger
}

func NewHTTPHandler(reports ReportStore, logger *slog.Logger) *HTTPHandler {
	return &HTTPHandler{reports: reports, logger: logger}
}

type reportRequest struct {
	Report *string `json:"report"`
}

func (h *HTTPHandler) Submit(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		api.WriteError(w, r, http.StatusUnauthorized, auth.CodeUnauthorized, "Authentication is required.", nil)
		return
	}

	var request reportRequest
	if err := api.DecodeJSON(r, &request); err != nil || request.Report == nil {
		details := map[string]any{}
		if err != nil {
			details["body"] = err.Error()
		}
		api.WriteError(w, r, http.StatusBadRequest, auth.CodeInvalidRequest, "A JSON report field is required.", details)
		return
	}
	if err := h.reports.Store(r.Context(), principal.Player.ID, *request.Report); err != nil {
		if h.logger != nil {
			h.logger.ErrorContext(r.Context(), "diagnostic report storage failed", "error", err)
		}
		api.WriteError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal server error.", nil)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}
