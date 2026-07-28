package admin

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type adminAuthExecutor interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type AuthRepository struct {
	pool *pgxpool.Pool
}

func NewAuthRepository(pool *pgxpool.Pool) *AuthRepository {
	return &AuthRepository{pool: pool}
}

func (r *AuthRepository) CreateAdmin(
	ctx context.Context,
	user AdminUser,
	secretCiphertext []byte,
	recoveryCodeHashes [][]byte,
	roleName string,
) error {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin create administrator: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	_, err = tx.Exec(ctx, `
		INSERT INTO admin_users (
			id, username, display_name, password_hash, status, mfa_required,
			last_login_at, created_at, updated_at, disabled_at
		) VALUES (
			$1, $2, $3, $4, 'ACTIVE', TRUE, NULL, $5, $5, NULL
		)
	`, user.ID, user.Username, user.DisplayName, user.PasswordHash, user.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert administrator: %w", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO admin_mfa_credentials (
			admin_id, secret_ciphertext, key_version, verified_at, created_at, updated_at
		) VALUES ($1, $2, 1, $3, $3, $3)
	`, user.ID, secretCiphertext, user.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert administrator MFA credential: %w", err)
	}
	for _, codeHash := range recoveryCodeHashes {
		_, err = tx.Exec(ctx, `
			INSERT INTO admin_recovery_codes (id, admin_id, code_hash, created_at, used_at)
			VALUES ($1, $2, $3, $4, NULL)
		`, newID("adrc_"), user.ID, codeHash, user.CreatedAt)
		if err != nil {
			return fmt.Errorf("insert administrator recovery code: %w", err)
		}
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO admin_user_roles (admin_id, role_id, created_at)
		SELECT $1, id, $3
		FROM admin_roles
		WHERE name = $2
	`, user.ID, roleName, user.CreatedAt)
	if err != nil {
		return fmt.Errorf("assign administrator role: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("assign administrator role: role %q does not exist", roleName)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit create administrator: %w", err)
	}
	return nil
}

func (r *AuthRepository) FindUserByUsername(ctx context.Context, username string) (AdminUser, error) {
	return scanAdminUser(r.pool.QueryRow(ctx, `
		SELECT id, username, display_name, password_hash, status, mfa_required,
		       last_login_at, created_at, updated_at, disabled_at
		FROM admin_users
		WHERE LOWER(username) = LOWER($1)
	`, username))
}

func (r *AuthRepository) GetUserByID(ctx context.Context, queryer adminAuthExecutor, adminID string) (AdminUser, error) {
	return scanAdminUser(queryer.QueryRow(ctx, `
		SELECT id, username, display_name, password_hash, status, mfa_required,
		       last_login_at, created_at, updated_at, disabled_at
		FROM admin_users
		WHERE id = $1
	`, adminID))
}

func (r *AuthRepository) GetMFASecret(ctx context.Context, adminID string) ([]byte, error) {
	var ciphertext []byte
	err := r.pool.QueryRow(ctx, `
		SELECT secret_ciphertext
		FROM admin_mfa_credentials
		WHERE admin_id = $1 AND verified_at IS NOT NULL
	`, adminID).Scan(&ciphertext)
	if err != nil {
		return nil, err
	}
	return ciphertext, nil
}

