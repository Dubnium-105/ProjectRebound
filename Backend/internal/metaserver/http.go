package metaserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/projectrebound/matchserver/internal/api"
	"github.com/projectrebound/matchserver/internal/auth"
)

type HTTPHandler struct {
	service    *Service
	repository *Repository
	logger     *slog.Logger
}

func NewHTTPHandler(service *Service, repository *Repository, logger *slog.Logger) *HTTPHandler {
	return &HTTPHandler{service: service, repository: repository, logger: logger}
}

func (h *HTTPHandler) Root(w http.ResponseWriter, r *http.Request) {
	regions, err := h.service.Regions(r.Context())
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	servers := make([]map[string]any, 0)
	for _, region := range regions {
		for _, endpoint := range region.Endpoints {
			servers = append(servers, map[string]any{
				"location_id": 0, "region_id": region.ID,
				"ipv4": endpoint.Host, "ipv6": "", "port": endpoint.Port,
			})
		}
	}
	api.WriteData(w, r, http.StatusOK, map[string]any{
		"service": "project-rebound-meta-server", "protocol_version": h.service.ProtocolVersion(),
		"servers": servers,
	})
}

func (h *HTTPHandler) Regions(w http.ResponseWriter, r *http.Request) {
	items, err := h.service.Regions(r.Context())
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusOK, map[string]any{"items": items})
}

func (h *HTTPHandler) Playlists(w http.ResponseWriter, r *http.Request) {
	items, err := h.service.Playlists(r.Context())
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusOK, map[string]any{"items": items})
}

func (h *HTTPHandler) Notifications(w http.ResponseWriter, r *http.Request) {
	items, err := h.service.Notifications(r.Context(), r.URL.Query().Get("locale"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusOK, map[string]any{"items": items})
}

type sessionRequest struct {
	ClientVersion   string `json:"client_version"`
	Version         string `json:"version"`
	ProtocolVersion int    `json:"protocol_version"`
	Platform        string `json:"platform"`
	PlayerID        string `json:"playerId"`
	LoginToken      string `json:"loginToken"`
}

func (h *HTTPHandler) Session(w http.ResponseWriter, r *http.Request) {
	var input sessionRequest
	if err := decodeJSON(r, &input); err != nil {
		h.writeError(w, r, invalid(map[string]any{"body": err.Error()}))
		return
	}
	if input.ClientVersion == "" {
		input.ClientVersion = input.Version
	}
	if input.ProtocolVersion == 0 {
		input.ProtocolVersion = h.service.ProtocolVersion()
	}
	principal := auth.PrincipalFromContext(r.Context())
	ticket, err := h.service.IssueSession(
		r.Context(), principal.Player.ID, principal.SessionID,
		input.ClientVersion, input.ProtocolVersion,
	)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusCreated, map[string]any{
		"user_id": principal.Player.ID, "gate_ticket": ticket,
		"endpoint":           h.service.LogicEndpoint(),
		"expires_in_seconds": int(h.service.GateTicketTTL().Seconds()),
		"protocol_version":   h.service.ProtocolVersion(),
	})
}

// ConnectServer preserves the game's field names while deriving identity only
// from the authenticated control-plane principal. playerId and loginToken in
// the request are deliberately ignored.
func (h *HTTPHandler) ConnectServer(w http.ResponseWriter, r *http.Request) {
	var input sessionRequest
	if err := decodeJSON(r, &input); err != nil {
		h.writeError(w, r, invalid(map[string]any{"body": err.Error()}))
		return
	}
	if input.ClientVersion == "" {
		input.ClientVersion = input.Version
	}
	if input.ProtocolVersion == 0 {
		input.ProtocolVersion = h.service.ProtocolVersion()
	}
	principal := auth.PrincipalFromContext(r.Context())
	ticket, err := h.service.IssueSession(
		r.Context(), principal.Player.ID, principal.SessionID,
		input.ClientVersion, input.ProtocolVersion,
	)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": 0, "userId": principal.Player.ID, "aceId": principal.Player.ID,
		"gateToken": ticket, "endpoint": h.service.LogicEndpoint(),
	})
}

func (h *HTTPHandler) Profile(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	item, err := h.service.Profile(r.Context(), principal.Player.ID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusOK, item)
}

func (h *HTTPHandler) ListLoadouts(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	items, err := h.service.ListLoadouts(r.Context(), principal.Player.ID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusOK, map[string]any{"items": items})
}

func (h *HTTPHandler) GetLoadout(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	item, err := h.service.GetLoadout(r.Context(), principal.Player.ID, chi.URLParam(r, "role_id"))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusOK, item)
}

func (h *HTTPHandler) PutLoadout(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Snapshot json.RawMessage `json:"snapshot"`
		Revision int64           `json:"revision"`
	}
	if err := decodeJSON(r, &input); err != nil {
		h.writeError(w, r, invalid(map[string]any{"body": err.Error()}))
		return
	}
	principal := auth.PrincipalFromContext(r.Context())
	item, err := h.service.PutLoadout(
		r.Context(), principal.Player.ID, chi.URLParam(r, "role_id"),
		input.Snapshot, input.Revision,
	)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusOK, item)
}

func (h *HTTPHandler) CreateParty(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Mode          string `json:"mode"`
		Region        string `json:"region"`
		ClientVersion string `json:"client_version"`
	}
	if err := decodeJSON(r, &input); err != nil {
		h.writeError(w, r, invalid(map[string]any{"body": err.Error()}))
		return
	}
	principal := auth.PrincipalFromContext(r.Context())
	item, err := h.service.CreateParty(
		r.Context(), principal.Player.ID, input.Mode, input.Region, input.ClientVersion,
	)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusCreated, item)
}

