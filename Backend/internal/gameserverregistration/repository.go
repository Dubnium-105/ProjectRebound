package gameserverregistration

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

var (
	ErrInvalidToken       = errors.New("invalid game server registration token")
	ErrInvalidInviteGrant = errors.New("player does not have a game server invitation grant")
)

type Repository struct{}

func NewRepository() *Repository { return &Repository{} }

func (r *Repository) Insert(ctx context.Context, tx pgx.Tx, credential Credential, tokenHash []byte) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO game_server_registration_tokens (
			id, instance_id, token_hash, created_by, issued_to_player_id,
			source_invite_use_id, expires_at, created_at
		) VALUES ($1, $2, $3, NULLIF($4, ''), NULLIF($5, ''), NULLIF($6, ''), $7, $8)
	`, credential.ID, credential.InstanceID, tokenHash, credential.CreatedBy,
		credential.IssuedToPlayerID, credential.SourceInviteUseID,
		credential.ExpiresAt, credential.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert game server registration token: %w", err)
	}
	return nil
}

func (r *Repository) FindPlayerInviteGrant(
	ctx context.Context,
	tx pgx.Tx,
	playerID string,
) (string, error) {
	var inviteUseID string
	err := tx.QueryRow(ctx, `
		SELECT invite_use.id
		FROM invite_code_uses AS invite_use
		WHERE invite_use.player_id = $1
		  AND invite_use.result = 'SUCCESS'
		  AND invite_use.permission_snapshot @> '{"allow_game_server_registration": true}'::jsonb
		ORDER BY invite_use.used_at DESC
		LIMIT 1
	`, playerID).Scan(&inviteUseID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrInvalidInviteGrant
	}
	if err != nil {
		return "", fmt.Errorf("find game server invitation grant: %w", err)
	}
	return inviteUseID, nil
}

func (r *Repository) RevokeActiveForInstance(
	ctx context.Context,
	tx pgx.Tx,
	instanceID string,
	now time.Time,
) (int64, error) {
	tag, err := tx.Exec(ctx, `
		UPDATE game_server_registration_tokens
		SET revoked_at = $2
		WHERE instance_id = $1 AND consumed_at IS NULL AND revoked_at IS NULL
	`, instanceID, now)
	if err != nil {
		return 0, fmt.Errorf("revoke previous game server registration tokens: %w", err)
	}
	return tag.RowsAffected(), nil
}

func (r *Repository) LockActive(
	ctx context.Context,
	tx pgx.Tx,
	tokenHash []byte,
	now time.Time,
) (Credential, error) {
	var credential Credential
	err := tx.QueryRow(ctx, `
		SELECT id, instance_id, COALESCE(created_by, ''),
		       COALESCE(issued_to_player_id, ''), COALESCE(source_invite_use_id, ''),
		       expires_at, created_at
		FROM game_server_registration_tokens
		WHERE token_hash = $1
		  AND consumed_at IS NULL
		  AND revoked_at IS NULL
		  AND expires_at > $2
		FOR UPDATE
	`, tokenHash, now).Scan(
		&credential.ID,
		&credential.InstanceID,
		&credential.CreatedBy,
		&credential.IssuedToPlayerID,
		&credential.SourceInviteUseID,
		&credential.ExpiresAt,
		&credential.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Credential{}, ErrInvalidToken
	}
	if err != nil {
		return Credential{}, fmt.Errorf("lock game server registration token: %w", err)
	}
	return credential, nil
}

func (r *Repository) MarkConsumed(
	ctx context.Context,
	tx pgx.Tx,
	credentialID, serverID string,
	now time.Time,
) error {
	tag, err := tx.Exec(ctx, `
		UPDATE game_server_registration_tokens
		SET consumed_at = $3, consumed_server_id = $2
		WHERE id = $1 AND consumed_at IS NULL AND revoked_at IS NULL AND expires_at > $3
	`, credentialID, serverID, now)
	if err != nil {
		return fmt.Errorf("consume game server registration token: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrInvalidToken
	}
	return nil
}
