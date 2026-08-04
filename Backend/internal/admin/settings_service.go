package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type SettingsService struct {
	pool   *pgxpool.Pool
	audits *Repository
	now    func() time.Time
}

func NewSettingsService(pool *pgxpool.Pool, audits *Repository) *SettingsService {
	return &SettingsService{pool: pool, audits: audits, now: time.Now}
}

func (s *SettingsService) List(ctx context.Context) ([]AdminSetting, error) {
	rows, err := s.pool.Query(ctx, adminSettingsQuery+" ORDER BY category, setting_key")
	if err != nil {
		return nil, internal(err)
	}
	defer rows.Close()
	items, err := scanAdminSettings(rows)
	if err != nil {
		return nil, internal(err)
	}
	return items, nil
}

func (s *SettingsService) Features(ctx context.Context) (map[string]bool, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT setting_key, value
		FROM admin_settings
		WHERE category = 'FEATURES'
		ORDER BY setting_key
	`)
	if err != nil {
		return nil, internal(err)
	}
	defer rows.Close()
	result := make(map[string]bool)
	for rows.Next() {
		var key string
		var raw []byte
		if err := rows.Scan(&key, &raw); err != nil {
			return nil, internal(err)
		}
		var enabled bool
		if err := json.Unmarshal(raw, &enabled); err != nil {
			return nil, internal(fmt.Errorf("decode feature %s: %w", key, err))
		}
		result[strings.TrimPrefix(key, "features.")] = enabled
	}
	if err := rows.Err(); err != nil {
		return nil, internal(err)
	}
	return result, nil
}

func (s *SettingsService) Capabilities(ctx context.Context) (AdminCapabilities, error) {
	features, err := s.Features(ctx)
	if err != nil {
		return AdminCapabilities{}, err
	}
	return AdminCapabilities{
		APIVersion: "v1",
		Resources: []AdminCapabilityResource{
			{Name: "dashboard", Operations: []string{"read"}},
			{Name: "players", Operations: []string{"read", "update_status", "update_vip", "revoke_sessions"}},
			{Name: "risk_events", Operations: []string{"read", "resolve"}},
			{Name: "invite_codes", Operations: []string{"read", "create", "update", "revoke", "export_once"}},
			{Name: "rooms", Operations: []string{"read", "close", "remove_member"}},
			{Name: "game_servers", Operations: []string{"read", "register", "drain", "resume", "disable"}},
			{Name: "relay_nodes", Operations: []string{"read", "drain", "resume", "revoke", "migrate_existing"}},
			{Name: "vnt_nodes", Operations: []string{"read", "drain", "revoke"}},
			{Name: "connections", Operations: []string{"read", "close", "migrate"}},
			{Name: "updates", Operations: []string{"read", "create", "validate", "publish", "rollback", "archive"}},
			{Name: "administrators", Operations: []string{"read", "create", "update", "reset_mfa"}},
			{Name: "roles", Operations: []string{"read", "manage"}},
			{Name: "audit_logs", Operations: []string{"read"}},
			{Name: "settings", Operations: []string{"read", "update"}},
		},
		MaxBatchOperations:    100,
		RealtimeSubscriptions: false,
		DualApproval:          features["dual_approval"],
		PollingFallbackSeconds: map[string]int{
			"dashboard": 30,
			"fleet":     10,
			"lists":     60,
		},
	}, nil
}

func (s *SettingsService) Update(
	ctx context.Context,
	values map[string]any,
	reasonInput string,
	meta RequestMeta,
) ([]AdminSetting, error) {
	meta, reason, err := validateOnlineOperation(meta, reasonInput)
	if err != nil {
		return nil, err
	}
	if len(values) == 0 || len(values) > 20 {
		return nil, &ServiceError{
			Status: 400, Code: "INVALID_REQUEST",
			Message: "Between one and twenty settings must be supplied.",
		}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, &ServiceError{Status: 400, Code: "INVALID_SETTING", Message: "A setting key is invalid."}
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	rows, err := tx.Query(ctx, adminSettingsQuery+`
		WHERE setting_key = ANY($1)
		ORDER BY setting_key
		FOR UPDATE
	`, keys)
	if err != nil {
		return nil, internal(err)
	}
	current, err := scanAdminSettings(rows)
	rows.Close()
	if err != nil {
		return nil, internal(err)
	}
	if len(current) != len(keys) {
		return nil, &ServiceError{Status: 400, Code: "INVALID_SETTING", Message: "One or more settings do not exist."}
	}
	byKey := make(map[string]AdminSetting, len(current))
	for _, item := range current {
		byKey[item.Key] = item
	}
	oldValues := make(map[string]any, len(keys))
	newValues := make(map[string]any, len(keys))
	now := s.now().UTC()
	for _, key := range keys {
		item := byKey[key]
		if !item.Editable {
			return nil, &ServiceError{
				Status: 409, Code: "SETTING_READ_ONLY",
				Message: "One or more settings are capability indicators and cannot be edited.",
				Details: map[string]any{"key": key},
			}
		}
		normalized, err := normalizeAdminSettingValue(item, values[key])
		if err != nil {
			return nil, err
		}
		encoded, err := json.Marshal(normalized)
		if err != nil {
			return nil, internal(err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE admin_settings
			SET value = $2::jsonb, updated_by = $3, updated_at = $4
			WHERE setting_key = $1
		`, key, encoded, meta.AdminID, now); err != nil {
			return nil, internal(err)
		}
		oldValues[key], newValues[key] = item.Value, normalized
		item.Value, item.UpdatedBy, item.UpdatedAt = normalized, meta.AdminID, now
		byKey[key] = item
	}
	if err := s.audits.InsertAudit(ctx, tx, AuditLog{
		ID: newID("ada_"), AdminID: meta.AdminID,
		Action: "ADMIN_SETTINGS_UPDATED", TargetType: "admin_settings",
		TargetID: strings.Join(keys, ","), OldValue: oldValues, NewValue: newValues,
		Reason: reason, RequestID: meta.RequestID, IPAddress: meta.IPAddress,
		UserAgent: meta.UserAgent, Result: "SUCCEEDED", CreatedAt: now,
	}); err != nil {
		return nil, internal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, internal(fmt.Errorf("commit administrator settings: %w", err))
	}
	updated := make([]AdminSetting, 0, len(keys))
	for _, key := range keys {
		updated = append(updated, byKey[key])
	}
	return updated, nil
}

