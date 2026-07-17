package database

import (
	"context"
	"os"
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
}
