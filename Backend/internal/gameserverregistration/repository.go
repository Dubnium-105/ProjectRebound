package gameserverregistration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
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
		SELECT source_invite_use_id
		FROM player_feature_grants
		WHERE player_id = $1 AND capability = 'game_server_registration'
		  AND (expires_at IS NULL OR expires_at > NOW())
	`, playerID).Scan(&inviteUseID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrInvalidInviteGrant
	}
	if err != nil {
		return "", fmt.Errorf("find game server invitation grant: %w", err)
	}
	return inviteUseID, nil
}

func (r *Repository) RedeemPlayerInviteGrant(
	ctx context.Context,
	tx pgx.Tx,
	plaintext, playerID, steamID, ipAddress string,
	now time.Time,
) (string, error) {
	plaintext = strings.ToUpper(strings.TrimSpace(plaintext))
	if plaintext == "" || len(plaintext) > 128 {
		return "", ErrInvalidInviteGrant
	}
	codeHash := sha256.Sum256([]byte(plaintext))
	var inviteID string
	var maxUses, usedCount int
	var permissions []byte
	var expiresAt sql.NullTime
	err := tx.QueryRow(ctx, `
		SELECT id, max_uses, used_count, permissions, expires_at
		FROM invite_codes
		WHERE code_hash = $1
		  AND enabled = TRUE
		  AND revoked_at IS NULL
		  AND (expires_at IS NULL OR expires_at > $2)
		  AND used_count < max_uses
		  AND permissions @> '{"allow_game_server_registration": true}'::jsonb
		FOR UPDATE
	`, codeHash[:], now).Scan(&inviteID, &maxUses, &usedCount, &permissions, &expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrInvalidInviteGrant
	}
	if err != nil {
		return "", fmt.Errorf("lock dedicated server invitation: %w", err)
	}
	if usedCount >= maxUses {
		return "", ErrInvalidInviteGrant
	}
	tag, err := tx.Exec(ctx, `
		UPDATE invite_codes
		SET used_count = used_count + 1, updated_at = $2
		WHERE id = $1 AND used_count < max_uses
	`, inviteID, now)
	if err != nil {
		return "", fmt.Errorf("consume dedicated server invitation: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return "", ErrInvalidInviteGrant
	}
	useID := "icu_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = tx.Exec(ctx, `
		INSERT INTO invite_code_uses (
			id, invite_code_id, player_id, steam_id, ip_address, used_at, result,
			permission_snapshot
		) VALUES ($1, $2, $3, $4, NULLIF($5, '')::inet, $6, 'SUCCESS', $7::jsonb)
	`, useID, inviteID, playerID, steamID, ipAddress, now, permissions)
	if err != nil {
		return "", fmt.Errorf("record dedicated server invitation use: %w", err)
	}
	var permissionExpiresAt *time.Time
	if expiresAt.Valid {
		value := expiresAt.Time
		permissionExpiresAt = &value
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO player_feature_grants (
			player_id, capability, source_invite_use_id, granted_at, expires_at
		) VALUES ($1, 'game_server_registration', $2, $3, $4)
		ON CONFLICT (player_id, capability) DO UPDATE SET
			source_invite_use_id = EXCLUDED.source_invite_use_id,
			granted_at = EXCLUDED.granted_at,
			expires_at = EXCLUDED.expires_at
		WHERE player_feature_grants.expires_at IS NOT NULL
		  AND (EXCLUDED.expires_at IS NULL OR EXCLUDED.expires_at > player_feature_grants.expires_at)
	`, playerID, useID, now, permissionExpiresAt)
	if err != nil {
		return "", fmt.Errorf("grant dedicated server registration permission: %w", err)
	}
	return useID, nil
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
