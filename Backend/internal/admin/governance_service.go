package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	playerauth "github.com/projectrebound/matchserver/internal/auth"
)

type GovernanceService struct {
	pool      *pgxpool.Pool
	audits    *Repository
	secretBox *SecretBox
	now       func() time.Time
}

func NewGovernanceService(
	pool *pgxpool.Pool,
	audits *Repository,
	secretBox *SecretBox,
) *GovernanceService {
	return &GovernanceService{pool: pool, audits: audits, secretBox: secretBox, now: time.Now}
}

func (s *GovernanceService) ListAdmins(ctx context.Context) ([]GovernedAdmin, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT user_account.id, user_account.username, user_account.display_name,
		       user_account.status,
		       EXISTS (
		           SELECT 1 FROM admin_mfa_credentials
		           WHERE admin_id = user_account.id AND verified_at IS NOT NULL
		       ),
		       COALESCE(array_agg(role.name ORDER BY role.name)
		           FILTER (WHERE role.name IS NOT NULL), '{}'),
		       user_account.last_login_at, user_account.created_at,
		       user_account.updated_at, user_account.disabled_at
		FROM admin_users AS user_account
		LEFT JOIN admin_user_roles AS user_role ON user_role.admin_id = user_account.id
		LEFT JOIN admin_roles AS role ON role.id = user_role.role_id
		GROUP BY user_account.id
		ORDER BY user_account.created_at DESC, user_account.id
	`)
	if err != nil {
		return nil, internal(err)
	}
	defer rows.Close()
	items := make([]GovernedAdmin, 0)
	for rows.Next() {
		item, err := scanGovernedAdmin(rows)
		if err != nil {
			return nil, internal(err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, internal(err)
	}
	return items, nil
}

func (s *GovernanceService) ListRoles(
	ctx context.Context,
) ([]GovernedRole, []PermissionDefinition, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT role.id, role.name, role.display_name, role.description,
		       role.system_role,
		       COALESCE(array_agg(permission.permission_key ORDER BY permission.permission_key)
		           FILTER (WHERE permission.permission_key IS NOT NULL), '{}'),
		       role.created_at, role.updated_at
		FROM admin_roles AS role
		LEFT JOIN admin_role_permissions AS role_permission ON role_permission.role_id = role.id
		LEFT JOIN admin_permissions AS permission ON permission.id = role_permission.permission_id
		GROUP BY role.id
		ORDER BY role.name
	`)
	if err != nil {
		return nil, nil, internal(err)
	}
	defer rows.Close()
	roles := make([]GovernedRole, 0)
	for rows.Next() {
		var item GovernedRole
		if err := rows.Scan(
			&item.ID, &item.Name, &item.DisplayName, &item.Description,
			&item.SystemRole, &item.Permissions, &item.CreatedAt, &item.UpdatedAt,
		); err != nil {
			return nil, nil, internal(err)
		}
		roles = append(roles, item)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, internal(err)
	}
	permissionRows, err := s.pool.Query(ctx, `
		SELECT permission_key, resource, action, description, risk_level
		FROM admin_permissions
		ORDER BY resource, permission_key
	`)
	if err != nil {
		return nil, nil, internal(err)
	}
	defer permissionRows.Close()
	permissions := make([]PermissionDefinition, 0)
	for permissionRows.Next() {
		var item PermissionDefinition
		if err := permissionRows.Scan(
			&item.Key, &item.Resource, &item.Action, &item.Description, &item.RiskLevel,
		); err != nil {
			return nil, nil, internal(err)
		}
		permissions = append(permissions, item)
	}
	if err := permissionRows.Err(); err != nil {
		return nil, nil, internal(err)
	}
	return roles, permissions, nil
}

