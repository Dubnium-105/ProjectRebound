package metaserver

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/admin"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/api"
	appmiddleware "github.com/Dubnium-105/ProjectRebound/Backend/internal/middleware"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/requestctx"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

type MetaAdminHandler struct {
	repository *Repository
	service    *Service
	audits     *admin.Repository
	logger     *slog.Logger
	trustProxy bool
}

func NewMetaAdminHandler(
	repository *Repository,
	service *Service,
	logger *slog.Logger,
	trustProxy bool,
) *MetaAdminHandler {
	return &MetaAdminHandler{
		repository: repository, service: service, audits: admin.NewRepository(),
		logger: logger, trustProxy: trustProxy,
	}
}

func (h *MetaAdminHandler) Overview(w http.ResponseWriter, r *http.Request) {
	var result struct {
		Profiles, ActiveParties, QueuedTickets, ActiveMatches int64
	}
	err := h.repository.pool.QueryRow(r.Context(), `
		SELECT
			(SELECT COUNT(*) FROM meta_player_profiles),
			(SELECT COUNT(*) FROM meta_parties WHERE state <> 'CLOSED'),
			(SELECT COUNT(*) FROM meta_match_tickets WHERE state = 'QUEUED'),
			(SELECT COUNT(*) FROM meta_matches WHERE state IN ('RESERVED', 'RUNNING'))
	`).Scan(&result.Profiles, &result.ActiveParties, &result.QueuedTickets, &result.ActiveMatches)
	if err != nil {
		h.writeError(w, r, internalError(err))
		return
	}
	api.WriteData(w, r, http.StatusOK, map[string]int64{
		"profiles": result.Profiles, "active_parties": result.ActiveParties,
		"queued_tickets": result.QueuedTickets, "active_matches": result.ActiveMatches,
	})
}

func (h *MetaAdminHandler) PlayerLoadouts(w http.ResponseWriter, r *http.Request) {
	items, err := h.repository.ListLoadouts(r.Context(), chi.URLParam(r, "player_id"))
	if err != nil {
		h.writeError(w, r, internalError(err))
		return
	}
	api.WriteData(w, r, http.StatusOK, map[string]any{"items": items})
}

func (h *MetaAdminHandler) PutPlayerLoadout(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Snapshot json.RawMessage `json:"snapshot"`
		Revision int64           `json:"revision"`
		Reason   string          `json:"reason"`
	}
	if err := decodeJSON(r, &input); err != nil {
		h.writeError(w, r, invalid(map[string]any{"body": err.Error()}))
		return
	}
	reason, err := validateAdminReason(input.Reason)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	playerID, roleID := chi.URLParam(r, "player_id"), chi.URLParam(r, "role_id")
	canonical, digest, err := h.service.prepareLoadout(roleID, input.Snapshot, input.Revision)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	tx, err := h.repository.pool.BeginTx(r.Context(), pgx.TxOptions{})
	if err != nil {
		h.writeError(w, r, internalError(err))
		return
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(r.Context())) }()
	old, oldErr := h.repository.getLoadout(r.Context(), tx, playerID, roleID)
	if oldErr != nil {
		var serviceErr *ServiceError
		if !errors.As(oldErr, &serviceErr) || serviceErr.Code != "META_LOADOUT_NOT_FOUND" {
			h.writeError(w, r, oldErr)
			return
		}
	}
	item, err := h.repository.putLoadout(
		r.Context(), tx, playerID, roleID, canonical, digest, input.Revision,
	)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	oldValue := map[string]any{"exists": false}
	if oldErr == nil {
		oldValue = map[string]any{"revision": old.Revision}
	}
	if err := h.insertAudit(
		r, tx, "META_LOADOUT_UPDATED", "meta_loadout", playerID+":"+roleID,
		oldValue, map[string]any{"revision": item.Revision}, reason,
	); err != nil {
		h.writeError(w, r, internalError(err))
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		h.writeError(w, r, internalError(err))
		return
	}
	api.WriteData(w, r, http.StatusOK, item)
}

