package update

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/projectrebound/matchserver/internal/api"
)

type HTTPService interface {
	Check(context.Context, CheckInput) (CheckResult, error)
	Manifest(context.Context, string, string, string, string) (Manifest, error)
	File(context.Context, string) (FileDownload, error)
	ClientConfig(context.Context) (ClientConfig, error)
}

type HTTPHandler struct {
	service HTTPService
	logger  *slog.Logger
}

func NewHTTPHandler(service HTTPService, logger *slog.Logger) *HTTPHandler {
	return &HTTPHandler{service: service, logger: logger}
}

func (h *HTTPHandler) Check(w http.ResponseWriter, r *http.Request) {
	result, err := h.service.Check(r.Context(), CheckInput{
		Platform: r.URL.Query().Get("platform"), Architecture: r.URL.Query().Get("architecture"),
		Channel: r.URL.Query().Get("channel"), Version: r.URL.Query().Get("current_version"),
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusOK, result)
}

func (h *HTTPHandler) Manifest(w http.ResponseWriter, r *http.Request) {
	result, err := h.service.Manifest(
		r.Context(), chi.URLParam(r, "platform"), r.URL.Query().Get("architecture"),
		r.URL.Query().Get("channel"), chi.URLParam(r, "version"),
	)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusOK, result)
}

func (h *HTTPHandler) File(w http.ResponseWriter, r *http.Request) {
	result, err := h.service.File(r.Context(), strings.TrimSpace(chi.URLParam(r, "file_id")))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusOK, result)
}

func (h *HTTPHandler) ClientConfig(w http.ResponseWriter, r *http.Request) {
	result, err := h.service.ClientConfig(r.Context())
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusOK, result)
}

func (h *HTTPHandler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, message, details := errorDetails(err)
	if status >= http.StatusInternalServerError {
		h.logger.ErrorContext(r.Context(), "update request failed", "code", code, "error", err)
	}
	api.WriteError(w, r, status, code, message, details)
}
