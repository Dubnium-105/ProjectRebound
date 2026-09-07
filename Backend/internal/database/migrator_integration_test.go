package database

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/migrations"
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
	var currentVersion int64
	if err := pool.QueryRow(ctx, "SELECT MAX(version) FROM schema_migrations").Scan(&currentVersion); err != nil {
		t.Fatalf("read current migration version: %v", err)
	}
	futureVersion := currentVersion + 1
	if _, err := pool.Exec(ctx, `
		INSERT INTO schema_migrations (version, name, checksum)
		VALUES ($1, 'synthetic_future', repeat('f', 64))
	`, futureVersion); err != nil {
		t.Fatalf("insert synthetic future schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM schema_migrations WHERE version = $1`, futureVersion)
	})
	if err := migrator.VerifyCompatible(ctx); err == nil || !strings.Contains(err.Error(), "schema version mismatch") {
		t.Fatalf("future schema was accepted before migration: %v", err)
	}
	if err := migrator.Up(ctx); err == nil || !strings.Contains(err.Error(), "schema version mismatch") {
		t.Fatalf("future schema was accepted by the in-lock migration gate: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM schema_migrations WHERE version = $1`, futureVersion); err != nil {
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

// TestLiveGenerationMigrationPreservesHistoricalConnectedRoster exercises the
// exact schema-47 -> schema-48 migration against rows that already existed
// before the new live-generation columns were introduced.  The temporary
// tables keep the fixture isolated from the shared integration database while
// retaining the real migration SQL and its HOST/MEMBER backfill semantics.
func TestLiveGenerationMigrationPreservesHistoricalConnectedRoster(t *testing.T) {
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

	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire PostgreSQL connection: %v", err)
	}
	defer conn.Release()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin migration fixture transaction: %v", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	if _, err := tx.Exec(ctx, `
		CREATE TEMP TABLE match_attempts (
			id VARCHAR(64) PRIMARY KEY,
			route_generation INTEGER NOT NULL
		) ON COMMIT DROP;
		CREATE TEMP TABLE match_attempt_roster (
			attempt_id VARCHAR(64) NOT NULL,
			room_role VARCHAR(16) NOT NULL,
			connection_state VARCHAR(32) NOT NULL,
			connection_generation INTEGER NOT NULL
		) ON COMMIT DROP;
		INSERT INTO match_attempts (id, route_generation)
		VALUES ('attempt-schema47-history', 7);
		INSERT INTO match_attempt_roster (attempt_id, room_role, connection_state, connection_generation)
		VALUES
			('attempt-schema47-history', 'HOST', 'CONNECTED', 11),
			('attempt-schema47-history', 'MEMBER', 'CONNECTED', 12),
			('attempt-schema47-history', 'MEMBER', 'CONNECTING', 13),
			('attempt-schema47-history', 'HOST', 'DISCONNECTED', 14)
	`); err != nil {
		t.Fatalf("seed schema-47 historical roster: %v", err)
	}

	all, err := loadMigrations(migrations.Files)
	if err != nil {
		t.Fatalf("load migrations: %v", err)
	}
	var liveGenerationMigration migration
	for _, item := range all {
		if item.version == 48 {
			liveGenerationMigration = item
			break
		}
	}
	if liveGenerationMigration.version != 48 {
		t.Fatal("schema-48 live-generation migration is not embedded")
	}
	for _, statement := range migrationStatements(liveGenerationMigration.sql) {
		if _, err := tx.Exec(ctx, statement); err != nil {
			t.Fatalf("apply schema-48 migration statement: %v", err)
		}
	}

	rows, err := tx.Query(ctx, `
		SELECT room_role, connection_state,
		       live_connection_generation, live_route_generation,
		       host_live_scope_preserved
		FROM match_attempt_roster
		WHERE attempt_id = 'attempt-schema47-history'
		ORDER BY room_role, connection_state, connection_generation
	`)
	if err != nil {
		t.Fatalf("read migrated historical roster: %v", err)
	}
	defer rows.Close()
	type migratedSeat struct {
		role, state               string
		liveGeneration, liveRoute *int
		hostScopePreserved        bool
	}
	var seats []migratedSeat
	for rows.Next() {
		var seat migratedSeat
		if err := rows.Scan(&seat.role, &seat.state, &seat.liveGeneration, &seat.liveRoute, &seat.hostScopePreserved); err != nil {
			t.Fatalf("scan migrated historical roster: %v", err)
		}
		seats = append(seats, seat)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate migrated historical roster: %v", err)
	}
	if len(seats) != 4 {
		t.Fatalf("migrated historical roster rows = %d, want 4", len(seats))
	}
	for _, seat := range seats {
		if seat.state == "CONNECTED" {
			if seat.liveGeneration == nil || seat.liveRoute == nil || *seat.liveRoute != 7 {
				t.Fatalf("connected %s seat lost live route/generation: %+v", seat.role, seat)
			}
			wantGeneration := 11
			wantPreserved := seat.role == "HOST"
			if seat.role == "MEMBER" {
				wantGeneration = 12
			}
			if *seat.liveGeneration != wantGeneration || seat.hostScopePreserved != wantPreserved {
				t.Fatalf("connected %s seat backfill = %+v, want generation=%d preserved=%t", seat.role, seat, wantGeneration, wantPreserved)
			}
		} else if seat.liveGeneration != nil || seat.liveRoute != nil || seat.hostScopePreserved {
			t.Fatalf("non-connected %s seat incorrectly received live scope: %+v", seat.role, seat)
		}
	}
}
