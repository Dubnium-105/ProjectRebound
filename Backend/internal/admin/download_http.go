package admin

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/api"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/download"
	appmiddleware "github.com/Dubnium-105/ProjectRebound/Backend/internal/middleware"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/requestctx"
	"github.com/go-chi/chi/v5"
)

type DownloadHTTPService interface {
	Capabilities() download.Capabilities
	ListCategories(context.Context) ([]download.Category, error)
	CreateCategory(context.Context, download.CategoryInput, download.ActorMeta) (download.Category, error)
	UpdateCategory(context.Context, string, download.CategoryInput, download.ActorMeta) (download.Category, error)
	ArchiveCategory(context.Context, string, string, download.ActorMeta) (download.Category, error)
	ListEntries(context.Context) ([]download.Entry, error)
	ListReleaseFiles(context.Context) ([]download.ReleaseFile, error)
	GetEntry(context.Context, string) (download.Entry, error)
	CreateEntry(context.Context, download.EntryInput, download.ActorMeta) (download.Entry, error)
	UpdateEntry(context.Context, string, download.EntryInput, download.ActorMeta) (download.Entry, error)
	ArchiveEntry(context.Context, string, string, download.ActorMeta) (download.Entry, error)
	CreateUpload(context.Context, string, download.UploadInput, download.ActorMeta) (download.UploadCreated, error)
	GetUpload(context.Context, string) (download.UploadSession, error)
	SignParts(context.Context, string, []int32) ([]download.SignedPart, error)
	CompleteUpload(context.Context, string, string, download.ActorMeta) (download.UploadSession, error)
	AbortUpload(context.Context, string, string, download.ActorMeta) (download.UploadSession, error)
	Publish(context.Context, string, string, download.ActorMeta) (download.Version, error)
	ArchiveVersion(context.Context, string, string, download.ActorMeta) (download.Version, error)
}

type DownloadHTTPHandler struct {
	service    DownloadHTTPService
	logger     *slog.Logger
	trustProxy bool
}

func NewDownloadHTTPHandler(service DownloadHTTPService, logger *slog.Logger, trustProxy bool) *DownloadHTTPHandler {
	return &DownloadHTTPHandler{service: service, logger: logger, trustProxy: trustProxy}
}

type downloadCategoryRequest struct {
	Slug            string `json:"slug"`
	TitleEN         string `json:"title_en"`
	TitleZhCN       string `json:"title_zh_cn"`
	DescriptionEN   string `json:"description_en"`
	DescriptionZhCN string `json:"description_zh_cn"`
	SortOrder       int    `json:"sort_order"`
	Enabled         bool   `json:"enabled"`
	Reason          string `json:"reason"`
}

type downloadEntryRequest struct {
	CategoryID      string `json:"category_id"`
	Slug            string `json:"slug"`
	TitleEN         string `json:"title_en"`
	TitleZhCN       string `json:"title_zh_cn"`
	DescriptionEN   string `json:"description_en"`
	DescriptionZhCN string `json:"description_zh_cn"`
	SortOrder       int    `json:"sort_order"`
	Reason          string `json:"reason"`
}

type downloadUploadRequest struct {
	VersionLabel string `json:"version_label"`
	FileName     string `json:"file_name"`
	ContentType  string `json:"content_type"`
	SizeBytes    int64  `json:"size_bytes"`
	SHA256       string `json:"sha256"`
	Reason       string `json:"reason"`
}

type downloadPartRequest struct {
	PartNumbers []int32 `json:"part_numbers"`
}

type downloadOperationRequest struct {
	Reason string `json:"reason"`
}

func (h *DownloadHTTPHandler) Capabilities(w http.ResponseWriter, r *http.Request) {
	api.WriteData(w, r, http.StatusOK, h.service.Capabilities())
}

func (h *DownloadHTTPHandler) ListCategories(w http.ResponseWriter, r *http.Request) {
	items, err := h.service.ListCategories(r.Context())
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusOK, map[string]any{"items": items})
}

