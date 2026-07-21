package invite

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/projectrebound/matchserver/internal/admin"
)

type Service struct {
	pool       *pgxpool.Pool
	repository *Repository
	audits     *admin.Repository
	now        func() time.Time
}

func NewService(pool *pgxpool.Pool, repository *Repository, audits *admin.Repository) *Service {
	return &Service{pool: pool, repository: repository, audits: audits, now: time.Now}
}

func (s *Service) Create(ctx context.Context, input CreateInput, meta RequestMeta) (CreateResult, error) {
	meta = sanitizeMeta(meta)
	if meta.AdminID == "" {
		return CreateResult{}, unauthorized()
	}
	input.BatchName = strings.TrimSpace(input.BatchName)
	if input.BatchName == "" || len(input.BatchName) > 128 {
		return CreateResult{}, invalid("Invalid batch name.", map[string]any{"batch_name": "must contain 1 to 128 bytes"})
	}
	if input.MaxUses < 1 || input.MaxUses > 1_000_000 {
		return CreateResult{}, invalid("Invalid maximum uses.", map[string]any{"max_uses": "must be between 1 and 1000000"})
	}
	now := s.now().UTC()
	if input.ExpiresAt != nil && !input.ExpiresAt.After(now) {
		return CreateResult{}, invalid("Invalid expiry.", map[string]any{"expires_at": "must be in the future"})
	}
	if input.Permissions == nil {
		input.Permissions = map[string]any{}
	}
	if encoded, err := json.Marshal(input.Permissions); err != nil || len(encoded) > 8*1024 {
		return CreateResult{}, invalid("Invalid permissions.", map[string]any{"permissions": "must be valid JSON no larger than 8192 bytes"})
	}
	plaintext, err := newPlaintextCode()
	if err != nil {
		return CreateResult{}, internal(err)
	}
	item := Code{
		ID: newID("inv_"), BatchName: input.BatchName, MaxUses: input.MaxUses,
		ExpiresAt: input.ExpiresAt, Enabled: true, Permissions: input.Permissions,
		CreatedBy: meta.AdminID, CreatedAt: now, UpdatedAt: now,
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return CreateResult{}, internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := s.repository.Insert(ctx, tx, item, hashCode(plaintext)); err != nil {
		return CreateResult{}, internal(err)
	}
	if err := s.insertAudit(ctx, tx, meta, "INVITE_CODE_CREATED", item.ID,
		map[string]any{}, map[string]any{"batch_name": item.BatchName, "max_uses": item.MaxUses, "expires_at": item.ExpiresAt, "enabled": true}, now); err != nil {
		return CreateResult{}, internal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return CreateResult{}, internal(fmt.Errorf("commit invite creation: %w", err))
	}
	return CreateResult{Code: item, Plaintext: plaintext}, nil
}

func (s *Service) List(ctx context.Context, cursor string, limit int) (ListResult, error) {
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 100 {
		return ListResult{}, invalid("Invalid limit.", map[string]any{"limit": "must be between 1 and 100"})
	}
	items, err := s.repository.List(ctx, strings.TrimSpace(cursor), limit+1)
	if err != nil {
		return ListResult{}, internal(err)
	}
	nextCursor := ""
	if len(items) > limit {
		nextCursor = items[limit-1].ID
		items = items[:limit]
	}
	return ListResult{Items: items, NextCursor: nextCursor}, nil
}

func (s *Service) Get(ctx context.Context, id string) (Code, error) {
	item, err := s.repository.Get(ctx, strings.TrimSpace(id))
	if err != nil {
		return Code{}, mapNotFound(err)
	}
	return item, nil
}

func (s *Service) Patch(ctx context.Context, id string, patch Patch, meta RequestMeta) (Code, error) {
	if patch.BatchName == nil && patch.MaxUses == nil && patch.ExpiresAt == nil && !patch.ClearExpiry && patch.Enabled == nil && patch.Permissions == nil {
		return Code{}, invalid("At least one invite field is required.", nil)
	}
	return s.mutate(ctx, id, meta, "INVITE_CODE_UPDATED", func(item *Code, now time.Time) error {
		if patch.BatchName != nil {
			value := strings.TrimSpace(*patch.BatchName)
			if value == "" || len(value) > 128 {
				return invalid("Invalid batch name.", nil)
			}
			item.BatchName = value
		}
		if patch.MaxUses != nil {
			if *patch.MaxUses < item.UsedCount || *patch.MaxUses < 1 || *patch.MaxUses > 1_000_000 {
				return invalid("Invalid maximum uses.", map[string]any{"max_uses": "must be at least used_count and no more than 1000000"})
			}
			item.MaxUses = *patch.MaxUses
		}
		if patch.ClearExpiry {
			item.ExpiresAt = nil
		} else if patch.ExpiresAt != nil {
			if !patch.ExpiresAt.After(now) {
				return invalid("Invalid expiry.", nil)
			}
			item.ExpiresAt = patch.ExpiresAt
		}
		if patch.Enabled != nil {
			item.Enabled = *patch.Enabled
		}
		if patch.Permissions != nil {
			encoded, err := json.Marshal(patch.Permissions)
			if err != nil || len(encoded) > 8*1024 {
				return invalid("Invalid permissions.", nil)
			}
			item.Permissions = patch.Permissions
		}
		return nil
	})
}

func (s *Service) Revoke(ctx context.Context, id string, meta RequestMeta) (Code, error) {
	return s.mutate(ctx, id, meta, "INVITE_CODE_REVOKED", func(item *Code, now time.Time) error {
		item.Enabled = false
		if item.RevokedAt == nil {
			item.RevokedAt = &now
		}
		return nil
	})
}

func (s *Service) Consume(ctx context.Context, tx pgx.Tx, plaintext, playerID, steamID, ipAddress string, now time.Time) error {
	plaintext = normalizeCode(plaintext)
	if plaintext == "" || len(plaintext) > 128 {
		return ErrInvalidCode
	}
	return s.repository.Consume(ctx, tx, hashCode(plaintext), playerID, steamID, ipAddress, now)
}

func (s *Service) mutate(ctx context.Context, id string, meta RequestMeta, action string, mutate func(*Code, time.Time) error) (Code, error) {
	meta = sanitizeMeta(meta)
	if meta.AdminID == "" {
		return Code{}, unauthorized()
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Code{}, internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	oldItem, err := s.repository.GetForUpdate(ctx, tx, strings.TrimSpace(id))
	if err != nil {
		return Code{}, mapNotFound(err)
	}
	item := oldItem
	now := s.now().UTC()
	if err := mutate(&item, now); err != nil {
		return Code{}, err
	}
	item.UpdatedAt = now
	if err := s.repository.Update(ctx, tx, item); err != nil {
		return Code{}, internal(err)
	}
	if err := s.insertAudit(ctx, tx, meta, action, item.ID, auditValue(oldItem), auditValue(item), now); err != nil {
		return Code{}, internal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Code{}, internal(fmt.Errorf("commit invite mutation: %w", err))
	}
	return item, nil
}

func (s *Service) insertAudit(ctx context.Context, tx pgx.Tx, meta RequestMeta, action, id string, oldValue, newValue map[string]any, now time.Time) error {
	return s.audits.InsertAudit(ctx, tx, admin.AuditLog{
		ID: newID("ada_"), AdminID: meta.AdminID, Action: action, TargetType: "invite_code", TargetID: id,
		OldValue: oldValue, NewValue: newValue, RequestID: meta.RequestID, IPAddress: meta.IPAddress, CreatedAt: now,
	})
}

func auditValue(item Code) map[string]any {
	return map[string]any{
		"batch_name": item.BatchName, "max_uses": item.MaxUses, "used_count": item.UsedCount,
		"expires_at": item.ExpiresAt, "enabled": item.Enabled, "permissions": item.Permissions,
		"revoked_at": item.RevokedAt,
	}
}

func newPlaintextCode() (string, error) {
	random := make([]byte, 15)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("generate invite code: %w", err)
	}
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(random)
	parts := make([]string, 0, len(encoded)/4)
	for len(encoded) > 0 {
		length := min(4, len(encoded))
		parts = append(parts, encoded[:length])
		encoded = encoded[length:]
	}
	return "INV-" + strings.Join(parts, "-"), nil
}

func normalizeCode(value string) string { return strings.ToUpper(strings.TrimSpace(value)) }

func hashCode(value string) []byte {
	sum := sha256.Sum256([]byte(normalizeCode(value)))
	return sum[:]
}

func sanitizeMeta(meta RequestMeta) RequestMeta {
	meta.AdminID = truncate(strings.TrimSpace(meta.AdminID), 128)
	meta.RequestID = truncate(strings.TrimSpace(meta.RequestID), 128)
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

func newID(prefix string) string { return prefix + strings.ReplaceAll(uuid.NewString(), "-", "") }

func invalid(message string, details map[string]any) error {
	return &ServiceError{Status: http.StatusBadRequest, Code: "INVALID_REQUEST", Message: message, Details: details}
}

func unauthorized() error {
	return &ServiceError{Status: http.StatusUnauthorized, Code: "ADMIN_UNAUTHORIZED", Message: "Administrator authentication is required."}
}

func internal(err error) error {
	return &ServiceError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "Internal server error.", Cause: err}
}

func mapNotFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return &ServiceError{Status: http.StatusNotFound, Code: "INVITE_CODE_NOT_FOUND", Message: "Invite code not found."}
	}
	return internal(err)
}
