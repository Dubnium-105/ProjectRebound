package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type SecurityRepository struct {
	pool *pgxpool.Pool
}

func NewSecurityRepository(pool *pgxpool.Pool) *SecurityRepository {
	return &SecurityRepository{pool: pool}
}

func (r *SecurityRepository) DashboardSummary(ctx context.Context, now time.Time) (DashboardSummary, error) {
	var result DashboardSummary
	err := r.pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(DISTINCT player_id) FROM auth_sessions
			 WHERE revoked_at IS NULL AND expires_at > $1),
			(SELECT COUNT(*) FROM p2p_rooms WHERE state <> 'CLOSED'),
			(SELECT COUNT(*) FROM game_servers
			 WHERE state IN ('READY', 'RESERVED', 'RUNNING', 'DRAINING')),
			(SELECT COUNT(*) FROM relay_nodes WHERE state = 'READY'),
			(SELECT COUNT(*) FROM relay_allocations
			 WHERE state IN ('ALLOCATED', 'BINDING', 'ACTIVE')),
			(SELECT COUNT(*) FROM auth_risk_events WHERE resolved_at IS NULL),
			(SELECT COUNT(*) FROM invite_codes
			 WHERE enabled = TRUE AND revoked_at IS NULL
			   AND (expires_at IS NULL OR expires_at > $1) AND used_count < max_uses),
			(SELECT COUNT(*) FROM admin_sessions
			 WHERE revoked_at IS NULL AND expires_at > $1)
	`, now).Scan(
		&result.OnlinePlayers,
		&result.ActiveP2PRooms,
		&result.OnlineGameServers,
		&result.ReadyRelayNodes,
		&result.ActiveRelayAllocations,
		&result.UnresolvedRiskEvents,
		&result.ActiveInviteCodes,
		&result.ActiveAdminSessions,
	)
	if err != nil {
		return DashboardSummary{}, fmt.Errorf("query administrator dashboard summary: %w", err)
	}
	result.GeneratedAt = now
	return result, nil
}

func (r *SecurityRepository) DashboardTimeseries(
	ctx context.Context,
	start, end time.Time,
	step time.Duration,
) ([]DashboardPoint, error) {
	rows, err := r.pool.Query(ctx, `
		WITH buckets AS (
			SELECT generate_series($1::timestamptz, $2::timestamptz - $3::interval, $3::interval) AS bucket_start
		)
		SELECT bucket_start,
			(SELECT COUNT(*) FROM auth_login_events
			 WHERE created_at >= bucket_start AND created_at < bucket_start + $3::interval),
			(SELECT COUNT(*) FROM p2p_rooms
			 WHERE created_at >= bucket_start AND created_at < bucket_start + $3::interval),
			(SELECT COUNT(*) FROM auth_risk_events
			 WHERE created_at >= bucket_start AND created_at < bucket_start + $3::interval)
		FROM buckets
		ORDER BY bucket_start
	`, start, end, durationInterval(step))
	if err != nil {
		return nil, fmt.Errorf("query administrator dashboard timeseries: %w", err)
	}
	defer rows.Close()
	items := make([]DashboardPoint, 0, 32)
	for rows.Next() {
		var item DashboardPoint
		if err := rows.Scan(&item.BucketStart, &item.LoginCount, &item.RoomsCreated, &item.RiskEvents); err != nil {
			return nil, fmt.Errorf("scan administrator dashboard point: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate administrator dashboard timeseries: %w", err)
	}
	return items, nil
}

func durationInterval(value time.Duration) string {
	return fmt.Sprintf("%d seconds", int64(value/time.Second))
}

func (r *SecurityRepository) AlertCounts(ctx context.Context) (map[string]int64, error) {
	var relayUnhealthy, gameServerUnhealthy, criticalRisk int64
	err := r.pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM relay_nodes WHERE state IN ('UNHEALTHY', 'OFFLINE')),
			(SELECT COUNT(*) FROM game_servers WHERE state IN ('UNHEALTHY', 'OFFLINE')),
			(SELECT COUNT(*) FROM auth_risk_events
			 WHERE resolved_at IS NULL AND severity IN ('HIGH', 'CRITICAL'))
	`).Scan(&relayUnhealthy, &gameServerUnhealthy, &criticalRisk)
	if err != nil {
		return nil, fmt.Errorf("query administrator dashboard alerts: %w", err)
	}
	return map[string]int64{
		"relay_unhealthy":       relayUnhealthy,
		"game_server_unhealthy": gameServerUnhealthy,
		"critical_risk":         criticalRisk,
	}, nil
}

