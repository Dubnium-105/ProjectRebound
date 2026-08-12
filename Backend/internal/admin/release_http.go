package admin

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/api"
	appmiddleware "github.com/Dubnium-105/ProjectRebound/Backend/internal/middleware"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/requestctx"
	clientupdate "github.com/Dubnium-105/ProjectRebound/Backend/internal/update"
	"github.com/go-chi/chi/v5"
)

type ReleaseHTTPService interface {
	Create(context.Context, ReleaseCreateInput, RequestMeta) (Release, error)
	List(context.Context, ReleaseListFilter) (ReleaseListResult, error)
	Get(context.Context, string) (Release, error)
	Validate(context.Context, string, string, RequestMeta) (Release, error)
	Publish(context.Context, string, string, RequestMeta) (Release, error)
	Rollback(context.Context, string, string, RequestMeta) (Release, error)
	Archive(context.Context, string, string, RequestMeta) (Release, error)
}

type ReleaseHTTPHandler struct {
	service    ReleaseHTTPService
	logger     *slog.Logger
	trustProxy bool
}

func NewReleaseHTTPHandler(
	service ReleaseHTTPService,
	logger *slog.Logger,
	trustProxy bool,
) *ReleaseHTTPHandler {
	return &ReleaseHTTPHandler{service: service, logger: logger, trustProxy: trustProxy}
}

type releaseCreateRequest struct {
	Platform                string                    `json:"platform"`
	Architecture            string                    `json:"architecture"`
	Channel                 string                    `json:"channel"`
	Version                 string                    `json:"version"`
	MinimumSupportedVersion string                    `json:"minimum_supported_version"`
	ForceUpdate             bool                      `json:"force_update"`
	Files                   []clientupdate.SourceFile `json:"files"`
	Reason                  string                    `json:"reason"`
}

type releaseResponse struct {
	ID                      string                          `json:"id"`
	Product                 string                          `json:"product"`
	Platform                string                          `json:"platform"`
	Architecture            string                          `json:"architecture"`
	Channel                 string                          `json:"channel"`
	Version                 string                          `json:"version"`
	MinimumSupportedVersion string                          `json:"minimum_supported_version"`
	ForceUpdate             bool                            `json:"force_update"`
	Status                  string                          `json:"status"`
	Files                   []clientupdate.SourceFile       `json:"files"`
	VNTRuntime              *clientupdate.VNTRuntimeRelease `json:"vnt_runtime,omitempty"`
	Manifest                *clientupdate.Manifest          `json:"manifest,omitempty"`
	Validation              ReleaseValidation               `json:"validation"`
	CreatedBy               string                          `json:"created_by"`
	PublishedBy             string                          `json:"published_by"`
	RolledBackBy            string                          `json:"rolled_back_by"`
	ArchivedBy              string                          `json:"archived_by"`
	CreatedAt               time.Time                       `json:"created_at"`
	UpdatedAt               time.Time                       `json:"updated_at"`
	PublishedAt             *time.Time                      `json:"published_at"`
	RolledBackAt            *time.Time                      `json:"rolled_back_at"`
	ArchivedAt              *time.Time                      `json:"archived_at"`
}

func (h *ReleaseHTTPHandler) Create(w http.ResponseWriter, r *http.Request) {
	var request releaseCreateRequest
	if err := api.DecodeJSON(r, &request); err != nil {
		api.WriteError(w, r, 400, "INVALID_REQUEST", "Invalid request.", nil)
		return
	}
	item, err := h.service.Create(r.Context(), ReleaseCreateInput{
		Platform: request.Platform, Architecture: request.Architecture,
		Channel: request.Channel, Version: request.Version,
		MinimumSupportedVersion: request.MinimumSupportedVersion,
		ForceUpdate:             request.ForceUpdate, Files: request.Files, Reason: request.Reason,
	}, h.requestMeta(r))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusCreated, toReleaseResponse(item))
}

