package gameserver

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct {
	pool *pgxpool.Pool
}

func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

func (r *Repository) Register(ctx context.Context, tx pgx.Tx, input Server) (Server, error) {
	row := tx.QueryRow(ctx, `
		INSERT INTO game_servers (
			id, instance_id, owner_player_id, display_name, region, mode, version,
			public_host, public_port, max_players, player_count, state,
			server_token_hash, registration_issuer, token_expires_at,
			last_heartbeat_at, created_at, updated_at
		) VALUES (
			$1, $2, NULLIF($3, ''), $4, $5, $6, $7, $8, $9, $10, 0, 'STARTING',
			$11, $12, $13, $14, $14, $14
		)
		ON CONFLICT (instance_id) DO UPDATE SET
			display_name = EXCLUDED.display_name,
			region = EXCLUDED.region,
			mode = EXCLUDED.mode,
			version = EXCLUDED.version,
			public_host = EXCLUDED.public_host,
			public_port = EXCLUDED.public_port,
			max_players = EXCLUDED.max_players,
			player_count = 0,
			state = 'STARTING',
			server_token_hash = EXCLUDED.server_token_hash,
			registration_issuer = EXCLUDED.registration_issuer,
			token_expires_at = EXCLUDED.token_expires_at,
			token_revoked_at = NULL,
			previous_server_token_hash = NULL,
			previous_token_expires_at = NULL,
			credential_generation = game_servers.credential_generation + 1,
			last_heartbeat_at = EXCLUDED.last_heartbeat_at,
			updated_at = EXCLUDED.updated_at
		WHERE (
			game_servers.owner_player_id = EXCLUDED.owner_player_id OR
			(game_servers.owner_player_id IS NULL AND EXCLUDED.owner_player_id IS NULL)
		)
		RETURNING `+serverColumns,
		input.ID, input.InstanceID, input.OwnerPlayerID, input.DisplayName, input.Region, input.Mode,
		input.Version, input.PublicHost, input.PublicPort, input.MaxPlayers,
		input.ServerTokenHash, input.RegistrationIssuer, input.TokenExpiresAt,
		input.LastHeartbeatAt,
	)
	item, err := scanServer(row)
	if err != nil {
		return Server{}, fmt.Errorf("register game server: %w", err)
	}
	return item, nil
}

func (r *Repository) Get(ctx context.Context, serverID string) (Server, error) {
	item, err := scanServer(r.pool.QueryRow(ctx, `SELECT `+serverColumns+` FROM game_servers WHERE id = $1`, serverID))
	if err != nil {
		return Server{}, fmt.Errorf("get game server: %w", err)
	}
	return item, nil
}

func (r *Repository) List(ctx context.Context, filter ListFilter) ([]Server, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+serverColumns+`
		FROM game_servers
		WHERE ($1 = '' OR id > $1)
		  AND ($2 = '' OR region = $2)
		  AND ($3 = '' OR mode = $3)
		  AND ($4 = '' OR version = $4)
		  AND ($5 = '' OR state = $5)
		ORDER BY id
		LIMIT $6
	`, filter.Cursor, filter.Region, filter.Mode, filter.Version, filter.State, filter.Limit)
	if err != nil {
		return nil, fmt.Errorf("list game servers: %w", err)
	}
	defer rows.Close()
	items := make([]Server, 0, filter.Limit)
	for rows.Next() {
		item, err := scanServer(rows)
		if err != nil {
			return nil, fmt.Errorf("scan listed game server: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate game servers: %w", err)
	}
	return items, nil
}

func (r *Repository) GetForManagement(ctx context.Context, tx pgx.Tx, serverID string, tokenHash []byte, now time.Time) (Server, error) {
	return scanServer(tx.QueryRow(ctx, `
		SELECT `+serverColumns+`
		FROM game_servers
		WHERE id = $1
		  AND (
			server_token_hash = $2 OR
			(previous_server_token_hash = $2 AND previous_token_expires_at > $3)
		  )
		  AND token_revoked_at IS NULL AND token_expires_at > $3
		FOR UPDATE
	`, serverID, tokenHash, now))
}