func (r *SecurityRepository) ListRiskEvents(ctx context.Context, filter RiskEventFilter) ([]AdminRiskEvent, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, COALESCE(player_id, ''), COALESCE(steam_id, ''),
		       COALESCE(ip_address::text, ''), event_type, severity, details,
		       created_at, resolved_at, COALESCE(resolved_by, ''),
		       COALESCE(resolution_note, '')
		FROM auth_risk_events
		WHERE ($1 = '' OR id > $1)
		  AND ($2 = '' OR player_id = $2)
		  AND ($3 = '' OR steam_id = $3)
		  AND ($4 = '' OR event_type = $4)
		  AND ($5 = '' OR severity = $5)
		  AND (NOT $6 OR resolved_at IS NULL)
		ORDER BY id
		LIMIT $7
	`, filter.Cursor, filter.PlayerID, filter.SteamID, filter.EventType,
		filter.Severity, filter.UnresolvedOnly, filter.Limit)
	if err != nil {
		return nil, fmt.Errorf("list administrator risk events: %w", err)
	}
	defer rows.Close()
	items := make([]AdminRiskEvent, 0, filter.Limit)
	for rows.Next() {
		item, err := scanAdminRiskEvent(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate administrator risk events: %w", err)
	}
	return items, nil
}

func (r *SecurityRepository) GetRiskEvent(ctx context.Context, queryer adminAuthExecutor, id string, forUpdate bool) (AdminRiskEvent, error) {
	suffix := ""
	if forUpdate {
		suffix = " FOR UPDATE"
	}
	return scanAdminRiskEvent(queryer.QueryRow(ctx, `
		SELECT id, COALESCE(player_id, ''), COALESCE(steam_id, ''),
		       COALESCE(ip_address::text, ''), event_type, severity, details,
		       created_at, resolved_at, COALESCE(resolved_by, ''),
		       COALESCE(resolution_note, '')
		FROM auth_risk_events
		WHERE id = $1
	`+suffix, id))
}

func (r *SecurityRepository) ResolveRiskEvent(
	ctx context.Context,
	tx pgx.Tx,
	id, adminID, note string,
	now time.Time,
) error {
	tag, err := tx.Exec(ctx, `
		UPDATE auth_risk_events
		SET resolved_at = $2, resolved_by = $3, resolution_note = $4
		WHERE id = $1 AND resolved_at IS NULL
	`, id, now, adminID, note)
	if err != nil {
		return fmt.Errorf("resolve administrator risk event: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	return nil
}

func (r *SecurityRepository) ListAudit(ctx context.Context, filter AuditFilter) ([]AuditEntry, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, admin_id, action, target_type, target_id, old_value, new_value,
		       reason, COALESCE(request_id, ''), COALESCE(ip_address::text, ''),
		       COALESCE(user_agent, ''), result, created_at
		FROM admin_audit_logs
		WHERE ($1 = '' OR id > $1)
		  AND ($2 = '' OR admin_id = $2)
		  AND ($3 = '' OR action = $3)
		  AND ($4 = '' OR target_type = $4)
		  AND ($5 = '' OR target_id = $5)
		ORDER BY id
		LIMIT $6
	`, filter.Cursor, filter.AdminID, filter.Action, filter.TargetType, filter.TargetID, filter.Limit)
	if err != nil {
		return nil, fmt.Errorf("list administrator audit logs: %w", err)
	}
	defer rows.Close()
	items := make([]AuditEntry, 0, filter.Limit)
	for rows.Next() {
		item, err := scanAuditEntry(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate administrator audit logs: %w", err)
	}
	return items, nil
}

func (r *SecurityRepository) GetAudit(ctx context.Context, id string) (AuditEntry, error) {
	return scanAuditEntry(r.pool.QueryRow(ctx, `
		SELECT id, admin_id, action, target_type, target_id, old_value, new_value,
		       reason, COALESCE(request_id, ''), COALESCE(ip_address::text, ''),
		       COALESCE(user_agent, ''), result, created_at
		FROM admin_audit_logs
		WHERE id = $1
	`, id))
}

func (r *SecurityRepository) ListLoginAudit(ctx context.Context, filter LoginAuditFilter) ([]LoginAuditEntry, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, COALESCE(admin_id, ''), event_type, result,
		       COALESCE(reason_code, ''), COALESCE(request_id, ''),
		       COALESCE(ip_address::text, ''), user_agent, turnstile_success,
		       turnstile_error_codes, COALESCE(turnstile_hostname, ''),
		       COALESCE(turnstile_action, ''), turnstile_verify_latency_ms, created_at
		FROM admin_login_audit_logs
		WHERE ($1 = '' OR id > $1)
		  AND ($2 = '' OR admin_id = $2)
		  AND ($3 = '' OR result = $3)
		ORDER BY id
		LIMIT $4
	`, filter.Cursor, filter.AdminID, filter.Result, filter.Limit)
	if err != nil {
		return nil, fmt.Errorf("list administrator login audit: %w", err)
	}
	defer rows.Close()
	items := make([]LoginAuditEntry, 0, filter.Limit)
	for rows.Next() {
		var item LoginAuditEntry
		var turnstileSuccess sql.NullBool
		var latency sql.NullInt32
		if err := rows.Scan(
			&item.ID, &item.AdminID, &item.EventType, &item.Result, &item.ReasonCode,
			&item.RequestID, &item.IPAddress, &item.UserAgent, &turnstileSuccess,
			&item.TurnstileErrorCodes, &item.TurnstileHostname, &item.TurnstileAction,
			&latency, &item.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan administrator login audit: %w", err)
		}
		if turnstileSuccess.Valid {
			value := turnstileSuccess.Bool
			item.TurnstileSuccess = &value
		}
		if latency.Valid {
			value := int(latency.Int32)
			item.TurnstileVerifyLatencyMS = &value
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate administrator login audit: %w", err)
	}
	return items, nil
}

func (r *SecurityRepository) PlayerExists(ctx context.Context, playerID string) (bool, error) {
	var exists bool
	if err := r.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM players WHERE id = $1)`, playerID).Scan(&exists); err != nil {
		return false, fmt.Errorf("check administrator player resource: %w", err)
	}
	return exists, nil
}

