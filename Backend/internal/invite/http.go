package invite

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/projectrebound/matchserver/internal/admin"
	"github.com/projectrebound/matchserver/internal/api"
	appmiddleware "github.com/projectrebound/matchserver/internal/middleware"
	"github.com/projectrebound/matchserver/internal/requestctx"
)

type HTTPService interface {
	Create(context.Context, CreateInput, RequestMeta) (CreateResult, error)
	List(context.Context, string, int) (ListResult, error)
	Get(context.Context, string) (Code, error)
	Patch(context.Context, string, Patch, RequestMeta) (Code, error)
	Revoke(context.Context, string, RequestMeta) (Code, error)
}

type HTTPHandler struct {
	service    HTTPService
	logger     *slog.Logger
	trustProxy bool
}

func NewHTTPHandler(service HTTPService, logger *slog.Logger, trustProxy bool) *HTTPHandler {
	return &HTTPHandler{service: service, logger: logger, trustProxy: trustProxy}
}

type createRequest struct {
	BatchName   string         `json:"batch_name"`
	MaxUses     int            `json:"max_uses"`
	ExpiresAt   *time.Time     `json:"expires_at"`
	Permissions map[string]any `json:"permissions"`
}

type patchRequest struct {
	BatchName   *string         `json:"batch_name"`
	MaxUses     *int            `json:"max_uses"`
	ExpiresAt   json.RawMessage `json:"expires_at"`
	Enabled     *bool           `json:"enabled"`
	Permissions map[string]any  `json:"permissions"`
}

type codeResponse struct {
	ID          string         `json:"id"`
	BatchName   string         `json:"batch_name"`
	MaxUses     int            `json:"max_uses"`
	UsedCount   int            `json:"used_count"`
	ExpiresAt   *time.Time     `json:"expires_at"`
	Enabled     bool           `json:"enabled"`
	Permissions map[string]any `json:"permissions"`
	CreatedBy   string         `json:"created_by"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	RevokedAt   *time.Time     `json:"revoked_at"`
}

func (h *HTTPHandler) Create(w http.ResponseWriter, r *http.Request) {
	var request createRequest
	if err := api.DecodeJSON(r, &request); err != nil {
		api.WriteError(w, r, 400, "INVALID_REQUEST", "Invalid request.", map[string]any{"body": err.Error()})
		return
	}
	result, err := h.service.Create(r.Context(), CreateInput{
		BatchName: request.BatchName, MaxUses: request.MaxUses,
		ExpiresAt: request.ExpiresAt, Permissions: request.Permissions,
	}, h.requestMeta(r))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusCreated, map[string]any{
		"invite_code": toResponse(result.Code),
		"code":        result.Plaintext,
	})
}

func (h *HTTPHandler) List(w http.ResponseWriter, r *http.Request) {
	limit, err := strconv.Atoi(defaultString(r.URL.Query().Get("limit"), "50"))
	if err != nil {
		api.WriteError(w, r, 400, "INVALID_REQUEST", "Invalid limit.", nil)
		return
	}
	result, err := h.service.List(r.Context(), r.URL.Query().Get("cursor"), limit)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	items := make([]codeResponse, 0, len(result.Items))
	for _, item := range result.Items {
		items = append(items, toResponse(item))
	}
	api.WriteData(w, r, 200, map[string]any{"items": items, "next_cursor": result.NextCursor})
}

func (h *HTTPHandler) Get(w http.ResponseWriter, r *http.Request) {
	item, err := h.service.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, 200, toResponse(item))
}

func (h *HTTPHandler) Patch(w http.ResponseWriter, r *http.Request) {
	var request patchRequest
	if err := api.DecodeJSON(r, &request); err != nil {
		api.WriteError(w, r, 400, "INVALID_REQUEST", "Invalid request.", map[string]any{"body": err.Error()})
		return
	}
	patch := Patch{
		BatchName: request.BatchName, MaxUses: request.MaxUses,
		Enabled: request.Enabled, Permissions: request.Permissions,
	}
	if len(request.ExpiresAt) > 0 {
		if string(request.ExpiresAt) == "null" {
			patch.ClearExpiry = true
		} else {
			var expiresAt time.Time
			if err := json.Unmarshal(request.ExpiresAt, &expiresAt); err != nil {
				api.WriteError(w, r, 400, "INVALID_REQUEST", "Invalid expiry.", nil)
				return
			}
			patch.ExpiresAt = &expiresAt
		}
	}
	item, err := h.service.Patch(r.Context(), chi.URLParam(r, "id"), patch, h.requestMeta(r))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, 200, toResponse(item))
}

func (h *HTTPHandler) Revoke(w http.ResponseWriter, r *http.Request) {
	item, err := h.service.Revoke(r.Context(), chi.URLParam(r, "id"), h.requestMeta(r))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, 200, toResponse(item))
}

func (h *HTTPHandler) requestMeta(r *http.Request) RequestMeta {
	principal := admin.PrincipalFromContext(r.Context())
	adminID := ""
	if principal != nil {
		adminID = principal.AdminID
	}
	return RequestMeta{
		AdminID: adminID, RequestID: requestctx.RequestID(r.Context()),
		IPAddress: appmiddleware.ClientIP(r, h.trustProxy),
	}
}

func (h *HTTPHandler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, message, details := errorDetails(err)
	if status >= 500 {
		h.logger.ErrorContext(r.Context(), "invite request failed", "code", code, "error", err)
	}
	api.WriteError(w, r, status, code, message, details)
}

func toResponse(item Code) codeResponse {
	return codeResponse{
		ID: item.ID, BatchName: item.BatchName, MaxUses: item.MaxUses, UsedCount: item.UsedCount,
		ExpiresAt: item.ExpiresAt, Enabled: item.Enabled, Permissions: item.Permissions,
		CreatedBy: item.CreatedBy, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt, RevokedAt: item.RevokedAt,
	}
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