func (r *Repository) RotateCredential(
	ctx context.Context,
	tx pgx.Tx,
	serverID string,
	newTokenHash []byte,
	newExpiresAt, previousValidUntil, now time.Time,
) (Server, error) {
	return scanServer(tx.QueryRow(ctx, `
		UPDATE game_servers
		SET previous_server_token_hash = server_token_hash,
		    previous_token_expires_at = $3,
		    server_token_hash = $2,
		    token_expires_at = $4,
		    credential_generation = credential_generation + 1,
		    updated_at = $5
		WHERE id = $1 AND token_revoked_at IS NULL
		RETURNING `+serverColumns,
		serverID, newTokenHash, previousValidUntil, newExpiresAt, now,
	))
}

func (r *Repository) UpdateHeartbeat(ctx context.Context, tx pgx.Tx, serverID string, state State, playerCount int, now time.Time) (Server, error) {
	return scanServer(tx.QueryRow(ctx, `
		UPDATE game_servers
		SET state = $2, player_count = $3, last_heartbeat_at = $4, updated_at = $4
		WHERE id = $1
		RETURNING `+serverColumns,
		serverID, state, playerCount, now,
	))
}

func (r *Repository) Deregister(ctx context.Context, tx pgx.Tx, serverID string, now time.Time) error {
	_, err := tx.Exec(ctx, `
		UPDATE game_servers
		SET state = 'OFFLINE', token_revoked_at = $2,
		    previous_server_token_hash = NULL, previous_token_expires_at = NULL,
		    updated_at = $2
		WHERE id = $1
	`, serverID, now)
	if err != nil {
		return fmt.Errorf("deregister game server: %w", err)
	}
	return nil
}

func (r *Repository) SweepStale(ctx context.Context, now time.Time, unhealthyAfter, offlineAfter time.Duration) (int64, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE game_servers
		SET state = CASE
		        WHEN last_heartbeat_at <= $2 THEN 'OFFLINE'
		        WHEN last_heartbeat_at <= $3 THEN 'UNHEALTHY'
		        ELSE state
		    END,
		    updated_at = $1
		WHERE state <> 'OFFLINE' AND last_heartbeat_at <= $3
	`, now, now.Add(-offlineAfter), now.Add(-unhealthyAfter))
	if err != nil {
		return 0, fmt.Errorf("sweep stale game servers: %w", err)
	}
	return tag.RowsAffected(), nil
}

const serverColumns = `
	id, instance_id, COALESCE(owner_player_id, ''), display_name, region, mode, version,
	public_host, public_port, max_players, player_count, state,
	server_token_hash, previous_server_token_hash, registration_issuer, token_expires_at,
	previous_token_expires_at, credential_generation,
	token_revoked_at, last_heartbeat_at, created_at, updated_at
`

func scanServer(row pgx.Row) (Server, error) {
	var item Server
	var revokedAt, previousExpiresAt sql.NullTime
	err := row.Scan(
		&item.ID,
		&item.InstanceID,
		&item.OwnerPlayerID,
		&item.DisplayName,
		&item.Region,
		&item.Mode,
		&item.Version,
		&item.PublicHost,
		&item.PublicPort,
		&item.MaxPlayers,
		&item.PlayerCount,
		&item.State,
		&item.ServerTokenHash,
		&item.PreviousServerTokenHash,
		&item.RegistrationIssuer,
		&item.TokenExpiresAt,
		&previousExpiresAt,
		&item.CredentialGeneration,
		&revokedAt,
		&item.LastHeartbeatAt,
		&item.CreatedAt,
		&item.UpdatedAt,
	)
	if revokedAt.Valid {
		item.TokenRevokedAt = &revokedAt.Time
	}
	if previousExpiresAt.Valid {
		item.PreviousTokenExpiresAt = &previousExpiresAt.Time
	}
	return item, err
}
