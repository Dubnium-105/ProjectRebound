package admin

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/auth"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/player"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PlayerRepository interface {
	List(context.Context, player.Queryer, string, player.AccountStatus, int) ([]player.Player, error)
	GetByID(context.Context, player.Queryer, string) (player.Player, error)
	UpdateAdministrativeFields(context.Context, pgx.Tx, string, player.AdministrativePatch, time.Time) (player.Player, player.Player, error)
}

type SessionRevoker interface {
	RevokePlayerSessions(context.Context, auth.Executor, string, time.Time, string) (int64, error)
}

type Service struct {
	pool       *pgxpool.Pool
	players    PlayerRepository
	sessions   SessionRevoker
	repository *Repository
	now        func() time.Time
}

func NewService(pool *pgxpool.Pool, players PlayerRepository, sessions SessionRevoker, repository *Repository) *Service {
	return &Service{pool: pool, players: players, sessions: sessions, repository: repository, now: time.Now}
}

func (s *Service) ListPlayers(ctx context.Context, cursor, status string, limit int) (ListResult, error) {
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 100 {
		return ListResult{}, &ServiceError{Status: http.StatusBadRequest, Code: "INVALID_REQUEST", Message: "Invalid limit.", Details: map[string]any{"limit": "must be between 1 and 100"}}
	}
	accountStatus, err := parseAccountStatus(status, true)
	if err != nil {
		return ListResult{}, err
	}
	items, err := s.players.List(ctx, s.pool, strings.TrimSpace(cursor), accountStatus, limit+1)
	if err != nil {
		return ListResult{}, &ServiceError{Status: 500, Code: "INTERNAL_ERROR", Message: "Internal server error.", Cause: err}
	}
	nextCursor := ""
	if len(items) > limit {
		nextCursor = items[limit-1].ID
		items = items[:limit]
	}
	return ListResult{Items: items, NextCursor: nextCursor}, nil
}

func (s *Service) GetPlayer(ctx context.Context, playerID string) (player.Player, error) {
	if strings.TrimSpace(playerID) == "" {
		return player.Player{}, &ServiceError{Status: 400, Code: "INVALID_REQUEST", Message: "Invalid player ID."}
	}
	item, err := s.players.GetByID(ctx, s.pool, playerID)
	if err != nil {
		return player.Player{}, mapPlayerError(err)
	}
	return item, nil
}

