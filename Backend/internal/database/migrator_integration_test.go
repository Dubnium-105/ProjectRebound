package database

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMigratorAgainstPostgreSQL(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open PostgreSQL: %v", err)
	}
	defer pool.Close()

	migrator := NewMigrator(pool)
	if err := migrator.Up(ctx); err != nil {
		t.Fatalf("first migration run: %v", err)
	}
	if err := migrator.Up(ctx); err != nil {
		t.Fatalf("idempotent migration run: %v", err)
	}

	var count int
	if err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&count); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if count < 1 {
		t.Fatalf("applied migration count = %d", count)
	}
	if err := migrator.VerifyCurrent(ctx); err != nil {
		t.Fatalf("verify current schema: %v", err)
	}
	if err := migrator.VerifyCompatible(ctx); err != nil {
		t.Fatalf("verify compatible schema: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO schema_migrations (version, name, checksum)
		VALUES (48, 'synthetic_future', repeat('f', 64))
	`); err != nil {
		t.Fatalf("insert synthetic future schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM schema_migrations WHERE version = 48`)
	})
	if err := migrator.VerifyCompatible(ctx); err == nil || !strings.Contains(err.Error(), "schema version mismatch") {
		t.Fatalf("future schema was accepted before migration: %v", err)
	}
	if err := migrator.Up(ctx); err == nil || !strings.Contains(err.Error(), "schema version mismatch") {
		t.Fatalf("future schema was accepted by the in-lock migration gate: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM schema_migrations WHERE version = 48`); err != nil {
		t.Fatalf("remove synthetic future schema: %v", err)
	}
	var originalChecksum string
	if err := pool.QueryRow(ctx, `SELECT checksum FROM schema_migrations WHERE version = 1`).Scan(&originalChecksum); err != nil {
		t.Fatalf("read migration checksum: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `UPDATE schema_migrations SET checksum = $1 WHERE version = 1`, originalChecksum)
	})
	if _, err := pool.Exec(ctx, `UPDATE schema_migrations SET checksum = repeat('0', 64) WHERE version = 1`); err != nil {
		t.Fatalf("introduce checksum drift: %v", err)
	}
	if err := migrator.VerifyCurrent(ctx); err == nil || !strings.Contains(err.Error(), "checksum changed") {
		t.Fatalf("checksum drift was accepted: %v", err)
	}
	if err := migrator.Up(ctx); err == nil || !strings.Contains(err.Error(), "checksum changed") {
		t.Fatalf("checksum drift was accepted by the in-lock migration gate: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE schema_migrations SET checksum = $1 WHERE version = 1`, originalChecksum); err != nil {
		t.Fatalf("restore migration checksum: %v", err)
	}
	if err := migrator.VerifyCurrent(ctx); err != nil {
		t.Fatalf("verify restored schema: %v", err)
	}
}
