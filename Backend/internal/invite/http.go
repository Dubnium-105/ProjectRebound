package invite

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/admin"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/api"
	appmiddleware "github.com/Dubnium-105/ProjectRebound/Backend/internal/middleware"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/requestctx"
	"github.com/go-chi/chi/v5"
)

type HTTPService interface {
	Create(context.Context, CreateInput, RequestMeta) (CreateResult, error)
	List(context.Context, string, int) (ListResult, error)
	Get(context.Context, string) (Code, error)
	ListUses(context.Context, string, string, int) (UseListResult, error)
	Patch(context.Context, string, Patch, RequestMeta) (Code, error)
	Revoke(context.Context, string, string, RequestMeta) (Code, error)
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
	Quantity    int            `json:"quantity"`
	MaxUses     int            `json:"max_uses"`
	ExpiresAt   *time.Time     `json:"expires_at"`
	Permissions map[string]any `json:"permissions"`
	Reason      string         `json:"reason"`
}

type patchRequest struct {
	BatchName   *string         `json:"batch_name"`
	MaxUses     *int            `json:"max_uses"`
	ExpiresAt   json.RawMessage `json:"expires_at"`
	Enabled     *bool           `json:"enabled"`
	Permissions map[string]any  `json:"permissions"`
	Reason      string          `json:"reason"`
}

type revokeRequest struct {
	Reason string `json:"reason"`
}

type createdCodeResponse struct {
	InviteCode codeResponse `json:"invite_code"`
	Code       string       `json:"code"`
}

type useResponse struct {
	ID           string    `json:"id"`
	InviteCodeID string    `json:"invite_code_id"`
	PlayerID     string    `json:"player_id"`
	SteamID      string    `json:"steam_id"`
	IPAddress    string    `json:"ip_address"`
	UsedAt       time.Time `json:"used_at"`
	Result       string    `json:"result"`
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
		BatchName: request.BatchName, Quantity: request.Quantity, MaxUses: request.MaxUses,
		ExpiresAt: request.ExpiresAt, Permissions: request.Permissions, Reason: request.Reason,
	}, h.requestMeta(r))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	created := make([]createdCodeResponse, 0, len(result.Items))
	for _, item := range result.Items {
		created = append(created, createdCodeResponse{InviteCode: toResponse(item.Code), Code: item.Plaintext})
	}
	api.WriteData(w, r, http.StatusCreated, map[string]any{
		"invite_code":  toResponse(result.Code),
		"code":         result.Plaintext,
		"invite_codes": created,
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

func (h *HTTPHandler) ListUses(w http.ResponseWriter, r *http.Request) {
	limit, err := strconv.Atoi(defaultString(r.URL.Query().Get("limit"), "50"))
	if err != nil {
		api.WriteError(w, r, 400, "INVALID_REQUEST", "Invalid limit.", nil)
		return
	}
	result, err := h.service.ListUses(
		r.Context(), chi.URLParam(r, "id"), r.URL.Query().Get("cursor"), limit,
	)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	items := make([]useResponse, 0, len(result.Items))
	for _, item := range result.Items {
		items = append(items, useResponse{
			ID: item.ID, InviteCodeID: item.InviteCodeID, PlayerID: item.PlayerID,
			SteamID: item.SteamID, IPAddress: maskIPAddress(item.IPAddress),
			UsedAt: item.UsedAt, Result: item.Result,
		})
	}
	api.WriteData(w, r, 200, map[string]any{"items": items, "next_cursor": result.NextCursor})
}

func (h *HTTPHandler) Patch(w http.ResponseWriter, r *http.Request) {
	var request patchRequest
	if err := api.DecodeJSON(r, &request); err != nil {
		api.WriteError(w, r, 400, "INVALID_REQUEST", "Invalid request.", map[string]any{"body": err.Error()})
		return
	}
	patch := Patch{
		BatchName: request.BatchName, MaxUses: request.MaxUses,
		Enabled: request.Enabled, Permissions: request.Permissions, Reason: request.Reason,
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
	var request revokeRequest
	if err := api.DecodeJSON(r, &request); err != nil {
		api.WriteError(w, r, 400, "INVALID_REQUEST", "Invalid request.", map[string]any{"body": err.Error()})
		return
	}
	item, err := h.service.Revoke(r.Context(), chi.URLParam(r, "id"), request.Reason, h.requestMeta(r))
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
		UserAgent: r.UserAgent(),
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

func maskIPAddress(value string) string {
	ip := net.ParseIP(value)
	if ip == nil {
		return ""
	}
	if ipv4 := ip.To4(); ipv4 != nil {
		return net.IPv4(ipv4[0], ipv4[1], ipv4[2], 0).String() + "/24"
	}
	masked := make(net.IP, net.IPv6len)
	copy(masked, ip.To16())
	for index := 6; index < len(masked); index++ {
		masked[index] = 0
	}
	return masked.String() + "/48"
}
