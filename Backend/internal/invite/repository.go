package invite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

func (r *Repository) Insert(ctx context.Context, tx pgx.Tx, code Code, codeHash []byte) error {
	permissions, err := json.Marshal(code.Permissions)
	if err != nil {
		return fmt.Errorf("marshal invite permissions: %w", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO invite_codes (
			id, code_hash, batch_name, max_uses, used_count, expires_at,
			enabled, permissions, created_by, created_at, updated_at
		) VALUES ($1, $2, $3, $4, 0, $5, TRUE, $6::jsonb, $7, $8, $8)
	`, code.ID, codeHash, code.BatchName, code.MaxUses, code.ExpiresAt,
		permissions, code.CreatedBy, code.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert invite code: %w", err)
	}
	return nil
}

func (r *Repository) Get(ctx context.Context, id string) (Code, error) {
	return scanCode(r.pool.QueryRow(ctx, codeSelect+" WHERE id = $1", id))
}

func (r *Repository) GetForUpdate(ctx context.Context, tx pgx.Tx, id string) (Code, error) {
	return scanCode(tx.QueryRow(ctx, codeSelect+" WHERE id = $1 FOR UPDATE", id))
}

func (r *Repository) List(ctx context.Context, cursor string, limit int) ([]Code, error) {
	rows, err := r.pool.Query(ctx, codeSelect+`
		WHERE ($1 = '' OR id > $1)
		ORDER BY id
		LIMIT $2
	`, cursor, limit)
	if err != nil {
		return nil, fmt.Errorf("list invite codes: %w", err)
	}
	defer rows.Close()
	items := make([]Code, 0, limit)
	for rows.Next() {
		item, err := scanCode(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate invite codes: %w", err)
	}
	return items, nil
}

func (r *Repository) ListUses(ctx context.Context, inviteCodeID, cursor string, limit int) ([]Use, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, invite_code_id, player_id, steam_id,
		       COALESCE(host(ip_address), ''), used_at, result
		FROM invite_code_uses
		WHERE invite_code_id = $1
		  AND ($2 = '' OR id > $2)
		ORDER BY id
		LIMIT $3
	`, inviteCodeID, cursor, limit)
	if err != nil {
		return nil, fmt.Errorf("list invite code uses: %w", err)
	}
	defer rows.Close()
	items := make([]Use, 0, limit)
	for rows.Next() {
		var item Use
		if err := rows.Scan(
			&item.ID, &item.InviteCodeID, &item.PlayerID, &item.SteamID,
			&item.IPAddress, &item.UsedAt, &item.Result,
		); err != nil {
			return nil, fmt.Errorf("scan invite code use: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate invite code uses: %w", err)
	}
	return items, nil
}

func (r *Repository) Update(ctx context.Context, tx pgx.Tx, code Code) error {
	permissions, err := json.Marshal(code.Permissions)
	if err != nil {
		return fmt.Errorf("marshal invite permissions: %w", err)
	}
	tag, err := tx.Exec(ctx, `
		UPDATE invite_codes
		SET batch_name = $2, max_uses = $3, expires_at = $4, enabled = $5,
		    permissions = $6::jsonb, updated_at = $7, revoked_at = $8
		WHERE id = $1
	`, code.ID, code.BatchName, code.MaxUses, code.ExpiresAt, code.Enabled,
		permissions, code.UpdatedAt, code.RevokedAt)
	if err != nil {
		return fmt.Errorf("update invite code: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	return nil
}

func (r *Repository) Consume(
	ctx context.Context,
	tx pgx.Tx,
	codeHash []byte,
	playerID, steamID, ipAddress string,
	now time.Time,
) error {
	var id string
	var maxUses, usedCount int
	var enabled bool
	var permissions []byte
	var expiresAt, revokedAt sql.NullTime
	err := tx.QueryRow(ctx, `
		SELECT id, max_uses, used_count, enabled, expires_at, revoked_at, permissions
		FROM invite_codes
		WHERE code_hash = $1
		FOR UPDATE
	`, codeHash).Scan(&id, &maxUses, &usedCount, &enabled, &expiresAt, &revokedAt, &permissions)
	if err != nil {
		if err == pgx.ErrNoRows {
			return ErrInvalidCode
		}
		return fmt.Errorf("lock invite code: %w", err)
	}
	if !enabled || revokedAt.Valid || expiresAt.Valid && !expiresAt.Time.After(now) || usedCount >= maxUses {
		return ErrInvalidCode
	}
	tag, err := tx.Exec(ctx, `
		UPDATE invite_codes
		SET used_count = used_count + 1, updated_at = $2
		WHERE id = $1 AND used_count < max_uses
	`, id, now)
	if err != nil {
		return fmt.Errorf("consume invite code: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrInvalidCode
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO invite_code_uses (
			id, invite_code_id, player_id, steam_id, ip_address, used_at, result,
			permission_snapshot
		) VALUES ($1, $2, $3, $4, NULLIF($5, '')::inet, $6, 'SUCCESS', $7::jsonb)
	`, newID("icu_"), id, playerID, steamID, ipAddress, now, permissions)
	if err != nil {
		return fmt.Errorf("record invite code use: %w", err)
	}
	return nil
}

const codeSelect = `
	SELECT id, batch_name, max_uses, used_count, expires_at, enabled,
	       permissions, created_by, created_at, updated_at, revoked_at
	FROM invite_codes
`

type rowScanner interface {
	Scan(...any) error
}

func scanCode(row rowScanner) (Code, error) {
	var code Code
	var permissions []byte
	var expiresAt, revokedAt sql.NullTime
	err := row.Scan(&code.ID, &code.BatchName, &code.MaxUses, &code.UsedCount,
		&expiresAt, &code.Enabled, &permissions, &code.CreatedBy, &code.CreatedAt,
		&code.UpdatedAt, &revokedAt)
	if err != nil {
		return Code{}, err
	}
	if expiresAt.Valid {
		code.ExpiresAt = &expiresAt.Time
	}
	if revokedAt.Valid {
		code.RevokedAt = &revokedAt.Time
	}
	if err := json.Unmarshal(permissions, &code.Permissions); err != nil {
		return Code{}, fmt.Errorf("decode invite permissions: %w", err)
	}
	return code, nil
}