func (r *AuthRepository) InsertLoginChallenge(ctx context.Context, challenge LoginChallenge) error {
	_, err := r.pool.Exec(ctx, `
		DELETE FROM admin_login_challenges
		WHERE admin_id = $1 OR expires_at <= $2
	`, challenge.Admin.ID, challenge.CreatedAt)
	if err != nil {
		return fmt.Errorf("prune administrator login challenges: %w", err)
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO admin_login_challenges (
			id, admin_id, token_hash, attempts, request_id, ip_address,
			user_agent, expires_at, created_at
		) VALUES (
			$1, $2, $3, 0, NULLIF($4, ''), NULLIF($5, '')::inet,
			$6, $7, $8
		)
	`, challenge.ID, challenge.Admin.ID, challenge.TokenHash, challenge.RequestID,
		challenge.IPAddress, challenge.UserAgent, challenge.ExpiresAt, challenge.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert administrator login challenge: %w", err)
	}
	return nil
}

func (r *AuthRepository) FindLoginChallengeForUpdate(
	ctx context.Context,
	tx pgx.Tx,
	tokenHash []byte,
) (LoginChallenge, error) {
	var challenge LoginChallenge
	var lastLoginAt sql.NullTime
	var disabledAt sql.NullTime
	err := tx.QueryRow(ctx, `
		SELECT c.id, c.token_hash, c.attempts, COALESCE(c.request_id, ''),
		       COALESCE(c.ip_address::text, ''), c.user_agent, c.expires_at, c.created_at,
		       u.id, u.username, u.display_name, u.password_hash, u.status, u.mfa_required,
		       u.last_login_at, u.created_at, u.updated_at, u.disabled_at,
		       m.secret_ciphertext
		FROM admin_login_challenges c
		JOIN admin_users u ON u.id = c.admin_id
		JOIN admin_mfa_credentials m ON m.admin_id = u.id
		WHERE c.token_hash = $1
		FOR UPDATE OF c
	`, tokenHash).Scan(
		&challenge.ID,
		&challenge.TokenHash,
		&challenge.Attempts,
		&challenge.RequestID,
		&challenge.IPAddress,
		&challenge.UserAgent,
		&challenge.ExpiresAt,
		&challenge.CreatedAt,
		&challenge.Admin.ID,
		&challenge.Admin.Username,
		&challenge.Admin.DisplayName,
		&challenge.Admin.PasswordHash,
		&challenge.Admin.Status,
		&challenge.Admin.MFARequired,
		&lastLoginAt,
		&challenge.Admin.CreatedAt,
		&challenge.Admin.UpdatedAt,
		&disabledAt,
		&challenge.SecretCiphertext,
	)
	if err != nil {
		return LoginChallenge{}, err
	}
	if lastLoginAt.Valid {
		challenge.Admin.LastLoginAt = &lastLoginAt.Time
	}
	if disabledAt.Valid {
		challenge.Admin.DisabledAt = &disabledAt.Time
	}
	return challenge, nil
}

func (r *AuthRepository) RecordFailedMFA(
	ctx context.Context,
	tx pgx.Tx,
	challengeID string,
	attempts int,
) error {
	if attempts >= 5 {
		_, err := tx.Exec(ctx, `DELETE FROM admin_login_challenges WHERE id = $1`, challengeID)
		return err
	}
	_, err := tx.Exec(ctx, `
		UPDATE admin_login_challenges
		SET attempts = $2
		WHERE id = $1
	`, challengeID, attempts)
	return err
}

func (r *AuthRepository) DeleteLoginChallenge(ctx context.Context, tx pgx.Tx, challengeID string) error {
	_, err := tx.Exec(ctx, `DELETE FROM admin_login_challenges WHERE id = $1`, challengeID)
	return err
}

func (r *AuthRepository) ConsumeRecoveryCode(
	ctx context.Context,
	tx pgx.Tx,
	adminID string,
	codeHash []byte,
	now time.Time,
) (bool, error) {
	tag, err := tx.Exec(ctx, `
		UPDATE admin_recovery_codes
		SET used_at = $3
		WHERE admin_id = $1 AND code_hash = $2 AND used_at IS NULL
	`, adminID, codeHash, now)
	if err != nil {
		return false, fmt.Errorf("consume administrator recovery code: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

func (r *AuthRepository) InsertSession(ctx context.Context, tx pgx.Tx, session AdminSession) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO admin_sessions (
			id, admin_id, refresh_token_hash, previous_refresh_token_hash,
			token_version, ip_address, user_agent, created_at, last_used_at,
			expires_at, revoked_at, revoke_reason
		) VALUES (
			$1, $2, $3, NULL, $4, NULLIF($5, '')::inet, $6, $7, NULL,
			$8, NULL, NULL
		)
	`, session.ID, session.AdminID, session.RefreshTokenHash, session.TokenVersion,
		session.IPAddress, session.UserAgent, session.CreatedAt, session.ExpiresAt)
	if err != nil {
		return fmt.Errorf("insert administrator session: %w", err)
	}
	return nil
}

