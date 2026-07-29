package database

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"

	"github.com/Dubnium-105/ProjectRebound/Backend/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const migrationLockID int64 = 727_300_101

type migration struct {
	version  int64
	name     string
	sql      string
	checksum string
}

type Migrator struct {
	pool *pgxpool.Pool
}

func NewMigrator(pool *pgxpool.Pool) *Migrator {
	return &Migrator{pool: pool}
}

func (m *Migrator) Up(ctx context.Context) error {
	all, err := loadMigrations(migrations.Files)
	if err != nil {
		return err
	}

	conn, err := m.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationLockID); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	defer func() { _, _ = conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", migrationLockID) }()

	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version BIGINT PRIMARY KEY,
		name TEXT NOT NULL,
		checksum CHAR(64) NOT NULL,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	applied, err := appliedMigrations(ctx, conn)
	if err != nil {
		return err
	}
	for _, item := range all {
		if checksum, ok := applied[item.version]; ok {
			if checksum != item.checksum {
				return fmt.Errorf("migration %d checksum changed after application", item.version)
			}
			continue
		}
		if err := applyMigration(ctx, conn, item); err != nil {
			return err
		}
	}
	return nil
}

func appliedMigrations(ctx context.Context, conn *pgxpool.Conn) (map[int64]string, error) {
	rows, err := conn.Query(ctx, "SELECT version, checksum FROM schema_migrations ORDER BY version")
	if err != nil {
		return nil, fmt.Errorf("read applied migrations: %w", err)
	}
	defer rows.Close()

	applied := make(map[int64]string)
	for rows.Next() {
		var version int64
		var checksum string
		if err := rows.Scan(&version, &checksum); err != nil {
			return nil, fmt.Errorf("scan applied migration: %w", err)
		}
		applied[version] = checksum
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate applied migrations: %w", err)
	}
	return applied, nil
}

func applyMigration(ctx context.Context, conn *pgxpool.Conn, item migration) error {
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin migration %d: %w", item.version, err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	for _, statement := range migrationStatements(item.sql) {
		if _, err := tx.Exec(ctx, statement); err != nil {
			return fmt.Errorf("execute migration %d (%s): %w", item.version, item.name, err)
		}
	}
	if _, err := tx.Exec(ctx,
		"INSERT INTO schema_migrations (version, name, checksum) VALUES ($1, $2, $3)",
		item.version, item.name, item.checksum,
	); err != nil {
		return fmt.Errorf("record migration %d: %w", item.version, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit migration %d: %w", item.version, err)
	}
	return nil
}

func migrationStatements(sql string) []string {
	parts := strings.Split(sql, "-- statement-breakpoint")
	statements := make([]string, 0, len(parts))
	for _, part := range parts {
		if statement := strings.TrimSpace(part); statement != "" {
			statements = append(statements, statement)
		}
	}
	return statements
}

func loadMigrations(source fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(source, ".")
	if err != nil {
		return nil, fmt.Errorf("list migrations: %w", err)
	}
	items := make([]migration, 0, len(entries))
	seen := make(map[int64]string)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		prefix, _, ok := strings.Cut(entry.Name(), "_")
		if !ok {
			return nil, fmt.Errorf("migration %q must start with a numeric version and underscore", entry.Name())
		}
		version, err := strconv.ParseInt(prefix, 10, 64)
		if err != nil || version < 1 {
			return nil, fmt.Errorf("migration %q has invalid version", entry.Name())
		}
		if previous, ok := seen[version]; ok {
			return nil, fmt.Errorf("migrations %q and %q use duplicate version %d", previous, entry.Name(), version)
		}
		contents, err := fs.ReadFile(source, entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", entry.Name(), err)
		}
		hash := sha256.Sum256(contents)
		items = append(items, migration{
			version:  version,
			name:     entry.Name(),
			sql:      string(contents),
			checksum: hex.EncodeToString(hash[:]),
		})
		seen[version] = entry.Name()
	}
	sort.Slice(items, func(i, j int) bool { return items[i].version < items[j].version })
	return items, nil
}
