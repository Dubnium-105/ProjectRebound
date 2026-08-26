package gameserver

import (
	"context"
	"database/sql"
	"errors"
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
			certificate_fingerprint = NULL,
			certificate_public_key = NULL,
			certificate_serial = NULL,
			certificate_expires_at = NULL,
			previous_certificate_fingerprint = NULL,
			previous_certificate_public_key = NULL,
			previous_certificate_expires_at = NULL,
			legacy_auth_expires_at = NULL,
			deleted_at = NULL,
			deleted_by = NULL,
			delete_reason = NULL,
			credential_generation = game_servers.credential_generation + 1,
			last_heartbeat_at = EXCLUDED.last_heartbeat_at,
			updated_at = EXCLUDED.updated_at
		WHERE game_servers.banned_at IS NULL AND (
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

func (r *Repository) BindCertificate(
	ctx context.Context,
	tx pgx.Tx,
	serverID string,
	certificate Certificate,
	now time.Time,
) (Server, error) {
	return scanServer(tx.QueryRow(ctx, `
		UPDATE game_servers
		SET certificate_fingerprint = $2,
		    certificate_public_key = $3,
		    certificate_serial = $4,
		    certificate_expires_at = $5,
		    previous_certificate_fingerprint = NULL,
		    previous_certificate_public_key = NULL,
		    previous_certificate_expires_at = NULL,
		    legacy_auth_expires_at = NULL,
		    updated_at = $6
		WHERE id = $1 AND deleted_at IS NULL AND banned_at IS NULL AND token_revoked_at IS NULL
		RETURNING `+serverColumns,
		serverID, certificate.Fingerprint, certificate.PublicKey,
		certificate.Serial, certificate.ExpiresAt, now,
	))
}

func (r *Repository) IsInstanceBanned(ctx context.Context, tx pgx.Tx, instanceID string) (bool, error) {
	var bannedAt sql.NullTime
	err := tx.QueryRow(ctx, `
		SELECT banned_at
		FROM game_servers
		WHERE instance_id = $1
		FOR SHARE
	`, instanceID).Scan(&bannedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check game server ban: %w", err)
	}
	return bannedAt.Valid, nil
}

func (r *Repository) Get(ctx context.Context, serverID string) (Server, error) {
	item, err := scanServer(r.pool.QueryRow(ctx, `SELECT `+serverColumns+` FROM game_servers WHERE id = $1 AND deleted_at IS NULL`, serverID))
	if err != nil {
		return Server{}, fmt.Errorf("get game server: %w", err)
	}
	return item, nil
}

func (r *Repository) List(ctx context.Context, filter ListFilter) ([]Server, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+serverColumns+`
		FROM game_servers
		WHERE deleted_at IS NULL
		  AND ($1 = '' OR id > $1)
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
		  AND deleted_at IS NULL AND banned_at IS NULL
		  AND (
			server_token_hash = $2 OR
			(previous_server_token_hash = $2 AND previous_token_expires_at > $3)
		  )
		  AND token_revoked_at IS NULL AND token_expires_at > $3
		FOR UPDATE
	`, serverID, tokenHash, now))
}

func (r *Repository) GetCurrentForManagement(ctx context.Context, tx pgx.Tx, serverID string, tokenHash []byte, now time.Time) (Server, error) {
	return scanServer(tx.QueryRow(ctx, `
		SELECT `+serverColumns+`
		FROM game_servers
		WHERE id = $1
		  AND deleted_at IS NULL AND banned_at IS NULL
		  AND server_token_hash = $2
		  AND token_revoked_at IS NULL AND token_expires_at > $3
		FOR UPDATE
	`, serverID, tokenHash, now))
}

func (r *Repository) RotateCredential(
	ctx context.Context,
	tx pgx.Tx,
	serverID string,
	newTokenHash []byte,
	certificate Certificate,
	newExpiresAt, previousValidUntil, now time.Time,
) (Server, error) {
	return scanServer(tx.QueryRow(ctx, `
		UPDATE game_servers
		SET previous_server_token_hash = server_token_hash,
		    previous_token_expires_at = $3,
		    server_token_hash = $2,
		    token_expires_at = $4,
		    previous_certificate_fingerprint = certificate_fingerprint,
		    previous_certificate_public_key = certificate_public_key,
		    previous_certificate_expires_at = CASE
		        WHEN certificate_public_key IS NULL THEN NULL
		        ELSE LEAST(certificate_expires_at, $3)
		    END,
		    certificate_fingerprint = $5,
		    certificate_public_key = $6,
		    certificate_serial = $7,
		    certificate_expires_at = $8,
		    legacy_auth_expires_at = NULL,
		    credential_generation = credential_generation + 1,
		    updated_at = $9
		WHERE id = $1 AND deleted_at IS NULL AND banned_at IS NULL AND token_revoked_at IS NULL
		RETURNING `+serverColumns,
		serverID, newTokenHash, previousValidUntil, newExpiresAt,
		certificate.Fingerprint, certificate.PublicKey, certificate.Serial,
		certificate.ExpiresAt, now,
	))
}

func (r *Repository) UpdateHeartbeat(ctx context.Context, tx pgx.Tx, serverID string, state State, playerCount int, now time.Time) (Server, error) {
	return scanServer(tx.QueryRow(ctx, `
		UPDATE game_servers
		SET state = $2, player_count = $3, last_heartbeat_at = $4, updated_at = $4
		WHERE id = $1 AND deleted_at IS NULL AND banned_at IS NULL
		RETURNING `+serverColumns,
		serverID, state, playerCount, now,
	))
}