func (s *Service) PatchPlayer(ctx context.Context, playerID string, patch PlayerPatch, meta RequestMeta) (PatchResult, error) {
	if patch.AccountStatus == nil && patch.IsVIP == nil {
		return PatchResult{}, &ServiceError{Status: 400, Code: "INVALID_REQUEST", Message: "At least one player field is required."}
	}
	if patch.AccountStatus != nil {
		normalized, err := parseAccountStatus(string(*patch.AccountStatus), false)
		if err != nil {
			return PatchResult{}, err
		}
		patch.AccountStatus = &normalized
	}
	reason, err := validateAuditReason(patch.Reason)
	if err != nil {
		return PatchResult{}, err
	}
	internalNote, err := validateInternalNote(patch.InternalNote)
	if err != nil {
		return PatchResult{}, err
	}
	meta = sanitizeMeta(meta)
	if meta.AdminID == "" {
		return PatchResult{}, &ServiceError{Status: 401, Code: "ADMIN_UNAUTHORIZED", Message: "Administrator authentication is required."}
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return PatchResult{}, internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	now := s.now().UTC()
	oldValue, newValue, err := s.players.UpdateAdministrativeFields(ctx, tx, playerID, player.AdministrativePatch{
		AccountStatus: patch.AccountStatus,
		IsVIP:         patch.IsVIP,
	}, now)
	if err != nil {
		return PatchResult{}, mapPlayerError(err)
	}
	var revoked int64
	if patch.RevokeSessions {
		revoked, err = s.sessions.RevokePlayerSessions(ctx, tx, playerID, now, "ADMIN_REVOKED")
		if err != nil {
			return PatchResult{}, internal(err)
		}
	}
	if err := s.repository.InsertAudit(ctx, tx, AuditLog{
		ID:         newID("ada_"),
		AdminID:    meta.AdminID,
		Action:     "PLAYER_UPDATED",
		TargetType: "player",
		TargetID:   playerID,
		OldValue: map[string]any{
			"account_status": oldValue.AccountStatus,
			"is_vip":         oldValue.IsVIP,
		},
		NewValue: map[string]any{
			"account_status":   newValue.AccountStatus,
			"is_vip":           newValue.IsVIP,
			"revoked_sessions": revoked,
			"internal_note":    internalNote,
		},
		Reason:    reason,
		RequestID: meta.RequestID,
		IPAddress: meta.IPAddress,
		UserAgent: meta.UserAgent,
		Result:    "SUCCEEDED",
		CreatedAt: now,
	}); err != nil {
		return PatchResult{}, internal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return PatchResult{}, internal(fmt.Errorf("commit admin player update: %w", err))
	}
	return PatchResult{Player: newValue, RevokedSessions: revoked}, nil
}

func (s *Service) RevokePlayerSessions(ctx context.Context, playerID, reasonInput string, meta RequestMeta) (int64, error) {
	reason, err := validateAuditReason(reasonInput)
	if err != nil {
		return 0, err
	}
	meta = sanitizeMeta(meta)
	if meta.AdminID == "" {
		return 0, &ServiceError{Status: 401, Code: "ADMIN_UNAUTHORIZED", Message: "Administrator authentication is required."}
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return 0, internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := s.players.GetByID(ctx, tx, playerID); err != nil {
		return 0, mapPlayerError(err)
	}
	now := s.now().UTC()
	revoked, err := s.sessions.RevokePlayerSessions(ctx, tx, playerID, now, "ADMIN_REVOKED")
	if err != nil {
		return 0, internal(err)
	}
	if err := s.repository.InsertAudit(ctx, tx, AuditLog{
		ID:         newID("ada_"),
		AdminID:    meta.AdminID,
		Action:     "PLAYER_SESSIONS_REVOKED",
		TargetType: "player",
		TargetID:   playerID,
		OldValue:   map[string]any{},
		NewValue:   map[string]any{"revoked_sessions": revoked},
		Reason:     reason,
		RequestID:  meta.RequestID,
		IPAddress:  meta.IPAddress,
		UserAgent:  meta.UserAgent,
		Result:     "SUCCEEDED",
		CreatedAt:  now,
	}); err != nil {
		return 0, internal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, internal(fmt.Errorf("commit admin session revocation: %w", err))
	}
	return revoked, nil
}

func validateAuditReason(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", &ServiceError{
			Status:  http.StatusBadRequest,
			Code:    "INVALID_REQUEST",
			Message: "An operation reason is required.",
			Details: map[string]any{"reason": "is required"},
		}
	}
	if len([]rune(value)) > 500 {
		return "", &ServiceError{
			Status:  http.StatusBadRequest,
			Code:    "INVALID_REQUEST",
			Message: "The operation reason is too long.",
			Details: map[string]any{"reason": "must contain at most 500 characters"},
		}
	}
	lower := strings.ToLower(value)
	for _, marker := range []string{"authorization:", "bearer ", "password=", "token=", "secret=", "cookie="} {
		if strings.Contains(lower, marker) {
			return "", &ServiceError{
				Status:  http.StatusBadRequest,
				Code:    "SENSITIVE_AUDIT_TEXT",
				Message: "Operation reasons must not contain passwords, tokens, cookies, or other credentials.",
				Details: map[string]any{"reason": "contains credential-like text"},
			}
		}
	}
	return value, nil
}

func validateInternalNote(value string) (string, error) {
	value = strings.TrimSpace(value)
	if len([]rune(value)) > 2000 {
		return "", &ServiceError{
			Status:  http.StatusBadRequest,
			Code:    "INVALID_REQUEST",
			Message: "The internal note is too long.",
			Details: map[string]any{"internal_note": "must contain at most 2000 characters"},
		}
	}
	return value, nil
}

func parseAccountStatus(value string, allowEmpty bool) (player.AccountStatus, error) {
	status := player.AccountStatus(strings.ToUpper(strings.TrimSpace(value)))
	if status == "" && allowEmpty {
		return "", nil
	}
	switch status {
	case player.AccountStatusActive, player.AccountStatusBanned, player.AccountStatusDeleted:
		return status, nil
	default:
		return "", &ServiceError{Status: 400, Code: "INVALID_REQUEST", Message: "Invalid account status.", Details: map[string]any{"account_status": "must be ACTIVE, BANNED, or DELETED"}}
	}
}

func mapPlayerError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return &ServiceError{Status: 404, Code: "PLAYER_NOT_FOUND", Message: "Player not found."}
	}
	return internal(err)
}

func internal(err error) error {
	return &ServiceError{Status: 500, Code: "INTERNAL_ERROR", Message: "Internal server error.", Cause: err}
}

func sanitizeMeta(meta RequestMeta) RequestMeta {
	meta.AdminID = truncate(strings.TrimSpace(meta.AdminID), 128)
	meta.RequestID = truncate(strings.TrimSpace(meta.RequestID), 128)
	meta.UserAgent = truncate(strings.TrimSpace(meta.UserAgent), 512)
	if net.ParseIP(meta.IPAddress) == nil {
		meta.IPAddress = ""
	}
	return meta
}

func truncate(value string, maximum int) string {
	runes := []rune(value)
	if len(runes) <= maximum {
		return value
	}
	return string(runes[:maximum])
}

func newID(prefix string) string {
	return prefix + strings.ReplaceAll(uuid.NewString(), "-", "")
}