func (h *MetaAdminHandler) ListMatches(w http.ResponseWriter, r *http.Request) {
	rows, err := h.repository.pool.Query(r.Context(), `
		SELECT id, game_server_id, ticket_id, mode, region, state,
		       endpoint_host || ':' || endpoint_port::text,
		       reserved_at, started_at, completed_at, updated_at
		FROM meta_matches
		ORDER BY updated_at DESC
		LIMIT 100
	`)
	if err != nil {
		h.writeError(w, r, internalError(err))
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id, serverID, ticketID, mode, region, state, endpoint string
		var reservedAt, updatedAt time.Time
		var startedAt, completedAt *time.Time
		if err := rows.Scan(
			&id, &serverID, &ticketID, &mode, &region, &state, &endpoint,
			&reservedAt, &startedAt, &completedAt, &updatedAt,
		); err != nil {
			h.writeError(w, r, internalError(err))
			return
		}
		items = append(items, map[string]any{
			"id": id, "game_server_id": serverID, "ticket_id": ticketID,
			"mode": mode, "region": region, "state": state, "endpoint": endpoint,
			"reserved_at": reservedAt, "started_at": startedAt,
			"completed_at": completedAt, "updated_at": updatedAt,
		})
	}
	api.WriteData(w, r, http.StatusOK, map[string]any{"items": items})
}