// ActiveMatchAssignment returns the strict-roster attempt currently bound to
// this Dedicated Server. The game_servers row is already locked by Heartbeat;
// this intentionally remains a plain MVCC read so completion (which locks the
// attempt before releasing the server) cannot deadlock with a heartbeat.
func (r *Repository) ActiveMatchAssignment(ctx context.Context, tx pgx.Tx, serverID string) (*MatchAssignment, error) {
	var item MatchAssignment
	err := tx.QueryRow(ctx, `
		SELECT id, state, route_generation
		FROM match_attempts
		WHERE authority_id = $1 AND hosting_kind = 'DEDICATED'
		  AND state IN ('FROZEN', 'PROVISIONING', 'CONNECTING', 'RUNNING')
		ORDER BY created_at DESC, id DESC
		LIMIT 1
	`, serverID).Scan(&item.AttemptID, &item.State, &item.RouteGeneration)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &item, nil
}

func (r *Repository) Deregister(ctx context.Context, tx pgx.Tx, serverID string, now time.Time) error {
	_, err := tx.Exec(ctx, `
		UPDATE game_servers
		SET state = 'OFFLINE', token_revoked_at = $2,
		    previous_server_token_hash = NULL, previous_token_expires_at = NULL,
		    previous_certificate_fingerprint = NULL,
		    previous_certificate_public_key = NULL,
		    previous_certificate_expires_at = NULL,
		    updated_at = $2
		WHERE id = $1 AND deleted_at IS NULL
	`, serverID, now)
	if err != nil {
		return fmt.Errorf("deregister game server: %w", err)
	}
	return nil
}

func (r *Repository) RecordRequestNonce(
	ctx context.Context,
	tx pgx.Tx,
	serverID string,
	nonceHash []byte,
	now, expiresAt time.Time,
) (bool, error) {
	if _, err := tx.Exec(ctx, `
		DELETE FROM game_server_request_nonces
		WHERE server_id = $1 AND nonce_hash = $2 AND expires_at <= $3
	`, serverID, nonceHash, now); err != nil {
		return false, fmt.Errorf("remove expired game server request nonce: %w", err)
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO game_server_request_nonces (
			server_id, nonce_hash, expires_at, created_at
		) VALUES ($1, $2, $3, $4)
		ON CONFLICT DO NOTHING
	`, serverID, nonceHash, expiresAt, now)
	if err != nil {
		return false, fmt.Errorf("record game server request nonce: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

func (r *Repository) SweepStale(ctx context.Context, now time.Time, unhealthyAfter, offlineAfter time.Duration) (int64, error) {
	if _, err := r.pool.Exec(ctx, `
		DELETE FROM game_server_request_nonces WHERE expires_at <= $1
	`, now); err != nil {
		return 0, fmt.Errorf("sweep expired game server request nonces: %w", err)
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE game_servers
		SET state = CASE
		        WHEN last_heartbeat_at <= $2 THEN 'OFFLINE'
		        WHEN last_heartbeat_at <= $3 THEN 'UNHEALTHY'
		        ELSE state
		    END,
		    updated_at = $1
		WHERE deleted_at IS NULL AND banned_at IS NULL
		  AND state <> 'OFFLINE' AND last_heartbeat_at <= $3
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
	COALESCE(certificate_fingerprint, ''), certificate_public_key,
	COALESCE(certificate_serial, ''), certificate_expires_at,
	COALESCE(previous_certificate_fingerprint, ''), previous_certificate_public_key,
	previous_certificate_expires_at, legacy_auth_expires_at,
	token_revoked_at,
	banned_at, COALESCE(banned_by, ''), COALESCE(ban_reason, ''),
	deleted_at, COALESCE(deleted_by, ''), COALESCE(delete_reason, ''),
	last_heartbeat_at, created_at, updated_at
`

func scanServer(row pgx.Row) (Server, error) {
	var item Server
	var revokedAt, previousExpiresAt, certificateExpiresAt sql.NullTime
	var previousCertificateExpiresAt, legacyAuthExpiresAt sql.NullTime
	var bannedAt, deletedAt sql.NullTime
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
		&item.CertificateFingerprint,
		&item.CertificatePublicKey,
		&item.CertificateSerial,
		&certificateExpiresAt,
		&item.PreviousCertificateFingerprint,
		&item.PreviousCertificatePublicKey,
		&previousCertificateExpiresAt,
		&legacyAuthExpiresAt,
		&revokedAt,
		&bannedAt,
		&item.BannedBy,
		&item.BanReason,
		&deletedAt,
		&item.DeletedBy,
		&item.DeleteReason,
		&item.LastHeartbeatAt,
		&item.CreatedAt,
		&item.UpdatedAt,
	)
	if revokedAt.Valid {
		item.TokenRevokedAt = &revokedAt.Time
	}
	if bannedAt.Valid {
		item.BannedAt = &bannedAt.Time
	}
	if deletedAt.Valid {
		item.DeletedAt = &deletedAt.Time
	}
	if previousExpiresAt.Valid {
		item.PreviousTokenExpiresAt = &previousExpiresAt.Time
	}
	if certificateExpiresAt.Valid {
		item.CertificateExpiresAt = &certificateExpiresAt.Time
	}
	if previousCertificateExpiresAt.Valid {
		item.PreviousCertificateExpiresAt = &previousCertificateExpiresAt.Time
	}
	if legacyAuthExpiresAt.Valid {
		item.LegacyAuthExpiresAt = &legacyAuthExpiresAt.Time
	}
	return item, err
}
