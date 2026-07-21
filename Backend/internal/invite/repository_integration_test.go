package invite

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/projectrebound/matchserver/internal/database"
)

func TestConcurrentConsumeAllowsOnlyFinalSlotOnce(t *testing.T) {
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
	t.Cleanup(pool.Close)
	if err := database.NewMigrator(pool).Up(ctx); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}

	repository := NewRepository(pool)
	suffix := uuid.NewString()
	inviteID := "inv_" + stringsNoHyphens(suffix)
	playerIDs := []string{"p_" + stringsNoHyphens(uuid.NewString()), "p_" + stringsNoHyphens(uuid.NewString())}
	steamIDs := []string{integrationSteamID(1), integrationSteamID(2)}
	plaintext := "INV-TEST-LAST-SLOT"
	now := time.Now().UTC()

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.Insert(ctx, tx, Code{
		ID: inviteID, BatchName: "integration", MaxUses: 1, Enabled: true,
		Permissions: map[string]any{}, CreatedBy: "test", CreatedAt: now, UpdatedAt: now,
	}, hashCode(plaintext)); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	for index := range playerIDs {
		_, err := tx.Exec(ctx, `
			INSERT INTO players (
				id, steam_id, persona_name, account_status, is_vip,
				auth_provider, auth_level, last_login_at, created_at, updated_at
			) VALUES ($1, $2, 'Invite Test', 'ACTIVE', FALSE,
			          'steam_client_asserted', 'unverified', $3, $3, $3)
		`, playerIDs[index], steamIDs[index], now)
		if err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM invite_code_uses WHERE invite_code_id = $1", inviteID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM invite_codes WHERE id = $1", inviteID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM players WHERE id = ANY($1)", playerIDs)
	})

	var successes atomic.Int32
	var wait sync.WaitGroup
	errorsCh := make(chan error, 2)
	for index := range playerIDs {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			consumeTx, err := pool.BeginTx(ctx, pgx.TxOptions{})
			if err != nil {
				errorsCh <- err
				return
			}
			defer func() { _ = consumeTx.Rollback(context.WithoutCancel(ctx)) }()
			err = repository.Consume(ctx, consumeTx, hashCode(plaintext), playerIDs[index], steamIDs[index], "192.0.2.10", now)
			if errors.Is(err, ErrInvalidCode) {
				return
			}
			if err != nil {
				errorsCh <- err
				return
			}
			if err := consumeTx.Commit(ctx); err != nil {
				errorsCh <- err
				return
			}
			successes.Add(1)
		}(index)
	}
	wait.Wait()
	close(errorsCh)
	for err := range errorsCh {
		t.Error(err)
	}
	if successes.Load() != 1 {
		t.Fatalf("successful consumes = %d", successes.Load())
	}
	var usedCount, useRows int
	if err := pool.QueryRow(ctx, "SELECT used_count FROM invite_codes WHERE id = $1", inviteID).Scan(&usedCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM invite_code_uses WHERE invite_code_id = $1", inviteID).Scan(&useRows); err != nil {
		t.Fatal(err)
	}
	if usedCount != 1 || useRows != 1 {
		t.Fatalf("used_count=%d use_rows=%d", usedCount, useRows)
	}
	var storedHash []byte
	if err := pool.QueryRow(ctx, "SELECT code_hash FROM invite_codes WHERE id = $1", inviteID).Scan(&storedHash); err != nil {
		t.Fatal(err)
	}
	if string(storedHash) == plaintext || len(storedHash) != 32 {
		t.Fatal("invite plaintext was stored")
	}
}

func stringsNoHyphens(value string) string {
	result := make([]byte, 0, len(value))
	for _, character := range []byte(value) {
		if character != '-' {
			result = append(result, character)
		}
	}
	return string(result)
}

func integrationSteamID(offset int64) string {
	return fmt.Sprintf("%017d", (time.Now().UnixNano()+offset)%100_000_000_000_000_000)
}