func (h *ReleaseHTTPHandler) List(w http.ResponseWriter, r *http.Request) {
	limit, err := strconv.Atoi(defaultOnlineString(r.URL.Query().Get("limit"), "50"))
	if err != nil {
		api.WriteError(w, r, 400, "INVALID_REQUEST", "Invalid limit.", nil)
		return
	}
	result, err := h.service.List(r.Context(), ReleaseListFilter{
		Cursor: r.URL.Query().Get("cursor"), Status: r.URL.Query().Get("status"),
		Platform:     r.URL.Query().Get("platform"),
		Architecture: r.URL.Query().Get("architecture"),
		Channel:      r.URL.Query().Get("channel"), Limit: limit,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	items := make([]releaseResponse, 0, len(result.Items))
	for _, item := range result.Items {
		items = append(items, toReleaseResponse(item))
	}
	api.WriteData(w, r, 200, map[string]any{"items": items, "next_cursor": result.NextCursor})
}

func (h *ReleaseHTTPHandler) Get(w http.ResponseWriter, r *http.Request) {
	item, err := h.service.Get(r.Context(), chi.URLParam(r, "release_id"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, 200, toReleaseResponse(item))
}

func (h *ReleaseHTTPHandler) Validate(w http.ResponseWriter, r *http.Request) {
	h.transition(w, r, "validate")
}

func (h *ReleaseHTTPHandler) Publish(w http.ResponseWriter, r *http.Request) {
	h.transition(w, r, "publish")
}

func (h *ReleaseHTTPHandler) Rollback(w http.ResponseWriter, r *http.Request) {
	h.transition(w, r, "rollback")
}

func (h *ReleaseHTTPHandler) Archive(w http.ResponseWriter, r *http.Request) {
	h.transition(w, r, "archive")
}

func (h *ReleaseHTTPHandler) transition(w http.ResponseWriter, r *http.Request, operation string) {
	var request reasonRequest
	if err := api.DecodeJSON(r, &request); err != nil {
		api.WriteError(w, r, 400, "INVALID_REQUEST", "Invalid request.", nil)
		return
	}
	var item Release
	var err error
	switch operation {
	case "validate":
		item, err = h.service.Validate(r.Context(), chi.URLParam(r, "release_id"), request.Reason, h.requestMeta(r))
	case "publish":
		item, err = h.service.Publish(r.Context(), chi.URLParam(r, "release_id"), request.Reason, h.requestMeta(r))
	case "rollback":
		item, err = h.service.Rollback(r.Context(), chi.URLParam(r, "release_id"), request.Reason, h.requestMeta(r))
	case "archive":
		item, err = h.service.Archive(r.Context(), chi.URLParam(r, "release_id"), request.Reason, h.requestMeta(r))
	}
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, 200, toReleaseResponse(item))
}

func (h *ReleaseHTTPHandler) requestMeta(r *http.Request) RequestMeta {
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

func (h *ReleaseHTTPHandler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, message, details := errorDetails(err)
	if status >= 500 {
		h.logger.ErrorContext(r.Context(), "administrator release request failed", "code", code, "error", err)
	}
	api.WriteError(w, r, status, code, message, details)
}

func toReleaseResponse(item Release) releaseResponse {
	files := item.Source.Files
	if files == nil {
		files = make([]clientupdate.SourceFile, 0)
	}
	return releaseResponse{
		ID: item.ID, Product: item.Product, Platform: item.Platform,
		Architecture: item.Architecture, Channel: item.Channel, Version: item.Version,
		MinimumSupportedVersion: item.MinimumSupportedVersion,
		ForceUpdate:             item.ForceUpdate, Status: item.Status, Files: files,
		VNTRuntime: item.Source.VNTRuntime,
		Manifest:   item.Manifest, Validation: item.Validation, CreatedBy: item.CreatedBy,
		PublishedBy: item.PublishedBy, RolledBackBy: item.RolledBackBy, ArchivedBy: item.ArchivedBy,
		CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
		PublishedAt: item.PublishedAt, RolledBackAt: item.RolledBackAt, ArchivedAt: item.ArchivedAt,
	}
}