func (h *DownloadHTTPHandler) CreateCategory(w http.ResponseWriter, r *http.Request) {
	var request downloadCategoryRequest
	if !decodeDownloadRequest(w, r, &request) {
		return
	}
	item, err := h.service.CreateCategory(r.Context(), categoryInput(request), h.meta(r))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusCreated, item)
}

func (h *DownloadHTTPHandler) UpdateCategory(w http.ResponseWriter, r *http.Request) {
	var request downloadCategoryRequest
	if !decodeDownloadRequest(w, r, &request) {
		return
	}
	item, err := h.service.UpdateCategory(r.Context(), chi.URLParam(r, "category_id"), categoryInput(request), h.meta(r))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusOK, item)
}

func (h *DownloadHTTPHandler) ArchiveCategory(w http.ResponseWriter, r *http.Request) {
	h.categoryOperation(w, r, chi.URLParam(r, "category_id"))
}

func (h *DownloadHTTPHandler) categoryOperation(w http.ResponseWriter, r *http.Request, id string) {
	var request downloadOperationRequest
	if !decodeDownloadRequest(w, r, &request) {
		return
	}
	item, err := h.service.ArchiveCategory(r.Context(), id, request.Reason, h.meta(r))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusOK, item)
}

func (h *DownloadHTTPHandler) ListEntries(w http.ResponseWriter, r *http.Request) {
	items, err := h.service.ListEntries(r.Context())
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusOK, map[string]any{"items": items})
}

func (h *DownloadHTTPHandler) ListReleaseFiles(w http.ResponseWriter, r *http.Request) {
	items, err := h.service.ListReleaseFiles(r.Context())
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusOK, map[string]any{"items": items})
}

func (h *DownloadHTTPHandler) GetEntry(w http.ResponseWriter, r *http.Request) {
	item, err := h.service.GetEntry(r.Context(), chi.URLParam(r, "download_id"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusOK, item)
}

func (h *DownloadHTTPHandler) CreateEntry(w http.ResponseWriter, r *http.Request) {
	var request downloadEntryRequest
	if !decodeDownloadRequest(w, r, &request) {
		return
	}
	item, err := h.service.CreateEntry(r.Context(), entryInput(request), h.meta(r))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusCreated, item)
}

func (h *DownloadHTTPHandler) UpdateEntry(w http.ResponseWriter, r *http.Request) {
	var request downloadEntryRequest
	if !decodeDownloadRequest(w, r, &request) {
		return
	}
	item, err := h.service.UpdateEntry(r.Context(), chi.URLParam(r, "download_id"), entryInput(request), h.meta(r))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusOK, item)
}

func (h *DownloadHTTPHandler) ArchiveEntry(w http.ResponseWriter, r *http.Request) {
	var request downloadOperationRequest
	if !decodeDownloadRequest(w, r, &request) {
		return
	}
	item, err := h.service.ArchiveEntry(r.Context(), chi.URLParam(r, "download_id"), request.Reason, h.meta(r))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusOK, item)
}

func (h *DownloadHTTPHandler) CreateUpload(w http.ResponseWriter, r *http.Request) {
	var request downloadUploadRequest
	if !decodeDownloadRequest(w, r, &request) {
		return
	}
	item, err := h.service.CreateUpload(r.Context(), chi.URLParam(r, "download_id"), download.UploadInput{
		VersionLabel: request.VersionLabel, FileName: request.FileName, ContentType: request.ContentType,
		SizeBytes: request.SizeBytes, SHA256: request.SHA256, Reason: request.Reason,
	}, h.meta(r))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusCreated, item)
}

func (h *DownloadHTTPHandler) GetUpload(w http.ResponseWriter, r *http.Request) {
	item, err := h.service.GetUpload(r.Context(), chi.URLParam(r, "upload_id"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusOK, item)
}

func (h *DownloadHTTPHandler) SignParts(w http.ResponseWriter, r *http.Request) {
	var request downloadPartRequest
	if !decodeDownloadRequest(w, r, &request) {
		return
	}
	items, err := h.service.SignParts(r.Context(), chi.URLParam(r, "upload_id"), request.PartNumbers)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusOK, map[string]any{"items": items})
}

