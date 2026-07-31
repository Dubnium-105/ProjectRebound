package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type Executor interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type Queryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

type Repository struct{}

func NewRepository() *Repository {
	return &Repository{}
}

func (r *Repository) UpsertDeviceFingerprint(
	ctx context.Context,
	executor Executor,
	fingerprint DeviceFingerprint,
) (string, error) {
	var id string
	err := executor.QueryRow(ctx, `
		INSERT INTO auth_device_fingerprints (
			id, format_version, digest_key_id, composite_digest,
			smbios_uuid_digest, disk_serial_digest, cpu_id_digest, factor_mask,
			first_seen_at, last_seen_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10
		)
		ON CONFLICT (digest_key_id, composite_digest)
		DO UPDATE SET last_seen_at = GREATEST(
			auth_device_fingerprints.last_seen_at,
			EXCLUDED.last_seen_at
		)
		RETURNING id
	`, fingerprint.ID, fingerprint.FormatVersion, fingerprint.DigestKeyID,
		fingerprint.CompositeDigest, fingerprint.SMBIOSUUIDDigest,
		fingerprint.DiskSerialDigest, fingerprint.CPUIDDigest, fingerprint.FactorMask,
		fingerprint.FirstSeenAt, fingerprint.LastSeenAt,
	).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("upsert device fingerprint: %w", err)
	}
	return id, nil
}

