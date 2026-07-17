package player

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

type Repository struct{}

type Queryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
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
	err := row.Scan(
		&item.ID,
		&item.SteamID,
		&item.PersonaName,
		&item.AccountStatus,
		&item.IsVIP,
		&item.AuthProvider,
		&item.AuthLevel,
		&item.LastLoginAt,
		&item.CreatedAt,
		&item.UpdatedAt,
	)
	return item, err
}