func (h *MetaAdminHandler) CancelMatch(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Reason string `json:"reason"`
	}
	if err := decodeJSON(r, &input); err != nil {
		h.writeError(w, r, invalid(map[string]any{"body": err.Error()}))
		return
	}
	reason, err := validateAdminReason(input.Reason)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	matchID := chi.URLParam(r, "match_id")
	tx, err := h.repository.pool.BeginTx(r.Context(), pgx.TxOptions{})
	if err != nil {
		h.writeError(w, r, internalError(err))
		return
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(r.Context())) }()
	var serverID, ticketID, state, partyID string
	err = tx.QueryRow(r.Context(), `
		SELECT match.game_server_id, match.ticket_id, match.state, COALESCE(ticket.party_id, '')
		FROM meta_matches AS match
		JOIN meta_match_tickets AS ticket ON ticket.id = match.ticket_id
		WHERE match.id = $1
		FOR UPDATE OF match
	`, matchID).Scan(&serverID, &ticketID, &state, &partyID)
	if errors.Is(err, pgx.ErrNoRows) {
		h.writeError(w, r, notFound("META_MATCH_NOT_FOUND", "Match not found."))
		return
	}
	if err != nil {
		h.writeError(w, r, internalError(err))
		return
	}
	if state != "RESERVED" && state != "RUNNING" {
		h.writeError(w, r, conflict("META_MATCH_NOT_CANCELLABLE", "The match is not active."))
		return
	}
	now := time.Now().UTC()
	if _, err = tx.Exec(r.Context(), `
		UPDATE meta_matches SET state = 'CANCELLED', completed_at = $2, updated_at = $2 WHERE id = $1
	`, matchID, now); err == nil {
		_, err = tx.Exec(r.Context(), `
			UPDATE meta_match_tickets
			SET state = 'FAILED', failure_code = 'META_MATCH_CANCELLED_BY_ADMIN',
			    completed_at = $2, updated_at = $2
			WHERE id = $1 AND state = 'MATCHED'
		`, ticketID, now)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `
			UPDATE game_servers SET state = 'READY', updated_at = $2
			WHERE id = $1 AND state IN ('RESERVED', 'RUNNING')
		`, serverID, now)
	}
	if err == nil && partyID != "" {
		_, err = tx.Exec(r.Context(), `
			UPDATE meta_parties
			SET state = 'ACTIVE', revision = revision + 1, updated_at = $2
			WHERE id = $1 AND state = 'IN_MATCH'
		`, partyID, now)
	}
	if err == nil {
		err = h.insertAudit(r, tx, "META_MATCH_CANCELLED", "meta_match", matchID,
			map[string]any{"state": state}, map[string]any{"state": "CANCELLED"}, reason)
	}
	if err != nil {
		h.writeError(w, r, internalError(err))
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		h.writeError(w, r, internalError(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *MetaAdminHandler) UpsertPlaylist(w http.ResponseWriter, r *http.Request) {
	var input struct {
		DisplayName string          `json:"display_name"`
		Description string          `json:"description"`
		Mode        string          `json:"mode"`
		Definition  json.RawMessage `json:"definition"`
		Enabled     bool            `json:"enabled"`
		SortOrder   int             `json:"sort_order"`
		Reason      string          `json:"reason"`
	}
	if err := decodeJSON(r, &input); err != nil {
		h.writeError(w, r, invalid(map[string]any{"body": err.Error()}))
		return
	}
	reason, err := validateAdminReason(input.Reason)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	slug := chi.URLParam(r, "slug")
	if !regexpPlaylistSlug.MatchString(slug) || strings.TrimSpace(input.DisplayName) == "" ||
		!metaLabelPattern.MatchString(input.Mode) || !jsonObject(input.Definition) {
		h.writeError(w, r, invalid(map[string]any{"playlist": "invalid playlist content"}))
		return
	}
	tx, err := h.repository.pool.BeginTx(r.Context(), pgx.TxOptions{})
	if err != nil {
		h.writeError(w, r, internalError(err))
		return
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(r.Context())) }()
	principal := admin.PrincipalFromContext(r.Context())
	id := newMetaID("mpl_")
	var item Playlist
	err = tx.QueryRow(r.Context(), `
		INSERT INTO meta_playlists (
			id, slug, display_name, description, mode, definition, enabled,
			sort_order, created_by, updated_by, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7, $8, $9, $9, $10, $10)
		ON CONFLICT (slug) DO UPDATE SET
			display_name = EXCLUDED.display_name, description = EXCLUDED.description,
			mode = EXCLUDED.mode, definition = EXCLUDED.definition,
			enabled = EXCLUDED.enabled, sort_order = EXCLUDED.sort_order,
			updated_by = EXCLUDED.updated_by, updated_at = EXCLUDED.updated_at
		RETURNING id, slug, display_name, description, mode, definition, sort_order, updated_at
	`, id, slug, input.DisplayName, input.Description, input.Mode, input.Definition,
		input.Enabled, input.SortOrder, principal.AdminID, time.Now().UTC(),
	).Scan(
		&item.ID, &item.Slug, &item.DisplayName, &item.Description,
		&item.Mode, &item.Definition, &item.SortOrder, &item.UpdatedAt,
	)
	if err == nil {
		err = h.insertAudit(r, tx, "META_PLAYLIST_UPSERTED", "meta_playlist", item.ID,
			map[string]any{}, map[string]any{"slug": slug, "enabled": input.Enabled}, reason)
	}
	if err != nil {
		h.writeError(w, r, internalError(err))
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		h.writeError(w, r, internalError(err))
		return
	}
	api.WriteData(w, r, http.StatusOK, item)
}

func (h *MetaAdminHandler) UpsertNotification(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Title    string     `json:"title"`
		Body     string     `json:"body"`
		Locale   string     `json:"locale"`
		Priority int        `json:"priority"`
		Enabled  bool       `json:"enabled"`
		StartsAt *time.Time `json:"starts_at"`
		EndsAt   *time.Time `json:"ends_at"`
		Reason   string     `json:"reason"`
	}
	if err := decodeJSON(r, &input); err != nil {
		h.writeError(w, r, invalid(map[string]any{"body": err.Error()}))
		return
	}
	reason, err := validateAdminReason(input.Reason)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	input.Title, input.Body, input.Locale = strings.TrimSpace(input.Title), strings.TrimSpace(input.Body), strings.TrimSpace(input.Locale)
	if input.Title == "" || len(input.Title) > 256 || input.Body == "" || len(input.Locale) > 16 ||
		(input.StartsAt != nil && input.EndsAt != nil && !input.StartsAt.Before(*input.EndsAt)) {
		h.writeError(w, r, invalid(map[string]any{"notification": "invalid notification content or time window"}))
		return
	}
	if input.Locale == "" {
		input.Locale = "en"
	}
	id := chi.URLParam(r, "notification_id")
	if id == "new" {
		id = newMetaID("mn_")
	}
	if !metaLabelPattern.MatchString(id) {
		h.writeError(w, r, invalid(map[string]any{"notification_id": "is invalid"}))
		return
	}
	tx, err := h.repository.pool.BeginTx(r.Context(), pgx.TxOptions{})
	if err != nil {
		h.writeError(w, r, internalError(err))
		return
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(r.Context())) }()
	principal := admin.PrincipalFromContext(r.Context())
	var item Notification
	err = tx.QueryRow(r.Context(), `
		INSERT INTO meta_notifications (
			id, title, body, locale, priority, enabled, starts_at, ends_at,
			created_by, updated_by, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $9, $10, $10)
		ON CONFLICT (id) DO UPDATE SET
			title = EXCLUDED.title, body = EXCLUDED.body, locale = EXCLUDED.locale,
			priority = EXCLUDED.priority, enabled = EXCLUDED.enabled,
			starts_at = EXCLUDED.starts_at, ends_at = EXCLUDED.ends_at,
			updated_by = EXCLUDED.updated_by, updated_at = EXCLUDED.updated_at
		RETURNING id, title, body, locale, priority, starts_at, ends_at, updated_at
	`, id, input.Title, input.Body, input.Locale, input.Priority, input.Enabled,
		input.StartsAt, input.EndsAt, principal.AdminID, time.Now().UTC(),
	).Scan(
		&item.ID, &item.Title, &item.Body, &item.Locale, &item.Priority,
		&item.StartsAt, &item.EndsAt, &item.UpdatedAt,
	)
	if err == nil {
		err = h.insertAudit(r, tx, "META_NOTIFICATION_UPSERTED", "meta_notification", item.ID,
			map[string]any{}, map[string]any{"locale": input.Locale, "enabled": input.Enabled}, reason)
	}
	if err != nil {
		h.writeError(w, r, internalError(err))
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		h.writeError(w, r, internalError(err))
		return
	}
	api.WriteData(w, r, http.StatusOK, item)
}

