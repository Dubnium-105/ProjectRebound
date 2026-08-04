package entitlement

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type querier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

func (r *Repository) GrantFromInvite(
	ctx context.Context,
	tx pgx.Tx,
	playerID, inviteUseID string,
	permissions map[string]any,
	expiresAt *time.Time,
	now time.Time,
) error {
	for _, capability := range FromInvitePermissions(permissions) {
		if _, err := tx.Exec(ctx, `
			INSERT INTO player_feature_grants (
				player_id, capability, source_invite_use_id, granted_at, expires_at
			) VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (player_id, capability) DO UPDATE SET
				source_invite_use_id = EXCLUDED.source_invite_use_id,
				granted_at = EXCLUDED.granted_at,
				expires_at = EXCLUDED.expires_at
			WHERE player_feature_grants.expires_at IS NOT NULL
			  AND (EXCLUDED.expires_at IS NULL OR EXCLUDED.expires_at > player_feature_grants.expires_at)
		`, playerID, capability, inviteUseID, now, expiresAt); err != nil {
			return fmt.Errorf("grant player capability %s: %w", capability, err)
		}
	}
	return nil
}

func (r *Repository) Has(ctx context.Context, playerID, capability string) (bool, error) {
	return r.HasWith(ctx, r.pool, playerID, capability)
}

func (r *Repository) HasWith(ctx context.Context, query querier, playerID, capability string) (bool, error) {
	var allowed bool
	if err := query.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM player_feature_grants
			WHERE player_id = $1 AND capability = $2
			  AND (expires_at IS NULL OR expires_at > NOW())
		)
	`, playerID, capability).Scan(&allowed); err != nil {
		return false, fmt.Errorf("check player capability: %w", err)
	}
	return allowed, nil
}

func (r *Repository) List(ctx context.Context, playerID string) ([]string, error) {
	return r.ListWith(ctx, r.pool, playerID)
}

func (r *Repository) ListWith(ctx context.Context, query querier, playerID string) ([]string, error) {
	rows, err := query.Query(ctx, `
		SELECT capability
		FROM player_feature_grants
		WHERE player_id = $1
		  AND (expires_at IS NULL OR expires_at > NOW())
		ORDER BY capability
	`, playerID)
	if err != nil {
		return nil, fmt.Errorf("list player capabilities: %w", err)
	}
	defer rows.Close()
	result := make([]string, 0, len(All))
	for rows.Next() {
		var capability string
		if err := rows.Scan(&capability); err != nil {
			return nil, fmt.Errorf("scan player capability: %w", err)
		}
		result = append(result, capability)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate player capabilities: %w", err)
	}
	return result, nil
}
