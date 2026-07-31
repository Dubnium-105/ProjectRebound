package p2pbattlelog

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/api"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/auth"
	"github.com/go-chi/chi/v5"
)

const reportTokenHeader = "X-P2P-Report-Token"

type HTTPService interface {
	ActiveMatch(context.Context, Actor, string) (MatchSession, error)
	IssueCapability(context.Context, Actor, string) (CapabilityResult, error)
	UpdatePresence(context.Context, Actor, string, PresenceInput) (PresenceResult, error)
	SubmitReport(context.Context, Actor, string, string, string, []byte) (ReportResult, error)
	Result(context.Context, Actor, string) (FinalizedResult, error)
}

type HTTPHandler struct {
	service        HTTPService
	logger         *slog.Logger
	maxReportBytes int
}

func NewHTTPHandler(service HTTPService, logger *slog.Logger, maxReportBytes int) *HTTPHandler {
	return &HTTPHandler{service: service, logger: logger, maxReportBytes: maxReportBytes}
}

func (h *HTTPHandler) ActiveMatch(w http.ResponseWriter, r *http.Request) {
	match, err := h.service.ActiveMatch(r.Context(), actorFromRequest(r), chi.URLParam(r, "room_id"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusOK, matchResponse(match))
}

func (h *HTTPHandler) IssueCapability(w http.ResponseWriter, r *http.Request) {
	result, err := h.service.IssueCapability(r.Context(), actorFromRequest(r), chi.URLParam(r, "match_id"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusCreated, result)
}

func (h *HTTPHandler) Presence(w http.ResponseWriter, r *http.Request) {
	var input PresenceInput
	if err := api.DecodeJSON(r, &input); err != nil {
		api.WriteError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Invalid request.", map[string]any{"body": err.Error()})
		return
	}
	result, err := h.service.UpdatePresence(
		r.Context(), actorFromRequest(r), chi.URLParam(r, "match_id"), input,
	)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusOK, result)
}

func (h *HTTPHandler) SubmitReport(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type"))), "application/json") {
		api.WriteError(w, r, http.StatusUnsupportedMediaType, "CONTENT_TYPE_REQUIRED", "Content-Type must be application/json.", nil)
		return
	}
	contents, err := io.ReadAll(io.LimitReader(r.Body, int64(h.maxReportBytes)+1))
	if err != nil {
		api.WriteError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "Could not read report body.", nil)
		return
	}
	if len(contents) > h.maxReportBytes {
		api.WriteError(
			w, r, http.StatusRequestEntityTooLarge, "P2P_REPORT_TOO_LARGE",
			"The P2P BattleLog report exceeds the configured size limit.",
			map[string]any{"maximum_bytes": h.maxReportBytes},
		)
		return
	}
	result, err := h.service.SubmitReport(
		r.Context(), actorFromRequest(r), chi.URLParam(r, "match_id"),
		chi.URLParam(r, "report_id"), r.Header.Get(reportTokenHeader), contents,
	)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusOK, result)
}

func (h *HTTPHandler) Result(w http.ResponseWriter, r *http.Request) {
	result, err := h.service.Result(r.Context(), actorFromRequest(r), chi.URLParam(r, "match_id"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusOK, result)
}

func (h *HTTPHandler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	status, code, message, details := errorDetails(err)
	if status >= http.StatusInternalServerError {
		h.logger.ErrorContext(r.Context(), "P2P BattleLog request failed", "code", code, "error", err)
	}
	api.WriteError(w, r, status, code, message, details)
}

func actorFromRequest(r *http.Request) Actor {
	principal := auth.PrincipalFromContext(r.Context())
	if principal == nil {
		return Actor{}
	}
	return Actor{
		PlayerID: principal.Player.ID, SessionID: principal.SessionID,
		AuthLevel: principal.AuthLevel, SteamVerified: principal.SteamVerified,
	}
}

func matchResponse(match MatchSession) map[string]any {
	return map[string]any{
		"match_id": match.ID, "room_id": match.RoomIDSnapshot,
		"sequence": match.Sequence, "mode": match.Mode, "map_alias": match.MapAlias,
		"match_type": match.MatchType, "state": match.State,
		"roster_revision":         match.RosterRevision,
		"expected_reporter_count": match.ExpectedReporterCount,
		"policy_version":          match.PolicyVersion,
		"collection_started_at":   match.CollectionStartedAt,
		"collection_deadline":     match.CollectionDeadline,
		"hard_expires_at":         match.HardExpiresAt,
	}
}