func (r *AuthRepository) FindSessionByRefreshForUpdate(
	ctx context.Context,
	tx pgx.Tx,
	tokenHash []byte,
) (AdminSession, AdminUser, error) {
	var session AdminSession
	var user AdminUser
	var lastUsedAt sql.NullTime
	var revokedAt sql.NullTime
	var lastLoginAt sql.NullTime
	var disabledAt sql.NullTime
	err := tx.QueryRow(ctx, `
		SELECT s.id, s.admin_id, s.refresh_token_hash, s.previous_refresh_token_hash,
		       s.token_version, COALESCE(s.ip_address::text, ''), s.user_agent,
		       s.created_at, s.last_used_at, s.expires_at, s.revoked_at,
		       COALESCE(s.revoke_reason, ''),
		       u.id, u.username, u.display_name, u.password_hash, u.status,
		       u.mfa_required, u.last_login_at, u.created_at, u.updated_at, u.disabled_at
		FROM admin_sessions s
		JOIN admin_users u ON u.id = s.admin_id
		WHERE s.refresh_token_hash = $1 OR s.previous_refresh_token_hash = $1
		FOR UPDATE OF s
	`, tokenHash).Scan(
		&session.ID,
		&session.AdminID,
		&session.RefreshTokenHash,
		&session.PreviousRefreshTokenHash,
		&session.TokenVersion,
		&session.IPAddress,
		&session.UserAgent,
		&session.CreatedAt,
		&lastUsedAt,
		&session.ExpiresAt,
		&revokedAt,
		&session.RevokeReason,
		&user.ID,
		&user.Username,
		&user.DisplayName,
		&user.PasswordHash,
		&user.Status,
		&user.MFARequired,
		&lastLoginAt,
		&user.CreatedAt,
		&user.UpdatedAt,
		&disabledAt,
	)
	if err != nil {
		return AdminSession{}, AdminUser{}, err
	}
	if lastUsedAt.Valid {
		session.LastUsedAt = &lastUsedAt.Time
	}
	if revokedAt.Valid {
		session.RevokedAt = &revokedAt.Time
	}
	if lastLoginAt.Valid {
		user.LastLoginAt = &lastLoginAt.Time
	}
	if disabledAt.Valid {
		user.DisabledAt = &disabledAt.Time
	}
	return session, user, nil
}