func (h *HTTPHandler) GetParty(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	item, err := h.service.GetParty(r.Context(), chi.URLParam(r, "party_id"), principal.Player.ID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusOK, item)
}

func (h *HTTPHandler) Ready(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Ready bool `json:"ready"`
	}
	if err := decodeJSON(r, &input); err != nil {
		h.writeError(w, r, invalid(map[string]any{"body": err.Error()}))
		return
	}
	principal := auth.PrincipalFromContext(r.Context())
	item, err := h.service.SetReady(r.Context(), chi.URLParam(r, "party_id"), principal.Player.ID, input.Ready)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusOK, item)
}

func (h *HTTPHandler) Presence(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Presence string `json:"presence"`
	}
	if err := decodeJSON(r, &input); err != nil {
		h.writeError(w, r, invalid(map[string]any{"body": err.Error()}))
		return
	}
	principal := auth.PrincipalFromContext(r.Context())
	item, err := h.service.SetPresence(
		r.Context(), chi.URLParam(r, "party_id"), principal.Player.ID, input.Presence,
	)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusOK, item)
}

func (h *HTTPHandler) CreateMatchTicket(w http.ResponseWriter, r *http.Request) {
	var input struct {
		PartyID       string `json:"party_id"`
		Mode          string `json:"mode"`
		Region        string `json:"region"`
		ClientVersion string `json:"client_version"`
	}
	if err := decodeJSON(r, &input); err != nil {
		h.writeError(w, r, invalid(map[string]any{"body": err.Error()}))
		return
	}
	principal := auth.PrincipalFromContext(r.Context())
	item, err := h.service.CreateMatchTicket(
		r.Context(), principal.Player.ID, input.PartyID,
		input.Mode, input.Region, input.ClientVersion,
	)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusAccepted, item)
}

func (h *HTTPHandler) GetMatchTicket(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	item, err := h.repository.GetTicket(r.Context(), chi.URLParam(r, "ticket_id"), principal.Player.ID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusOK, item)
}

func (h *HTTPHandler) CancelMatchTicket(w http.ResponseWriter, r *http.Request) {
	principal := auth.PrincipalFromContext(r.Context())
	if err := h.repository.CancelTicket(r.Context(), chi.URLParam(r, "ticket_id"), principal.Player.ID); err != nil {
		h.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type gameServerPrincipalKey uint8

const gameServerKey gameServerPrincipalKey = 0

func (h *HTTPHandler) RequireGameServer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			h.writeError(w, r, forbidden("META_GAME_SERVER_UNAUTHORIZED", "Game Server authentication failed."))
			return
		}
		principal, err := h.repository.AuthenticateGameServer(
			r.Context(), strings.TrimSpace(r.Header.Get("X-Game-Server-Id")),
			strings.TrimSpace(strings.TrimPrefix(header, "Bearer ")),
		)
		if err != nil {
			h.writeError(w, r, err)
			return
		}
		ctx := context.WithValue(r.Context(), gameServerKey, principal)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func gameServerFromRequest(r *http.Request) GameServerPrincipal {
	principal, _ := r.Context().Value(gameServerKey).(GameServerPrincipal)
	return principal
}

func (h *HTTPHandler) InternalLoadout(w http.ResponseWriter, r *http.Request) {
	item, err := h.repository.GetMatchPlayerLoadout(
		r.Context(), gameServerFromRequest(r),
		chi.URLParam(r, "match_id"), chi.URLParam(r, "player_id"),
	)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	api.WriteData(w, r, http.StatusOK, item)
}

func (h *HTTPHandler) InternalConnected(w http.ResponseWriter, r *http.Request) {
	err := h.repository.MarkMatchPlayerConnected(
		r.Context(), gameServerFromRequest(r),
		chi.URLParam(r, "match_id"), chi.URLParam(r, "player_id"),
	)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *HTTPHandler) InternalCompleted(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Result json.RawMessage `json:"result"`
	}
	if err := decodeJSON(r, &input); err != nil {
		h.writeError(w, r, invalid(map[string]any{"body": err.Error()}))
		return
	}
	if len(input.Result) == 0 {
		input.Result = json.RawMessage(`{}`)
	}
	var object map[string]any
	if err := json.Unmarshal(input.Result, &object); err != nil || object == nil {
		h.writeError(w, r, invalid(map[string]any{"result": "must be a JSON object"}))
		return
	}
	if err := h.repository.CompleteMatch(
		r.Context(), gameServerFromRequest(r), chi.URLParam(r, "match_id"), input.Result,
	); err != nil {
		h.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func decodeJSON(r *http.Request, target any) error {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain one JSON value")
	}
	return nil
}

func (h *HTTPHandler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var serviceErr *ServiceError
	if !errors.As(err, &serviceErr) {
		serviceErr = &ServiceError{
			Status: http.StatusInternalServerError, Code: "META_INTERNAL_ERROR",
			Message: "The MetaServer request could not be completed.", Err: err,
		}
	}
	if serviceErr.Status >= 500 {
		h.logger.ErrorContext(
			r.Context(), "MetaServer request failed",
			"code", serviceErr.Code, "error_class", safeErrorClass(serviceErr.Err),
		)
	}
	api.WriteError(w, r, serviceErr.Status, serviceErr.Code, serviceErr.Message, serviceErr.Details)
}