func (h *DownloadHTTPHandler) CompleteUpload(w http.ResponseWriter, r *http.Request) {
	h.uploadOperation(w, r, "complete")
}

func (h *DownloadHTTPHandler) AbortUpload(w http.ResponseWriter, r *http.Request) {
	h.uploadOperation(w, r, "abort")
}

func (h *DownloadHTTPHandler) uploadOperation(w http.ResponseWriter, r *http.Request, operation string) {
	var request downloadOperationRequest
	if !decodeDownloadRequest(w, r, &request) {
		return
	}
	var item download.UploadSession
	var err error
	if operation == "complete" {
		item, err = h.service.CompleteUpload(r.Context(), chi.URLParam(r, "upload_id"), request.Reason, h.meta(r))
	} else {
		item, err = h.service.AbortUpload(r.Context(), chi.URLParam(r, "upload_id"), request.Reason, h.meta(r))
	}
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusOK, item)
}

func (h *DownloadHTTPHandler) Publish(w http.ResponseWriter, r *http.Request) {
	h.versionOperation(w, r, "publish")
}

func (h *DownloadHTTPHandler) ArchiveVersion(w http.ResponseWriter, r *http.Request) {
	h.versionOperation(w, r, "archive")
}

func (h *DownloadHTTPHandler) versionOperation(w http.ResponseWriter, r *http.Request, operation string) {
	var request downloadOperationRequest
	if !decodeDownloadRequest(w, r, &request) {
		return
	}
	var item download.Version
	var err error
	if operation == "publish" {
		item, err = h.service.Publish(r.Context(), chi.URLParam(r, "version_id"), request.Reason, h.meta(r))
	} else {
		item, err = h.service.ArchiveVersion(r.Context(), chi.URLParam(r, "version_id"), request.Reason, h.meta(r))
	}
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusOK, item)
}

func (h *DownloadHTTPHandler) meta(r *http.Request) download.ActorMeta {
	principal := PrincipalFromContext(r.Context())
	adminID := ""
	if principal != nil {
		adminID = principal.AdminID
	}
	return download.ActorMeta{
		AdminID: adminID, RequestID: requestctx.RequestID(r.Context()),
		IPAddress: appmiddleware.ClientIP(r, h.trustProxy), UserAgent: r.UserAgent(),
	}
}

func (h *DownloadHTTPHandler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, message, details := download.ErrorDetails(err)
	if status >= 500 {
		h.logger.ErrorContext(r.Context(), "administrator download request failed", "code", code, "error", err)
	}
	api.WriteError(w, r, status, code, message, details)
}

func decodeDownloadRequest(w http.ResponseWriter, r *http.Request, target any) bool {
	if err := api.DecodeJSON(r, target); err != nil {
		api.WriteError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request.", nil)
		return false
	}
	return true
}

func categoryInput(request downloadCategoryRequest) download.CategoryInput {
	return download.CategoryInput{
		Slug: request.Slug, TitleEN: request.TitleEN, TitleZhCN: request.TitleZhCN,
		DescriptionEN: request.DescriptionEN, DescriptionZhCN: request.DescriptionZhCN,
		SortOrder: request.SortOrder, Enabled: request.Enabled, Reason: request.Reason,
	}
}

func entryInput(request downloadEntryRequest) download.EntryInput {
	return download.EntryInput{
		CategoryID: request.CategoryID, Slug: request.Slug,
		TitleEN: request.TitleEN, TitleZhCN: request.TitleZhCN,
		DescriptionEN: request.DescriptionEN, DescriptionZhCN: request.DescriptionZhCN,
		SortOrder: request.SortOrder, Reason: request.Reason,
	}
}