func (r *SecurityRepository) ListPlayerSessions(
	ctx context.Context,
	playerID string,
	now time.Time,
) ([]PlayerSessionEntry, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, COALESCE(device_id_suffix, ''), COALESCE(ip_address::text, ''),
		       token_family_id, created_at, last_used_at, expires_at, revoked_at,
		       COALESCE(revoked_reason, ''), reuse_detected_at,
		       (revoked_at IS NULL AND expires_at > $2)
		FROM auth_sessions
		WHERE player_id = $1
		ORDER BY created_at DESC
		LIMIT 200
	`, playerID, now)
	if err != nil {
		return nil, fmt.Errorf("list administrator player sessions: %w", err)
	}
	defer rows.Close()
	items := make([]PlayerSessionEntry, 0, 16)
	for rows.Next() {
		var item PlayerSessionEntry
		var lastUsedAt, revokedAt, reuseDetectedAt sql.NullTime
		if err := rows.Scan(
			&item.ID, &item.DeviceIDSuffix, &item.IPAddress, &item.TokenFamilyID,
			&item.CreatedAt, &lastUsedAt, &item.ExpiresAt, &revokedAt,
			&item.RevokedReason, &reuseDetectedAt, &item.Active,
		); err != nil {
			return nil, fmt.Errorf("scan administrator player session: %w", err)
		}
		if lastUsedAt.Valid {
			item.LastUsedAt = &lastUsedAt.Time
		}
		if revokedAt.Valid {
			item.RevokedAt = &revokedAt.Time
		}
		if reuseDetectedAt.Valid {
			item.ReuseDetectedAt = &reuseDetectedAt.Time
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate administrator player sessions: %w", err)
	}
	return items, nil
}

func (r *SecurityRepository) ListPlayerLoginEvents(
	ctx context.Context,
	playerID string,
) ([]PlayerLoginEventEntry, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, COALESCE(session_id, ''), COALESCE(ip_address::text, ''),
		       COALESCE(user_agent, ''), result, COALESCE(failure_code, ''), created_at
		FROM auth_login_events
		WHERE player_id = $1
		ORDER BY created_at DESC
		LIMIT 200
	`, playerID)
	if err != nil {
		return nil, fmt.Errorf("list administrator player login events: %w", err)
	}
	defer rows.Close()
	items := make([]PlayerLoginEventEntry, 0, 16)
	for rows.Next() {
		var item PlayerLoginEventEntry
		if err := rows.Scan(
			&item.ID, &item.SessionID, &item.IPAddress, &item.UserAgent,
			&item.Result, &item.FailureCode, &item.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan administrator player login event: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate administrator player login events: %w", err)
	}
	return items, nil
}

type securityRowScanner interface {
	Scan(...any) error
}

func scanAdminRiskEvent(row securityRowScanner) (AdminRiskEvent, error) {
	var item AdminRiskEvent
	var details []byte
	var resolvedAt sql.NullTime
	if err := row.Scan(
		&item.ID, &item.PlayerID, &item.SteamID, &item.IPAddress,
		&item.EventType, &item.Severity, &details, &item.CreatedAt,
		&resolvedAt, &item.ResolvedBy, &item.ResolutionNote,
	); err != nil {
		return AdminRiskEvent{}, err
	}
	if err := json.Unmarshal(details, &item.Details); err != nil {
		return AdminRiskEvent{}, fmt.Errorf("decode administrator risk event: %w", err)
	}
	if resolvedAt.Valid {
		item.ResolvedAt = &resolvedAt.Time
	}
	return item, nil
}

func scanAuditEntry(row securityRowScanner) (AuditEntry, error) {
	var item AuditEntry
	var oldValue, newValue []byte
	if err := row.Scan(
		&item.ID, &item.AdminID, &item.Action, &item.TargetType, &item.TargetID,
		&oldValue, &newValue, &item.Reason, &item.RequestID, &item.IPAddress,
		&item.UserAgent, &item.Result, &item.CreatedAt,
	); err != nil {
		return AuditEntry{}, err
	}
	if err := json.Unmarshal(oldValue, &item.OldValue); err != nil {
		return AuditEntry{}, fmt.Errorf("decode old administrator audit value: %w", err)
	}
	if err := json.Unmarshal(newValue, &item.NewValue); err != nil {
		return AuditEntry{}, fmt.Errorf("decode new administrator audit value: %w", err)
	}
	return item, nil
}
