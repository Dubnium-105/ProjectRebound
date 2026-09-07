package admin

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/api"
	appmiddleware "github.com/Dubnium-105/ProjectRebound/Backend/internal/middleware"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/requestctx"
	"github.com/go-chi/chi/v5"
)

// MatchAttemptForceAbortService is implemented by the strict match-lobby
// service adapter. Keeping the interface here lets the admin route reuse the
// established session, permission, step-up, and audit conventions without
// making the admin package depend on match-lobby model types.
type MatchAttemptForceAbortService interface {
	ForceAbort(context.Context, string, string, string, RequestMeta) (any, error)
}

type MatchAttemptHTTPHandler struct {
	service    MatchAttemptForceAbortService
	logger     *slog.Logger
	trustProxy bool
}

type matchAttemptForceAbortRequest struct {
	FailureCode string `json:"failure_code"`
	Reason      string `json:"reason"`
}

func NewMatchAttemptHTTPHandler(service MatchAttemptForceAbortService, logger *slog.Logger, trustProxy bool) *MatchAttemptHTTPHandler {
	return &MatchAttemptHTTPHandler{service: service, logger: logger, trustProxy: trustProxy}
}

// ForceAbort is a high-risk operator action. The route is mounted with the
// existing rooms.close permission and RequireStepUp middleware; the service
// records the state transition and leaves cleanup pending for native proof.
func (h *MatchAttemptHTTPHandler) ForceAbort(w http.ResponseWriter, r *http.Request) {
	var request matchAttemptForceAbortRequest
	if err := api.DecodeJSON(r, &request); err != nil {
		api.WriteError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request.", nil)
		return
	}
	principal := PrincipalFromContext(r.Context())
	if principal == nil {
		api.WriteError(w, r, http.StatusUnauthorized, "ADMIN_UNAUTHORIZED", "Administrator authentication is required.", nil)
		return
	}
	result, err := h.service.ForceAbort(
		r.Context(), chi.URLParam(r, "attempt_id"), strings.TrimSpace(request.FailureCode),
		strings.TrimSpace(request.Reason), RequestMeta{
			AdminID: principal.AdminID, RequestID: requestctx.RequestID(r.Context()),
			IPAddress: appmiddleware.ClientIP(r, h.trustProxy), UserAgent: r.UserAgent(),
		},
	)
	if err != nil {
		status, code, message, details := errorDetails(err)
		if status >= http.StatusInternalServerError {
			h.logger.ErrorContext(r.Context(), "administrator match attempt force-abort failed", "code", code, "error", err)
		}
		api.WriteError(w, r, status, code, message, details)
		return
	}
	api.WriteData(w, r, http.StatusOK, map[string]any{"attempt": result})
}