func (r *Repository) InsertSession(ctx context.Context, executor Executor, session Session) error {
	_, err := executor.Exec(ctx, `
		INSERT INTO auth_sessions (
			id, player_id, refresh_token_hash, token_family_id, token_version,
			auth_provider, auth_level, steam_verified,
			device_id, device_id_hash, device_id_suffix, device_fingerprint_id,
			ip_address, user_agent, expires_at, created_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, NULL, $9, NULLIF($10, ''), NULLIF($11, ''),
			NULLIF($12, '')::inet, NULLIF($13, ''), $14, $15
		)
	`, session.ID, session.PlayerID, session.RefreshTokenHash, session.TokenFamilyID,
		session.TokenVersion, session.AuthProvider, session.AuthLevel, session.SteamVerified,
		session.DeviceIDHash, session.DeviceIDSuffix, session.DeviceFingerprintID,
		session.IPAddress, session.UserAgent,
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

func (r *Repository) PromoteIntegrityTrusted(
	ctx context.Context,
	tx pgx.Tx,
	sessionID string,
	playerID string,
	now time.Time,
) (bool, error) {
	tag, err := tx.Exec(ctx, `
		UPDATE auth_sessions
		SET auth_level = 'trusted',
		    last_used_at = $3
		WHERE id = $1
		  AND player_id = $2
		  AND auth_level IN ('verified', 'trusted')
		  AND steam_verified = TRUE
		  AND revoked_at IS NULL
		  AND expires_at > $3
	`, sessionID, playerID, now)
	if err != nil {
		return false, fmt.Errorf("promote integrity session: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return false, nil
	}
	if _, err := tx.Exec(ctx, `
		UPDATE players
		SET auth_provider = 'steam_ticket',
		    auth_level = 'trusted',
		    updated_at = $2
		WHERE id = $1
		  AND auth_level IN ('verified', 'trusted')
	`, playerID, now); err != nil {
		return false, fmt.Errorf("promote player integrity level: %w", err)
	}
	return true, nil
}

func (r *Repository) RevokePlayerSessions(ctx context.Context, executor Executor, playerID string, now time.Time, reason string) (int64, error) {
	tag, err := executor.Exec(ctx, `
		UPDATE auth_sessions
		SET revoked_at = COALESCE(revoked_at, $2),
		    revoked_reason = COALESCE(revoked_reason, $3),
		    last_used_at = $2
		WHERE player_id = $1 AND revoked_at IS NULL
	`, playerID, now, reason)
	if err != nil {
		return 0, fmt.Errorf("revoke player auth sessions: %w", err)
	}
	return tag.RowsAffected(), nil
}

func (r *Repository) ListPlayerSessions(ctx context.Context, queryer Queryer, playerID string, now time.Time) ([]Session, error) {
	rows, err := queryer.Query(ctx, sessionSelect+`
		WHERE player_id = $1 AND revoked_at IS NULL AND expires_at > $2
		ORDER BY created_at DESC, id DESC
	`, playerID, now)
	if err != nil {
		return nil, fmt.Errorf("list player sessions: %w", err)
	}
	defer rows.Close()
	items := make([]Session, 0)
	for rows.Next() {
		item, err := scanSession(rows)
		if err != nil {
			return nil, fmt.Errorf("scan player session: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate player sessions: %w", err)
	}
	return items, nil
}

func (r *Repository) RevokeOwnedSession(ctx context.Context, executor Executor, playerID, sessionID string, now time.Time) (bool, error) {
	tag, err := executor.Exec(ctx, `
		UPDATE auth_sessions
		SET revoked_at = COALESCE(revoked_at, $3),
		    revoked_reason = COALESCE(revoked_reason, 'USER_REVOKED'),
		    last_used_at = $3
		WHERE player_id = $1 AND id = $2 AND revoked_at IS NULL
	`, playerID, sessionID, now)
	if err != nil {
		return false, fmt.Errorf("revoke owned auth session: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

func (r *Repository) RevokeOtherSessions(ctx context.Context, executor Executor, playerID, currentSessionID string, now time.Time) (int64, error) {
	tag, err := executor.Exec(ctx, `
		UPDATE auth_sessions
		SET revoked_at = $3, revoked_reason = 'USER_REVOKED_OTHERS', last_used_at = $3
		WHERE player_id = $1 AND id <> $2 AND revoked_at IS NULL AND expires_at > $3
	`, playerID, currentSessionID, now)
	if err != nil {
		return 0, fmt.Errorf("revoke other auth sessions: %w", err)
	}
	return tag.RowsAffected(), nil
}

func (r *Repository) TouchSession(ctx context.Context, executor Executor, sessionID string, now time.Time) error {
	_, err := executor.Exec(ctx, `
		UPDATE auth_sessions
		SET last_used_at = $2
		WHERE id = $1 AND (last_used_at IS NULL OR last_used_at < $2::timestamptz - INTERVAL '5 minutes')
	`, sessionID, now)
	if err != nil {
		return fmt.Errorf("touch auth session: %w", err)
	}
	return nil
}

func (r *Repository) ListRiskEvents(
	ctx context.Context,
	queryer Queryer,
	cursor, playerID, eventType, severity string,
	unresolvedOnly bool,
	limit int,
) ([]RiskEvent, error) {
	rows, err := queryer.Query(ctx, `
		SELECT id, COALESCE(player_id, ''), COALESCE(steam_id, ''), device_id_hash,
		       COALESCE(device_fingerprint_id, ''),
		       COALESCE(ip_address::text, ''), event_type, severity, details,
		       created_at, resolved_at
		FROM auth_risk_events
		WHERE ($1 = '' OR id > $1)
		  AND ($2 = '' OR player_id = $2)
		  AND ($3 = '' OR event_type = $3)
		  AND ($4 = '' OR severity = $4)
		  AND (NOT $5 OR resolved_at IS NULL)
		ORDER BY id
		LIMIT $6
	`, cursor, playerID, eventType, severity, unresolvedOnly, limit)
	if err != nil {
		return nil, fmt.Errorf("list authentication risk events: %w", err)
	}
	defer rows.Close()
	items := make([]RiskEvent, 0, limit)
	for rows.Next() {
		var item RiskEvent
		var details []byte
		var resolvedAt sql.NullTime
		if err := rows.Scan(
			&item.ID, &item.PlayerID, &item.SteamID, &item.DeviceIDHash, &item.DeviceFingerprintID,
			&item.IPAddress, &item.EventType, &item.Severity, &details,
			&item.CreatedAt, &resolvedAt,
		); err != nil {
			return nil, fmt.Errorf("scan authentication risk event: %w", err)
		}
		if err := json.Unmarshal(details, &item.Details); err != nil {
			return nil, fmt.Errorf("decode authentication risk event details: %w", err)
		}
		if resolvedAt.Valid {
			item.ResolvedAt = &resolvedAt.Time
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate authentication risk events: %w", err)
	}
	return items, nil
}

func (r *Repository) InsertAudit(ctx context.Context, executor Executor, event AuditEvent) error {
	_, err := executor.Exec(ctx, `
		INSERT INTO auth_login_audit_logs (
			id, player_id, steam_id, event, success, failure_code,
			request_id, ip_address, user_agent, device_id_hash,
			device_fingerprint_id, created_at
		) VALUES (
			$1, NULLIF($2, ''), NULLIF($3, ''), $4, $5, NULLIF($6, ''),
			NULLIF($7, ''), NULLIF($8, '')::inet, NULLIF($9, ''), $10,
			NULLIF($11, ''), $12
		)
	`, event.ID, event.PlayerID, event.SteamID, event.Event, event.Success,
		event.FailureCode, event.RequestID, event.IPAddress, event.UserAgent,
		event.DeviceIDHash, event.DeviceFingerprintID, event.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert authentication audit event: %w", err)
	}
	return nil
}

func (r *Repository) InsertRiskEvent(ctx context.Context, executor Executor, event RiskEvent) error {
	_, err := executor.Exec(ctx, `
		INSERT INTO auth_risk_events (
			id, player_id, steam_id, device_id_hash, device_fingerprint_id, ip_address,
			event_type, severity, details, created_at
		) VALUES (
			$1, NULLIF($2, ''), NULLIF($3, ''), $4, NULLIF($5, ''), NULLIF($6, '')::inet,
			$7, $8, $9, $10
		)
	`, event.ID, event.PlayerID, event.SteamID, event.DeviceIDHash, event.DeviceFingerprintID, event.IPAddress,
		event.EventType, event.Severity, event.Details, event.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert authentication risk event: %w", err)
	}
	return nil
}

func (r *Repository) InsertLoginEvent(ctx context.Context, executor Executor, event LoginEvent) error {
	_, err := executor.Exec(ctx, `
		INSERT INTO auth_login_events (
			id, player_id, steam_id, session_id, device_id_hash, device_fingerprint_id,
			ip_address, user_agent, result, failure_code, created_at
		) VALUES (
			$1, NULLIF($2, ''), NULLIF($3, ''), NULLIF($4, ''), $5, NULLIF($6, ''),
			NULLIF($7, '')::inet, NULLIF($8, ''), $9, NULLIF($10, ''), $11
		)
	`, event.ID, event.PlayerID, event.SteamID, event.SessionID, event.DeviceIDHash, event.DeviceFingerprintID,
		event.IPAddress, event.UserAgent, event.Result, event.FailureCode, event.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert authentication login event: %w", err)
	}
	return nil
}

func (r *Repository) InsertTicketVerification(
	ctx context.Context,
	executor Executor,
	verification TicketVerification,
) error {
	_, err := executor.Exec(ctx, `
		INSERT INTO auth_steam_ticket_verifications (
			id, player_id, steam_id, app_id, ticket_hash, issue_time, verified_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (ticket_hash) DO NOTHING
	`, verification.ID, verification.PlayerID, verification.SteamID, int64(verification.AppID),
		verification.TicketHash, verification.IssueTime, verification.VerifiedAt)
	if err != nil {
		return fmt.Errorf("insert Steam ticket verification: %w", err)
	}
	return nil
}

func (r *Repository) IsDeviceFingerprintBanned(
	ctx context.Context,
	queryer Executor,
	fingerprintID string,
) (bool, error) {
	if fingerprintID == "" {
		return false, nil
	}
	var banned bool
	err := queryer.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM auth_device_fingerprints AS fingerprint
			JOIN ban_device_fingerprint AS ban
			  ON ban.digest_key_id = fingerprint.digest_key_id
			WHERE fingerprint.id = $1
			  AND (
			      CASE WHEN ban.uuid_hash IS NOT NULL
			             AND ban.uuid_hash = fingerprint.smbios_uuid_digest THEN 1 ELSE 0 END +
			      CASE WHEN ban.disk_hash IS NOT NULL
			             AND ban.disk_hash = fingerprint.disk_serial_digest THEN 1 ELSE 0 END +
			      CASE WHEN ban.cpu_hash IS NOT NULL
			             AND ban.cpu_hash = fingerprint.cpu_id_digest THEN 1 ELSE 0 END
			  ) >= 2
		)
	`, fingerprintID).Scan(&banned)
	if err != nil {
		return false, fmt.Errorf("check device fingerprint ban: %w", err)
	}
	return banned, nil
}

const sessionSelect = `
	SELECT id, player_id, refresh_token_hash, token_family_id, token_version,
	       auth_provider, auth_level, steam_verified,
	       device_id_hash, COALESCE(device_id_suffix, RIGHT(device_id, 4), ''),
	       COALESCE(device_fingerprint_id, ''),
	       COALESCE(ip_address::text, ''), COALESCE(user_agent, ''),
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
		&session.AuthProvider,
		&session.AuthLevel,
		&session.SteamVerified,
		&session.DeviceIDHash,
		&session.DeviceIDSuffix,
		&session.DeviceFingerprintID,
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
