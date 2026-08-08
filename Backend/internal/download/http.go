package download

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/api"
	"github.com/go-chi/chi/v5"
)

type PublicHTTPService interface {
	Catalog(context.Context) (Catalog, error)
	DownloadURL(context.Context, string) (string, error)
}

type HTTPHandler struct {
	service PublicHTTPService
	logger  *slog.Logger
}

func NewHTTPHandler(service PublicHTTPService, logger *slog.Logger) *HTTPHandler {
	return &HTTPHandler{service: service, logger: logger}
}

func (h *HTTPHandler) Catalog(w http.ResponseWriter, r *http.Request) {
	catalog, err := h.service.Catalog(r.Context())
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	representation, _ := json.Marshal(struct {
		Categories []PublicCategory `json:"categories"`
		Items      []PublicEntry    `json:"items"`
	}{Categories: catalog.Categories, Items: catalog.Items})
	etagHash := sha256.Sum256(representation)
	// The response envelope carries a per-request request_id, so this is a weak
	// validator for the stable catalog data rather than the complete wire bytes.
	etag := `W/"` + hex.EncodeToString(etagHash[:12]) + `"`
	w.Header().Set("Cache-Control", "public, max-age=60, stale-while-revalidate=300")
	w.Header().Set("ETag", etag)
	w.Header().Set("Last-Modified", catalog.UpdatedAt.UTC().Format(http.TimeFormat))
	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	api.WriteData(w, r, http.StatusOK, map[string]any{
		"categories": catalog.Categories,
		"items":      catalog.Items,
	})
}

func etagMatches(header, etag string) bool {
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || candidate == etag {
			return true
		}
	}
	return false
}

func (h *HTTPHandler) File(w http.ResponseWriter, r *http.Request) {
	target, err := h.service.DownloadURL(r.Context(), chi.URLParam(r, "version_id"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	// Do not cache the redirect: every download must re-check publication state so
	// archiving takes effect immediately. The target object itself is immutable.
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, target, http.StatusFound)
}

func (h *HTTPHandler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, message, details := ErrorDetails(err)
	if status >= 500 {
		h.logger.ErrorContext(r.Context(), "download request failed", "code", code, "error", err)
	}
	api.WriteError(w, r, status, code, message, details)
}