func (r *AuthRepository) RotateSession(
	ctx context.Context,
	tx pgx.Tx,
	sessionID string,
	currentHash, replacementHash []byte,
	tokenVersion int,
	now time.Time,
) error {
	tag, err := tx.Exec(ctx, `
		UPDATE admin_sessions
		SET previous_refresh_token_hash = $2,
		    refresh_token_hash = $3,
		    token_version = $4,
		    last_used_at = $5
		WHERE id = $1 AND revoked_at IS NULL
	`, sessionID, currentHash, replacementHash, tokenVersion, now)
	if err != nil {
		return fmt.Errorf("rotate administrator session: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return errorsNewSessionChanged
	}
	return nil
}

var errorsNewSessionChanged = fmt.Errorf("administrator session changed during rotation")

func (r *AuthRepository) RevokeSession(
	ctx context.Context,
	executor adminAuthExecutor,
	sessionID string,
	now time.Time,
	reason string,
) error {
	_, err := executor.Exec(ctx, `
		UPDATE admin_sessions
		SET revoked_at = COALESCE(revoked_at, $2),
		    revoke_reason = COALESCE(revoke_reason, $3),
		    last_used_at = $2
		WHERE id = $1
	`, sessionID, now, reason)
	if err != nil {
		return fmt.Errorf("revoke administrator session: %w", err)
	}
	return nil
}

func (r *AuthRepository) RevokeOwnedSession(
	ctx context.Context,
	executor adminAuthExecutor,
	adminID, sessionID string,
	now time.Time,
	reason string,
) (bool, error) {
	tag, err := executor.Exec(ctx, `
		UPDATE admin_sessions
		SET revoked_at = COALESCE(revoked_at, $3),
		    revoke_reason = COALESCE(revoke_reason, $4),
		    last_used_at = $3
		WHERE admin_id = $1 AND id = $2 AND revoked_at IS NULL
	`, adminID, sessionID, now, reason)
	if err != nil {
		return false, fmt.Errorf("revoke owned administrator session: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

func (r *AuthRepository) GetCurrentAdmin(ctx context.Context, sessionID string) (CurrentAdmin, int, time.Time, *time.Time, error) {
	var current CurrentAdmin
	var tokenVersion int
	var expiresAt time.Time
	var revokedAt sql.NullTime
	var lastLoginAt sql.NullTime
	var disabledAt sql.NullTime
	err := r.pool.QueryRow(ctx, `
		SELECT u.id, u.username, u.display_name, u.password_hash, u.status,
		       u.mfa_required, u.last_login_at, u.created_at, u.updated_at, u.disabled_at,
		       s.id, s.token_version, s.expires_at, s.revoked_at
		FROM admin_sessions s
		JOIN admin_users u ON u.id = s.admin_id
		WHERE s.id = $1
	`, sessionID).Scan(
		&current.User.ID,
		&current.User.Username,
		&current.User.DisplayName,
		&current.User.PasswordHash,
		&current.User.Status,
		&current.User.MFARequired,
		&lastLoginAt,
		&current.User.CreatedAt,
		&current.User.UpdatedAt,
		&disabledAt,
		&current.SessionID,
		&tokenVersion,
		&expiresAt,
		&revokedAt,
	)
	if err != nil {
		return CurrentAdmin{}, 0, time.Time{}, nil, err
	}
	if lastLoginAt.Valid {
		current.User.LastLoginAt = &lastLoginAt.Time
	}
	if disabledAt.Valid {
		current.User.DisabledAt = &disabledAt.Time
	}
	if revokedAt.Valid {
		currentRevokedAt := revokedAt.Time
		return current, tokenVersion, expiresAt, &currentRevokedAt, nil
	}
	roles, permissions, err := r.LoadAccess(ctx, r.pool, current.User.ID)
	if err != nil {
		return CurrentAdmin{}, 0, time.Time{}, nil, err
	}
	current.Roles = roles
	current.Permissions = permissions
	return current, tokenVersion, expiresAt, nil, nil
}

// GetCurrentAdminAuthorization loads only the fields required to authorize an
// already signed administrator session. It deliberately excludes password and
// MFA material so verifier-only services can use column-level database grants.
func (r *AuthRepository) GetCurrentAdminAuthorization(
	ctx context.Context,
	sessionID string,
) (CurrentAdmin, int, time.Time, *time.Time, error) {
	var current CurrentAdmin
	var tokenVersion int
	var expiresAt time.Time
	var revokedAt sql.NullTime
	var lastLoginAt sql.NullTime
	var disabledAt sql.NullTime
	err := r.pool.QueryRow(ctx, `
		SELECT u.id, u.username, u.display_name, u.status,
		       u.last_login_at, u.created_at, u.updated_at, u.disabled_at,
		       s.id, s.token_version, s.expires_at, s.revoked_at
		FROM admin_sessions s
		JOIN admin_users u ON u.id = s.admin_id
		WHERE s.id = $1
	`, sessionID).Scan(
		&current.User.ID,
		&current.User.Username,
		&current.User.DisplayName,
		&current.User.Status,
		&lastLoginAt,
		&current.User.CreatedAt,
		&current.User.UpdatedAt,
		&disabledAt,
		&current.SessionID,
		&tokenVersion,
		&expiresAt,
		&revokedAt,
	)
	if err != nil {
		return CurrentAdmin{}, 0, time.Time{}, nil, err
	}
	if lastLoginAt.Valid {
		current.User.LastLoginAt = &lastLoginAt.Time
	}
	if disabledAt.Valid {
		current.User.DisabledAt = &disabledAt.Time
	}
	if revokedAt.Valid {
		currentRevokedAt := revokedAt.Time
		return current, tokenVersion, expiresAt, &currentRevokedAt, nil
	}
	roles, permissions, err := r.LoadAccess(ctx, r.pool, current.User.ID)
	if err != nil {
		return CurrentAdmin{}, 0, time.Time{}, nil, err
	}
	current.Roles = roles
	current.Permissions = permissions
	return current, tokenVersion, expiresAt, nil, nil
}

func (r *AuthRepository) LoadAccess(
	ctx context.Context,
	queryer interface {
		Query(context.Context, string, ...any) (pgx.Rows, error)
	},
	adminID string,
) ([]string, []string, error) {
	rows, err := queryer.Query(ctx, `
		SELECT DISTINCT r.name, p.permission_key
		FROM admin_user_roles ur
		JOIN admin_roles r ON r.id = ur.role_id
		LEFT JOIN admin_role_permissions rp ON rp.role_id = r.id
		LEFT JOIN admin_permissions p ON p.id = rp.permission_id
		WHERE ur.admin_id = $1
		ORDER BY r.name, p.permission_key
	`, adminID)
	if err != nil {
		return nil, nil, fmt.Errorf("load administrator access: %w", err)
	}
	defer rows.Close()
	roleSet := make(map[string]struct{})
	permissionSet := make(map[string]struct{})
	roles := make([]string, 0)
	permissions := make([]string, 0)
	for rows.Next() {
		var role string
		var permission *string
		if err := rows.Scan(&role, &permission); err != nil {
			return nil, nil, fmt.Errorf("scan administrator access: %w", err)
		}
		if _, exists := roleSet[role]; !exists {
			roleSet[role] = struct{}{}
			roles = append(roles, role)
		}
		if permission != nil {
			if _, exists := permissionSet[*permission]; !exists {
				permissionSet[*permission] = struct{}{}
				permissions = append(permissions, *permission)
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("iterate administrator access: %w", err)
	}
	return roles, permissions, nil
}

func (r *AuthRepository) ListSessions(
	ctx context.Context,
	adminID, currentSessionID string,
	now time.Time,
) ([]SessionListItem, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, COALESCE(ip_address::text, ''), user_agent, created_at,
		       last_used_at, expires_at, id = $2
		FROM admin_sessions
		WHERE admin_id = $1 AND revoked_at IS NULL AND expires_at > $3
		ORDER BY created_at DESC, id DESC
	`, adminID, currentSessionID, now)
	if err != nil {
		return nil, fmt.Errorf("list administrator sessions: %w", err)
	}
	defer rows.Close()
	items := make([]SessionListItem, 0)
	for rows.Next() {
		var item SessionListItem
		var lastUsedAt sql.NullTime
		if err := rows.Scan(
			&item.ID,
			&item.IPAddress,
			&item.UserAgent,
			&item.CreatedAt,
			&lastUsedAt,
			&item.ExpiresAt,
			&item.IsCurrent,
		); err != nil {
			return nil, fmt.Errorf("scan administrator session: %w", err)
		}
		if lastUsedAt.Valid {
			item.LastUsedAt = &lastUsedAt.Time
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *AuthRepository) UpdateLastLogin(ctx context.Context, tx pgx.Tx, adminID string, now time.Time) error {
	_, err := tx.Exec(ctx, `
		UPDATE admin_users
		SET last_login_at = $2, updated_at = $2
		WHERE id = $1
	`, adminID, now)
	return err
}

func (r *AuthRepository) InsertLoginAudit(ctx context.Context, executor adminAuthExecutor, audit LoginAudit) error {
	turnstileErrorCodes := audit.TurnstileErrorCodes
	if turnstileErrorCodes == nil {
		turnstileErrorCodes = []string{}
	}
	_, err := executor.Exec(ctx, `
		INSERT INTO admin_login_audit_logs (
			id, admin_id, username_hash, event_type, result, reason_code,
			request_id, ip_address, user_agent, turnstile_success,
			turnstile_error_codes, turnstile_hostname, turnstile_action,
			turnstile_verify_latency_ms, created_at
		) VALUES (
			$1, NULLIF($2, ''), NULLIF($3, ''), $4, $5, NULLIF($6, ''),
			NULLIF($7, ''), NULLIF($8, '')::inet, $9, $10, $11,
			NULLIF($12, ''), NULLIF($13, ''), $14, $15
		)
	`, audit.ID, audit.AdminID, audit.UsernameHash, audit.EventType, audit.Result,
		audit.ReasonCode, audit.RequestID, audit.IPAddress, audit.UserAgent,
		audit.TurnstileSuccess, turnstileErrorCodes, audit.TurnstileHostname,
		audit.TurnstileAction, audit.TurnstileVerifyLatencyMS, audit.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert administrator login audit: %w", err)
	}
	return nil
}

func scanAdminUser(row pgx.Row) (AdminUser, error) {
	var item AdminUser
	var lastLoginAt sql.NullTime
	var disabledAt sql.NullTime
	err := row.Scan(
		&item.ID,
		&item.Username,
		&item.DisplayName,
		&item.PasswordHash,
		&item.Status,
		&item.MFARequired,
		&lastLoginAt,
		&item.CreatedAt,
		&item.UpdatedAt,
		&disabledAt,
	)
	if err != nil {
		return AdminUser{}, err
	}
	if lastLoginAt.Valid {
		item.LastLoginAt = &lastLoginAt.Time
	}
	if disabledAt.Valid {
		item.DisabledAt = &disabledAt.Time
	}
	return item, nil
}
