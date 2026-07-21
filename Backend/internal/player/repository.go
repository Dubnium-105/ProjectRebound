package player

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

type Repository struct{}

type Queryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func NewRepository() *Repository {
	return &Repository{}
}

func (r *Repository) GetByID(ctx context.Context, queryer Queryer, playerID string) (Player, error) {
	row := queryer.QueryRow(ctx, `
		SELECT id, steam_id, persona_name, account_status, is_vip,
		       auth_provider, auth_level, last_login_at, created_at, updated_at
		FROM players
		WHERE id = $1
	`, playerID)
	item, err := scanPlayer(row)
	if err != nil {
		return Player{}, fmt.Errorf("get player by ID: %w", err)
	}
	return item, nil
}

func (r *Repository) List(ctx context.Context, queryer Queryer, cursor string, status AccountStatus, limit int) ([]Player, error) {
	rows, err := queryer.Query(ctx, `
		SELECT id, steam_id, persona_name, account_status, is_vip,
		       auth_provider, auth_level, last_login_at, created_at, updated_at
		FROM players
		WHERE ($1 = '' OR id > $1)
		  AND ($2 = '' OR account_status = $2)
		ORDER BY id
		LIMIT $3
	`, cursor, status, limit)
	if err != nil {
		return nil, fmt.Errorf("list players: %w", err)
	}
	defer rows.Close()

	items := make([]Player, 0, limit)
	for rows.Next() {
		item, err := scanPlayer(rows)
		if err != nil {
			return nil, fmt.Errorf("scan listed player: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate players: %w", err)
	}
	return items, nil
}

func (r *Repository) UpdateAdministrativeFields(
	ctx context.Context,
	tx pgx.Tx,
	playerID string,
	patch AdministrativePatch,
	now time.Time,
) (Player, Player, error) {
	oldValue, err := scanPlayer(tx.QueryRow(ctx, `
		SELECT id, steam_id, persona_name, account_status, is_vip,
		       auth_provider, auth_level, last_login_at, created_at, updated_at
		FROM players
		WHERE id = $1
		FOR UPDATE
	`, playerID))
	if err != nil {
		return Player{}, Player{}, fmt.Errorf("lock player for administrative update: %w", err)
	}
	newStatus := oldValue.AccountStatus
	if patch.AccountStatus != nil {
		newStatus = *patch.AccountStatus
	}
	newVIP := oldValue.IsVIP
	if patch.IsVIP != nil {
		newVIP = *patch.IsVIP
	}
	newValue, err := scanPlayer(tx.QueryRow(ctx, `
		UPDATE players
		SET account_status = $2, is_vip = $3, updated_at = $4
		WHERE id = $1
		RETURNING id, steam_id, persona_name, account_status, is_vip,
		          auth_provider, auth_level, last_login_at, created_at, updated_at
	`, playerID, newStatus, newVIP, now))
	if err != nil {
		return Player{}, Player{}, fmt.Errorf("update player administrative fields: %w", err)
	}
	return oldValue, newValue, nil
}

func (r *Repository) UpsertSteamIdentity(
	ctx context.Context,
	tx pgx.Tx,
	playerID string,
	steamID string,
	personaName string,
	now time.Time,
) (Player, bool, error) {
	tag, err := tx.Exec(ctx, `
		INSERT INTO players (
			id, steam_id, persona_name, account_status, is_vip,
			auth_provider, auth_level, last_login_at, created_at, updated_at
		) VALUES ($1, $2, $3, 'ACTIVE', FALSE, 'steam_client_asserted', 'unverified', $4, $4, $4)
		ON CONFLICT (steam_id) DO NOTHING
	`, playerID, steamID, personaName, now)
	if err != nil {
		return Player{}, false, fmt.Errorf("insert Steam player: %w", err)
	}
	isNew := tag.RowsAffected() == 1

	row := tx.QueryRow(ctx, `
		UPDATE players
		SET persona_name = $2, last_login_at = $3, updated_at = $3
		WHERE steam_id = $1
		RETURNING id, steam_id, persona_name, account_status, is_vip,
		          auth_provider, auth_level, last_login_at, created_at, updated_at
	`, steamID, personaName, now)
	item, err := scanPlayer(row)
	if err != nil {
		return Player{}, false, fmt.Errorf("load Steam player after upsert: %w", err)
	}
	return item, isNew, nil
}

func scanPlayer(row pgx.Row) (Player, error) {
	var item Player
	var lastLoginAt sql.NullTime
	err := row.Scan(
		&item.ID,
		&item.SteamID,
		&item.PersonaName,
		&item.AccountStatus,
		&item.IsVIP,
		&item.AuthProvider,
		&item.AuthLevel,
		&lastLoginAt,
		&item.CreatedAt,
		&item.UpdatedAt,
	)
	if lastLoginAt.Valid {
		item.LastLoginAt = lastLoginAt.Time
	}
	return item, err
}