func normalizeAdminSettingValue(item AdminSetting, value any) (any, error) {
	switch item.ValueType {
	case "BOOLEAN":
		enabled, ok := value.(bool)
		if !ok {
			return nil, invalidSettingValue(item.Key, "must be a boolean")
		}
		return enabled, nil
	case "URL":
		raw, ok := value.(string)
		if !ok {
			return nil, invalidSettingValue(item.Key, "must be an HTTPS URL or empty")
		}
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return "", nil
		}
		if len(raw) > 2048 {
			return nil, invalidSettingValue(item.Key, "must contain at most 2048 characters")
		}
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
			return nil, invalidSettingValue(item.Key, "must be an HTTPS URL without embedded credentials")
		}
		return parsed.String(), nil
	default:
		return nil, internal(errors.New("unsupported administrator setting type"))
	}
}

func invalidSettingValue(key, rule string) error {
	return &ServiceError{
		Status: 400, Code: "INVALID_SETTING_VALUE", Message: "A setting value is invalid.",
		Details: map[string]any{"key": key, "rule": rule},
	}
}

const adminSettingsQuery = `
	SELECT setting_key, category, display_name, description, value,
	       value_type, editable, updated_by, updated_at
	FROM admin_settings
`

type adminSettingRows interface {
	Next() bool
	Scan(...any) error
	Err() error
}

func scanAdminSettings(rows adminSettingRows) ([]AdminSetting, error) {
	items := make([]AdminSetting, 0)
	for rows.Next() {
		var item AdminSetting
		var raw []byte
		var updatedBy sql.NullString
		if err := rows.Scan(
			&item.Key, &item.Category, &item.DisplayName, &item.Description,
			&raw, &item.ValueType, &item.Editable, &updatedBy, &item.UpdatedAt,
		); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &item.Value); err != nil {
			return nil, fmt.Errorf("decode administrator setting %s: %w", item.Key, err)
		}
		if updatedBy.Valid {
			item.UpdatedBy = updatedBy.String
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
