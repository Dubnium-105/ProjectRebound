package p2pbattlelog

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/api"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

type AdminHTTPService interface {
	MatchEvidence(context.Context, string) (AdminMatchEvidence, error)
	RawEvidence(context.Context, string) (AdminRawEvidence, error)
}

type AdminHTTPHandler struct {
	service AdminHTTPService
	logger  *slog.Logger
}

func NewAdminHTTPHandler(service AdminHTTPService, logger *slog.Logger) *AdminHTTPHandler {
	return &AdminHTTPHandler{service: service, logger: logger}
}

func (h *AdminHTTPHandler) MatchEvidence(w http.ResponseWriter, r *http.Request) {
	result, err := h.service.MatchEvidence(r.Context(), chi.URLParam(r, "match_id"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusOK, result)
}

func (h *AdminHTTPHandler) RawEvidence(w http.ResponseWriter, r *http.Request) {
	result, err := h.service.RawEvidence(r.Context(), chi.URLParam(r, "evidence_id"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	api.WriteData(w, r, http.StatusOK, result)
}

func (h *AdminHTTPHandler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	if err == pgx.ErrNoRows {
		api.WriteError(w, r, http.StatusNotFound, "P2P_BATTLELOG_EVIDENCE_NOT_FOUND", "P2P BattleLog evidence was not found.", nil)
		return
	}
	h.logger.ErrorContext(r.Context(), "administrative P2P BattleLog read failed", "error", err)
	api.WriteError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal server error.", nil)
}
