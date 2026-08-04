package entitlement

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestGrantExpiryCanOnlyExtendAndPermanentWins(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := database.NewMigrator(pool).Up(ctx); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	suffix := uuid.NewString()
	playerID := "p_" + suffix
	inviteID := "inv_" + suffix
	inviteUseID := "icu_" + suffix
	now := time.Now().UTC()
	steamID := fmt.Sprintf("%017d", now.UnixNano()%100_000_000_000_000_000)
	if _, err := tx.Exec(ctx, `
		INSERT INTO players (
			id, steam_id, persona_name, account_status, is_vip,
			auth_provider, auth_level, last_login_at, created_at, updated_at
		) VALUES ($1, $2, 'Entitlement Test', 'ACTIVE', FALSE,
		          'steam_client_asserted', 'unverified', $3, $3, $3)
	`, playerID, steamID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO invite_codes (
			id, code_hash, batch_name, max_uses, used_count, enabled,
			permissions, created_by, created_at, updated_at
		) VALUES ($1, $2, 'entitlement-test', 10, 1, TRUE, '{}'::jsonb, 'test', $3, $3)
	`, inviteID, []byte("entitlement-"+suffix), now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO invite_code_uses (
			id, invite_code_id, player_id, steam_id, used_at, result, permission_snapshot
		) VALUES ($1, $2, $3, $4, $5, 'SUCCESS', '{}'::jsonb)
	`, inviteUseID, inviteID, playerID, steamID, now); err != nil {
		t.Fatal(err)
	}

	repository := NewRepository(pool)
	permissions := map[string]any{InviteAllowP2PRoom: true}
	shortExpiry := now.Add(time.Hour)
	if err := repository.GrantFromInvite(ctx, tx, playerID, inviteUseID, permissions, &shortExpiry, now); err != nil {
		t.Fatal(err)
	}
	earlierExpiry := now.Add(30 * time.Minute)
	if err := repository.GrantFromInvite(ctx, tx, playerID, inviteUseID, permissions, &earlierExpiry, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	assertGrantExpiry(t, ctx, tx, playerID, P2PRoomRegistration, shortExpiry, true)

	longExpiry := now.Add(2 * time.Hour)
	if err := repository.GrantFromInvite(ctx, tx, playerID, inviteUseID, permissions, &longExpiry, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	assertGrantExpiry(t, ctx, tx, playerID, P2PRoomRegistration, longExpiry, true)

	if err := repository.GrantFromInvite(ctx, tx, playerID, inviteUseID, permissions, nil, now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	assertGrantExpiry(t, ctx, tx, playerID, P2PRoomRegistration, time.Time{}, false)
	if err := repository.GrantFromInvite(ctx, tx, playerID, inviteUseID, permissions, &longExpiry, now.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	assertGrantExpiry(t, ctx, tx, playerID, P2PRoomRegistration, time.Time{}, false)

	expired := now.Add(-time.Hour)
	expiredGrantedAt := now.Add(-2 * time.Hour)
	if err := repository.GrantFromInvite(ctx, tx, playerID, inviteUseID,
		map[string]any{InviteAllowVNTNode: true}, &expired, expiredGrantedAt); err != nil {
		t.Fatal(err)
	}
	allowed, err := repository.HasWith(ctx, tx, playerID, VNTNodeRegistration)
	if err != nil {
		t.Fatal(err)
	}
	if allowed {
		t.Fatal("expired capability was treated as active")
	}
}

func assertGrantExpiry(
	t *testing.T,
	ctx context.Context,
	tx pgx.Tx,
	playerID, capability string,
	want time.Time,
	wantValid bool,
) {
	t.Helper()
	var got sql.NullTime
	if err := tx.QueryRow(ctx, `
		SELECT expires_at FROM player_feature_grants
		WHERE player_id = $1 AND capability = $2
	`, playerID, capability).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got.Valid != wantValid {
		t.Fatalf("expires_at validity = %v, want %v", got.Valid, wantValid)
	}
	if wantValid && !got.Time.Equal(want) {
		t.Fatalf("expires_at = %v, want %v", got.Time, want)
	}
}