func (s *GovernanceService) CreateAdmin(
	ctx context.Context,
	input CreateGovernedAdminInput,
	meta RequestMeta,
) (CreateGovernedAdminResult, error) {
	meta, reason, err := validateOnlineOperation(meta, input.Reason)
	if err != nil {
		return CreateGovernedAdminResult{}, err
	}
	username := normalizeUsername(input.Username)
	displayName := strings.TrimSpace(input.DisplayName)
	if username == "" || len(username) > 128 || displayName == "" || len(displayName) > 128 {
		return CreateGovernedAdminResult{}, &ServiceError{
			Status: 400, Code: "INVALID_REQUEST", Message: "Administrator identity is invalid.",
		}
	}
	passwordHash, err := HashPassword(input.Password)
	if err != nil {
		return CreateGovernedAdminResult{}, &ServiceError{
			Status: 400, Code: "INVALID_REQUEST", Message: "Administrator password is invalid.",
			Details: map[string]any{"password": err.Error()},
		}
	}
	roles := normalizedUniqueRoleNames(input.Roles)
	if len(roles) == 0 {
		return CreateGovernedAdminResult{}, &ServiceError{
			Status: 400, Code: "INVALID_REQUEST", Message: "At least one role is required.",
		}
	}
	adminID := playerauth.NewID("adm_")
	secret, err := NewTOTPSecret()
	if err != nil {
		return CreateGovernedAdminResult{}, internal(err)
	}
	secretCiphertext, err := s.secretBox.Encrypt(adminID, secret)
	if err != nil {
		return CreateGovernedAdminResult{}, internal(err)
	}
	recoveryCodes, recoveryHashes, err := NewRecoveryCodes(10)
	if err != nil {
		return CreateGovernedAdminResult{}, internal(err)
	}
	now := s.now().UTC()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return CreateGovernedAdminResult{}, internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := requireRoleNames(ctx, tx, roles); err != nil {
		return CreateGovernedAdminResult{}, err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO admin_users (
			id, username, display_name, password_hash, status, mfa_required,
			last_login_at, created_at, updated_at, disabled_at
		) VALUES ($1, $2, $3, $4, 'ACTIVE', TRUE, NULL, $5, $5, NULL)
	`, adminID, username, displayName, passwordHash, now)
	if err != nil {
		var pgError *pgconn.PgError
		if errors.As(err, &pgError) && pgError.Code == "23505" {
			return CreateGovernedAdminResult{}, &ServiceError{
				Status: 409, Code: "ADMIN_USERNAME_EXISTS", Message: "Administrator username already exists.",
			}
		}
		return CreateGovernedAdminResult{}, internal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO admin_mfa_credentials (
			admin_id, secret_ciphertext, key_version, verified_at, created_at, updated_at
		) VALUES ($1, $2, 1, $3, $3, $3)
	`, adminID, secretCiphertext, now); err != nil {
		return CreateGovernedAdminResult{}, internal(err)
	}
	for _, codeHash := range recoveryHashes {
		if _, err := tx.Exec(ctx, `
			INSERT INTO admin_recovery_codes (id, admin_id, code_hash, created_at, used_at)
			VALUES ($1, $2, $3, $4, NULL)
		`, newID("adrc_"), adminID, codeHash, now); err != nil {
			return CreateGovernedAdminResult{}, internal(err)
		}
	}
	if err := replaceAdminRoles(ctx, tx, adminID, roles, now); err != nil {
		return CreateGovernedAdminResult{}, err
	}
	item := GovernedAdmin{
		ID: adminID, Username: username, DisplayName: displayName,
		Status: AdminStatusActive, MFAEnabled: true, Roles: roles,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.insertGovernanceAudit(
		ctx, tx, meta, "ADMIN_CREATED", "admin_user", adminID,
		map[string]any{}, governedAdminAuditValue(item), reason, now,
	); err != nil {
		return CreateGovernedAdminResult{}, internal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return CreateGovernedAdminResult{}, internal(fmt.Errorf("commit administrator creation: %w", err))
	}
	return CreateGovernedAdminResult{
		Admin:           item,
		ProvisioningURI: TOTPProvisioningURI("ProjectRebound Admin", username, secret),
		RecoveryCodes:   recoveryCodes,
	}, nil
}

func (s *GovernanceService) UpdateAdmin(
	ctx context.Context,
	adminID string,
	input UpdateGovernedAdminInput,
	meta RequestMeta,
) (GovernedAdmin, error) {
	meta, reason, err := validateOnlineOperation(meta, input.Reason)
	if err != nil {
		return GovernedAdmin{}, err
	}
	if input.DisplayName == nil && input.Status == nil && !input.RolesSet && !input.RevokeSessions {
		return GovernedAdmin{}, &ServiceError{Status: 400, Code: "INVALID_REQUEST", Message: "At least one administrator change is required."}
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return GovernedAdmin{}, internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	oldItem, err := queryGovernedAdminForUpdate(ctx, tx, strings.TrimSpace(adminID))
	if errors.Is(err, pgx.ErrNoRows) {
		return GovernedAdmin{}, &ServiceError{Status: 404, Code: "ADMIN_NOT_FOUND", Message: "Administrator not found."}
	}
	if err != nil {
		return GovernedAdmin{}, internal(err)
	}
	displayName := oldItem.DisplayName
	if input.DisplayName != nil {
		displayName = strings.TrimSpace(*input.DisplayName)
		if displayName == "" || len(displayName) > 128 {
			return GovernedAdmin{}, &ServiceError{Status: 400, Code: "INVALID_REQUEST", Message: "Display name is invalid."}
		}
	}
	status := oldItem.Status
	if input.Status != nil {
		status = strings.ToUpper(strings.TrimSpace(*input.Status))
		if status != AdminStatusActive && status != AdminStatusDisabled {
			return GovernedAdmin{}, &ServiceError{Status: 400, Code: "INVALID_REQUEST", Message: "Administrator status is invalid."}
		}
	}
	roles := oldItem.Roles
	if input.RolesSet {
		roles = normalizedUniqueRoleNames(input.Roles)
		if len(roles) == 0 {
			return GovernedAdmin{}, &ServiceError{Status: 400, Code: "INVALID_REQUEST", Message: "At least one role is required."}
		}
		if err := requireRoleNames(ctx, tx, roles); err != nil {
			return GovernedAdmin{}, err
		}
	}
	if containsString(oldItem.Roles, "SUPER_ADMIN") &&
		(status != AdminStatusActive || !containsString(roles, "SUPER_ADMIN")) {
		if _, err := tx.Exec(ctx, `
			SELECT id FROM admin_roles WHERE name = 'SUPER_ADMIN' FOR UPDATE
		`); err != nil {
			return GovernedAdmin{}, internal(err)
		}
		var remaining int
		if err := tx.QueryRow(ctx, `
			SELECT COUNT(DISTINCT user_account.id)
			FROM admin_users AS user_account
			JOIN admin_user_roles AS user_role ON user_role.admin_id = user_account.id
			JOIN admin_roles AS role ON role.id = user_role.role_id
			WHERE role.name = 'SUPER_ADMIN' AND user_account.status = 'ACTIVE'
			  AND user_account.id <> $1
		`, oldItem.ID).Scan(&remaining); err != nil {
			return GovernedAdmin{}, internal(err)
		}
		if remaining == 0 {
			return GovernedAdmin{}, &ServiceError{
				Status: 409, Code: "LAST_SUPER_ADMIN",
				Message: "The last active SUPER_ADMIN cannot be disabled or stripped of that role.",
			}
		}
	}
	now := s.now().UTC()
	_, err = tx.Exec(ctx, `
		UPDATE admin_users
		SET display_name = $2, status = $3,
		    disabled_at = CASE WHEN $3 = 'DISABLED' THEN COALESCE(disabled_at, $4) ELSE NULL END,
		    updated_at = $4
		WHERE id = $1
	`, oldItem.ID, displayName, status, now)
	if err != nil {
		return GovernedAdmin{}, internal(err)
	}
	if input.RolesSet {
		if err := replaceAdminRoles(ctx, tx, oldItem.ID, roles, now); err != nil {
			return GovernedAdmin{}, err
		}
	}
	if input.RevokeSessions || status == AdminStatusDisabled {
		if _, err := tx.Exec(ctx, `
			UPDATE admin_sessions
			SET revoked_at = COALESCE(revoked_at, $2),
			    revoke_reason = COALESCE(revoke_reason, 'ADMIN_GOVERNANCE'),
			    last_used_at = $2
			WHERE admin_id = $1 AND revoked_at IS NULL
		`, oldItem.ID, now); err != nil {
			return GovernedAdmin{}, internal(err)
		}
	}
	item := oldItem
	item.DisplayName, item.Status, item.Roles, item.UpdatedAt = displayName, status, roles, now
	if status == AdminStatusDisabled && item.DisabledAt == nil {
		item.DisabledAt = &now
	}
	if status == AdminStatusActive {
		item.DisabledAt = nil
	}
	if err := s.insertGovernanceAudit(
		ctx, tx, meta, "ADMIN_UPDATED", "admin_user", item.ID,
		governedAdminAuditValue(oldItem), governedAdminAuditValue(item), reason, now,
	); err != nil {
		return GovernedAdmin{}, internal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return GovernedAdmin{}, internal(fmt.Errorf("commit administrator update: %w", err))
	}
	return item, nil
}

func (s *GovernanceService) ResetMFA(
	ctx context.Context,
	adminID, reasonInput string,
	meta RequestMeta,
) (ResetGovernedAdminMFAResult, error) {
	meta, reason, err := validateOnlineOperation(meta, reasonInput)
	if err != nil {
		return ResetGovernedAdminMFAResult{}, err
	}
	secret, err := NewTOTPSecret()
	if err != nil {
		return ResetGovernedAdminMFAResult{}, internal(err)
	}
	recoveryCodes, recoveryHashes, err := NewRecoveryCodes(10)
	if err != nil {
		return ResetGovernedAdminMFAResult{}, internal(err)
	}
	secretCiphertext, err := s.secretBox.Encrypt(strings.TrimSpace(adminID), secret)
	if err != nil {
		return ResetGovernedAdminMFAResult{}, internal(err)
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ResetGovernedAdminMFAResult{}, internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	item, err := queryGovernedAdminForUpdate(ctx, tx, strings.TrimSpace(adminID))
	if errors.Is(err, pgx.ErrNoRows) {
		return ResetGovernedAdminMFAResult{}, &ServiceError{Status: 404, Code: "ADMIN_NOT_FOUND", Message: "Administrator not found."}
	}
	if err != nil {
		return ResetGovernedAdminMFAResult{}, internal(err)
	}
	now := s.now().UTC()
	if _, err := tx.Exec(ctx, `
		UPDATE admin_mfa_credentials
		SET secret_ciphertext = $2, key_version = key_version + 1,
		    verified_at = $3, updated_at = $3
		WHERE admin_id = $1
	`, item.ID, secretCiphertext, now); err != nil {
		return ResetGovernedAdminMFAResult{}, internal(err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM admin_recovery_codes WHERE admin_id = $1`, item.ID); err != nil {
		return ResetGovernedAdminMFAResult{}, internal(err)
	}
	for _, codeHash := range recoveryHashes {
		if _, err := tx.Exec(ctx, `
			INSERT INTO admin_recovery_codes (id, admin_id, code_hash, created_at, used_at)
			VALUES ($1, $2, $3, $4, NULL)
		`, newID("adrc_"), item.ID, codeHash, now); err != nil {
			return ResetGovernedAdminMFAResult{}, internal(err)
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE admin_sessions
		SET revoked_at = COALESCE(revoked_at, $2),
		    revoke_reason = COALESCE(revoke_reason, 'MFA_RESET'),
		    last_used_at = $2
		WHERE admin_id = $1 AND revoked_at IS NULL
	`, item.ID, now); err != nil {
		return ResetGovernedAdminMFAResult{}, internal(err)
	}
	if err := s.insertGovernanceAudit(
		ctx, tx, meta, "ADMIN_MFA_RESET", "admin_user", item.ID,
		map[string]any{"mfa_enabled": true}, map[string]any{"mfa_enabled": true, "sessions_revoked": true},
		reason, now,
	); err != nil {
		return ResetGovernedAdminMFAResult{}, internal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return ResetGovernedAdminMFAResult{}, internal(fmt.Errorf("commit administrator MFA reset: %w", err))
	}
	return ResetGovernedAdminMFAResult{
		Admin:           item,
		ProvisioningURI: TOTPProvisioningURI("ProjectRebound Admin", item.Username, secret),
		RecoveryCodes:   recoveryCodes,
	}, nil
}

func (s *GovernanceService) UpdateRole(
	ctx context.Context,
	roleID string,
	permissionKeys []string,
	reasonInput string,
	meta RequestMeta,
) (GovernedRole, error) {
	meta, reason, err := validateOnlineOperation(meta, reasonInput)
	if err != nil {
		return GovernedRole{}, err
	}
	keys := normalizedUniquePermissionKeys(permissionKeys)
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return GovernedRole{}, internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var item GovernedRole
	err = tx.QueryRow(ctx, `
		SELECT id, name, display_name, description, system_role, created_at, updated_at
		FROM admin_roles WHERE id = $1 FOR UPDATE
	`, strings.TrimSpace(roleID)).Scan(
		&item.ID, &item.Name, &item.DisplayName, &item.Description,
		&item.SystemRole, &item.CreatedAt, &item.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return GovernedRole{}, &ServiceError{Status: 404, Code: "ROLE_NOT_FOUND", Message: "Administrator role not found."}
	}
	if err != nil {
		return GovernedRole{}, internal(err)
	}
	if item.Name == "SUPER_ADMIN" {
		return GovernedRole{}, &ServiceError{
			Status: 409, Code: "SUPER_ADMIN_ROLE_IMMUTABLE",
			Message: "SUPER_ADMIN always contains every permission and cannot be edited.",
		}
	}
	oldKeys, err := rolePermissionKeys(ctx, tx, item.ID)
	if err != nil {
		return GovernedRole{}, internal(err)
	}
	if len(keys) > 0 {
		var count int
		if err := tx.QueryRow(ctx, `
			SELECT COUNT(*) FROM admin_permissions WHERE permission_key = ANY($1)
		`, keys).Scan(&count); err != nil {
			return GovernedRole{}, internal(err)
		}
		if count != len(keys) {
			return GovernedRole{}, &ServiceError{Status: 400, Code: "INVALID_PERMISSION", Message: "One or more permissions do not exist."}
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM admin_role_permissions WHERE role_id = $1`, item.ID); err != nil {
		return GovernedRole{}, internal(err)
	}
	if len(keys) > 0 {
		if _, err := tx.Exec(ctx, `
			INSERT INTO admin_role_permissions (role_id, permission_id, created_at)
			SELECT $1, id, $3 FROM admin_permissions WHERE permission_key = ANY($2)
		`, item.ID, keys, s.now().UTC()); err != nil {
			return GovernedRole{}, internal(err)
		}
	}
	now := s.now().UTC()
	if _, err := tx.Exec(ctx, `UPDATE admin_roles SET updated_at = $2 WHERE id = $1`, item.ID, now); err != nil {
		return GovernedRole{}, internal(err)
	}
	item.Permissions, item.UpdatedAt = keys, now
	if err := s.insertGovernanceAudit(
		ctx, tx, meta, "ROLE_PERMISSIONS_UPDATED", "admin_role", item.ID,
		map[string]any{"permissions": oldKeys}, map[string]any{"permissions": keys},
		reason, now,
	); err != nil {
		return GovernedRole{}, internal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return GovernedRole{}, internal(fmt.Errorf("commit role update: %w", err))
	}
	return item, nil
}

func (s *GovernanceService) insertGovernanceAudit(
	ctx context.Context,
	tx pgx.Tx,
	meta RequestMeta,
	action, targetType, targetID string,
	oldValue, newValue map[string]any,
	reason string,
	now time.Time,
) error {
	return s.audits.InsertAudit(ctx, tx, AuditLog{
		ID: newID("ada_"), AdminID: meta.AdminID, Action: action,
		TargetType: targetType, TargetID: targetID, OldValue: oldValue, NewValue: newValue,
		Reason: reason, RequestID: meta.RequestID, IPAddress: meta.IPAddress,
		UserAgent: meta.UserAgent, Result: "SUCCEEDED", CreatedAt: now,
	})
}

func queryGovernedAdminForUpdate(ctx context.Context, tx pgx.Tx, adminID string) (GovernedAdmin, error) {
	return scanGovernedAdmin(tx.QueryRow(ctx, `
		SELECT user_account.id, user_account.username, user_account.display_name,
		       user_account.status,
		       EXISTS (
		           SELECT 1 FROM admin_mfa_credentials
		           WHERE admin_id = user_account.id AND verified_at IS NOT NULL
		       ),
		       COALESCE(ARRAY(
		           SELECT role.name
		           FROM admin_user_roles AS user_role
		           JOIN admin_roles AS role ON role.id = user_role.role_id
		           WHERE user_role.admin_id = user_account.id
		           ORDER BY role.name
		       ), '{}'),
		       user_account.last_login_at, user_account.created_at,
		       user_account.updated_at, user_account.disabled_at
		FROM admin_users AS user_account
		WHERE user_account.id = $1
		FOR UPDATE
	`, adminID))
}

func scanGovernedAdmin(row pgx.Row) (GovernedAdmin, error) {
	var item GovernedAdmin
	var lastLoginAt, disabledAt sql.NullTime
	err := row.Scan(
		&item.ID, &item.Username, &item.DisplayName, &item.Status,
		&item.MFAEnabled, &item.Roles, &lastLoginAt,
		&item.CreatedAt, &item.UpdatedAt, &disabledAt,
	)
	if lastLoginAt.Valid {
		item.LastLoginAt = &lastLoginAt.Time
	}
	if disabledAt.Valid {
		item.DisabledAt = &disabledAt.Time
	}
	return item, err
}

func requireRoleNames(ctx context.Context, tx pgx.Tx, roles []string) error {
	var count int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM admin_roles WHERE name = ANY($1)`, roles).Scan(&count); err != nil {
		return internal(err)
	}
	if count != len(roles) {
		return &ServiceError{Status: 400, Code: "INVALID_ROLE", Message: "One or more administrator roles do not exist."}
	}
	return nil
}

func replaceAdminRoles(ctx context.Context, tx pgx.Tx, adminID string, roles []string, now time.Time) error {
	if _, err := tx.Exec(ctx, `DELETE FROM admin_user_roles WHERE admin_id = $1`, adminID); err != nil {
		return internal(err)
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO admin_user_roles (admin_id, role_id, created_at)
		SELECT $1, id, $3 FROM admin_roles WHERE name = ANY($2)
	`, adminID, roles, now)
	if err != nil {
		return internal(err)
	}
	if int(tag.RowsAffected()) != len(roles) {
		return &ServiceError{Status: 400, Code: "INVALID_ROLE", Message: "One or more administrator roles do not exist."}
	}
	return nil
}

func rolePermissionKeys(ctx context.Context, tx pgx.Tx, roleID string) ([]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT permission.permission_key
		FROM admin_role_permissions AS role_permission
		JOIN admin_permissions AS permission ON permission.id = role_permission.permission_id
		WHERE role_permission.role_id = $1
		ORDER BY permission.permission_key
	`, roleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := make([]string, 0)
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

func normalizedUniqueRoleNames(values []string) []string {
	set := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToUpper(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if _, exists := set[value]; exists {
			continue
		}
		set[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func normalizedUniquePermissionKeys(values []string) []string {
	set := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := set[value]; exists {
			continue
		}
		set[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func governedAdminAuditValue(item GovernedAdmin) map[string]any {
	return map[string]any{
		"username": item.Username, "display_name": item.DisplayName,
		"status": item.Status, "mfa_enabled": item.MFAEnabled, "roles": item.Roles,
		"disabled_at": item.DisabledAt,
	}
}