func (h *MetaAdminHandler) insertAudit(
	r *http.Request,
	tx pgx.Tx,
	action, targetType, targetID string,
	oldValue, newValue map[string]any,
	reason string,
) error {
	principal := admin.PrincipalFromContext(r.Context())
	adminID := ""
	if principal != nil {
		adminID = principal.AdminID
	}
	return h.audits.InsertAudit(r.Context(), tx, admin.AuditLog{
		ID: newMetaID("audit_"), AdminID: adminID, Action: action,
		TargetType: targetType, TargetID: targetID,
		OldValue: oldValue, NewValue: newValue, Reason: reason,
		RequestID: requestctx.RequestID(r.Context()),
		IPAddress: appmiddleware.ClientIP(r, h.trustProxy),
		UserAgent: r.UserAgent(), Result: "SUCCEEDED", CreatedAt: time.Now().UTC(),
	})
}

func validateAdminReason(value string) (string, error) {
	value = strings.TrimSpace(value)
	lower := strings.ToLower(value)
	if len(value) < 8 || len(value) > 1000 ||
		strings.Contains(lower, "authorization:") || strings.Contains(lower, "bearer ") ||
		strings.Contains(lower, "password=") || strings.Contains(lower, "token=") {
		return "", invalid(map[string]any{"reason": "must be 8-1000 characters and must not contain credentials"})
	}
	return value, nil
}

func jsonObject(value json.RawMessage) bool {
	var object map[string]any
	return json.Unmarshal(value, &object) == nil && object != nil
}

var regexpPlaylistSlug = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

func (h *MetaAdminHandler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var serviceErr *ServiceError
	if !errors.As(err, &serviceErr) {
		serviceErr = &ServiceError{
			Status: http.StatusInternalServerError, Code: "META_INTERNAL_ERROR",
			Message: "The MetaServer request could not be completed.", Err: err,
		}
	}
	if serviceErr.Status >= 500 {
		h.logger.ErrorContext(
			r.Context(), "MetaServer admin request failed",
			"code", serviceErr.Code, "error_class", safeErrorClass(serviceErr.Err),
		)
	}
	api.WriteError(w, r, serviceErr.Status, serviceErr.Code, serviceErr.Message, serviceErr.Details)
}
