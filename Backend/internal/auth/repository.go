package auth

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type Executor interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type Repository struct{}

func NewRepository() *Repository {
	return &Repository{}
}

func (r *Repository) InsertSession(ctx context.Context, executor Executor, session Session) error {
	_, err := executor.Exec(ctx, `
		INSERT INTO auth_sessions (
			id, player_id, refresh_token_hash, token_family_id, token_version,
			device_id, ip_address, user_agent, expires_at, created_at
		) VALUES (
			$1, $2, $3, $4, $5, NULLIF($6, ''), NULLIF($7, '')::inet,
			NULLIF($8, ''), $9, $10
		)
	`, session.ID, session.PlayerID, session.RefreshTokenHash, session.TokenFamilyID,
		session.TokenVersion, session.DeviceID, session.IPAddress, session.UserAgent,
		session.ExpiresAt, session.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert auth session: %w", err)
	}
	return nil
}

func (r *Repository) FindByRefreshTokenForUpdate(ctx context.Context, tx pgx.Tx, refreshTokenHash []byte) (Session, error) {
	return scanSession(tx.QueryRow(ctx, sessionSelect+`
		WHERE refresh_token_hash = $1
		FOR UPDATE
	`, refreshTokenHash))
}

func (r *Repository) GetSession(ctx context.Context, queryer Executor, sessionID string) (Session, error) {
	return scanSession(queryer.QueryRow(ctx, sessionSelect+`
		WHERE id = $1
	`, sessionID))
}

func (r *Repository) RotateSession(ctx context.Context, tx pgx.Tx, oldSessionID string, replacement Session, now time.Time) error {
	tag, err := tx.Exec(ctx, `
		UPDATE auth_sessions
		SET revoked_at = $2,
		    revoked_reason = 'ROTATED',
		    replaced_by_session_id = $3,
		    last_used_at = $2
		WHERE id = $1 AND revoked_at IS NULL
	`, oldSessionID, now, replacement.ID)
	if err != nil {
		return fmt.Errorf("consume refresh session: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("consume refresh session: session was already revoked")
	}
	if err := r.InsertSession(ctx, tx, replacement); err != nil {
		return err
	}
	return nil
}

func (r *Repository) RevokeFamilyForReuse(ctx context.Context, tx pgx.Tx, familyID, reusedSessionID string, now time.Time) error {
	_, err := tx.Exec(ctx, `
		UPDATE auth_sessions
		SET revoked_at = COALESCE(revoked_at, $2),
		    revoked_reason = 'REFRESH_REUSE',
		    reuse_detected_at = CASE WHEN id = $3 THEN $2 ELSE reuse_detected_at END
		WHERE token_family_id = $1
	`, familyID, now, reusedSessionID)
	if err != nil {
		return fmt.Errorf("revoke session family after refresh reuse: %w", err)
	}
	return nil
}

func (r *Repository) RevokeSession(ctx context.Context, executor Executor, sessionID string, now time.Time, reason string) error {
	_, err := executor.Exec(ctx, `
		UPDATE auth_sessions
		SET revoked_at = COALESCE(revoked_at, $2),
		    revoked_reason = COALESCE(revoked_reason, $3),
		    last_used_at = $2
		WHERE id = $1
	`, sessionID, now, reason)
	if err != nil {
		return fmt.Errorf("revoke auth session: %w", err)
	}
	return nil
}

func (r *Repository) InsertAudit(ctx context.Context, executor Executor, event AuditEvent) error {
	_, err := executor.Exec(ctx, `
		INSERT INTO auth_login_audit_logs (
			id, player_id, steam_id, event, success, failure_code,
			request_id, ip_address, user_agent, created_at
		) VALUES (
			$1, NULLIF($2, ''), NULLIF($3, ''), $4, $5, NULLIF($6, ''),
			NULLIF($7, ''), NULLIF($8, '')::inet, NULLIF($9, ''), $10
		)
	`, event.ID, event.PlayerID, event.SteamID, event.Event, event.Success,
		event.FailureCode, event.RequestID, event.IPAddress, event.UserAgent, event.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert authentication audit event: %w", err)
	}
	return nil
}

const sessionSelect = `
	SELECT id, player_id, refresh_token_hash, token_family_id, token_version,
	       COALESCE(device_id, ''), COALESCE(ip_address::text, ''), COALESCE(user_agent, ''),
	       expires_at, revoked_at, COALESCE(revoked_reason, ''),
	       COALESCE(replaced_by_session_id, ''), reuse_detected_at, created_at, last_used_at
	FROM auth_sessions
`

func scanSession(row pgx.Row) (Session, error) {
	var session Session
	var revokedAt, reuseDetectedAt, lastUsedAt sql.NullTime
	err := row.Scan(
		&session.ID,
		&session.PlayerID,
		&session.RefreshTokenHash,
		&session.TokenFamilyID,
		&session.TokenVersion,
		&session.DeviceID,
		&session.IPAddress,
		&session.UserAgent,
		&session.ExpiresAt,
		&revokedAt,
		&session.RevokedReason,
		&session.ReplacedBySessionID,
		&reuseDetectedAt,
		&session.CreatedAt,
		&lastUsedAt,
	)
	if err != nil {
		return Session{}, err
	}
	if revokedAt.Valid {
		session.RevokedAt = &revokedAt.Time
	}
	if reuseDetectedAt.Valid {
		session.ReuseDetectedAt = &reuseDetectedAt.Time
	}
	if lastUsedAt.Valid {
		session.LastUsedAt = &lastUsedAt.Time
	}
	return session, nil
}
