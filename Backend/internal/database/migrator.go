package database

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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

// ErrSchemaNotInitialized is returned by the read-only verification path when
// the control-plane has not created schema_migrations yet. A migrator may
// treat this as permission to perform its first Up; long-running services must
// fail closed on it.
var ErrSchemaNotInitialized = errors.New("schema_migrations table is not initialized")

type migration struct {
	version      int64
	name         string
	sql          string
	checksum     string
	crlfChecksum string
}

// Checkouts must agree on executed SQL and the checksum stored by new
// migrations. Databases created from an exact CRLF checkout of the same SQL
// remain verifiable; SQL changes, names, missing versions and future schemas
// still fail closed. No applied migration is rewritten or executed again.
func (m migration) matchesChecksum(value string) bool {
	return value == m.checksum || (m.crlfChecksum != "" && value == m.crlfChecksum)
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
	// Recheck while holding the same advisory lock used for migration writes.
	// The caller's read-only preflight covers the common path, but only this
	// in-lock check closes the gap where another migrator changes the schema
	// after preflight and before this connection acquires the lock. Incomplete
	// older schemas remain upgradeable; future, unknown, renamed, or drifted
	// records are rejected before applying a single migration.
	actual, err := currentSchemaVersion(ctx, conn)
	if err != nil {
		return err
	}
	expected := all[len(all)-1].version
	if actual > expected {
		return fmt.Errorf("schema version mismatch: database=%d application=%d", actual, expected)
	}
	if err := verifyAppliedMigrationIdentities(ctx, conn, all, false); err != nil {
		return err
	}

	applied, err := appliedMigrations(ctx, conn)
	if err != nil {
		return err
	}
	for _, item := range all {
		if checksum, ok := applied[item.version]; ok {
			if !item.matchesChecksum(checksum) {
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

// VerifyCompatible checks all migration records already present without
// requiring the database to be complete. It is the preflight used immediately
// before Up: a future schema, unknown migration, renamed migration, or
// checksum drift is rejected before an older binary can mutate that database.
// An uninitialized database is allowed so the control-plane can create it.
func (m *Migrator) VerifyCompatible(ctx context.Context) error {
	all, err := loadMigrations(migrations.Files)
	if err != nil {
		return err
	}
	if len(all) == 0 {
		return fmt.Errorf("no embedded migrations are available")
	}
	conn, err := m.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire schema compatibility connection: %w", err)
	}
	defer conn.Release()
	initialized, err := schemaMigrationsTableExists(ctx, conn)
	if err != nil {
		return err
	}
	if !initialized {
		return nil
	}
	actual, err := currentSchemaVersion(ctx, conn)
	if err != nil {
		return err
	}
	expected := all[len(all)-1].version
	if actual > expected {
		return fmt.Errorf("schema version mismatch: database=%d application=%d", actual, expected)
	}
	return verifyAppliedMigrationIdentities(ctx, conn, all, false)
}

// VerifyCurrent rejects a database that is newer than (or otherwise does not
// match) the migration set embedded in this binary. Up intentionally upgrades
// older databases in place; this second gate prevents an older service binary
// from starting against a database whose schema it cannot understand. It also
// verifies every applied migration's name and checksum, so matching only the
// maximum version cannot hide migration-content drift.
func (m *Migrator) VerifyCurrent(ctx context.Context) error {
	all, err := loadMigrations(migrations.Files)
	if err != nil {
		return err
	}
	if len(all) == 0 {
		return fmt.Errorf("no embedded migrations are available")
	}
	expected := all[len(all)-1].version
	conn, err := m.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire schema verification connection: %w", err)
	}
	defer conn.Release()
	initialized, err := schemaMigrationsTableExists(ctx, conn)
	if err != nil {
		return err
	}
	if !initialized {
		return ErrSchemaNotInitialized
	}
	actual, err := currentSchemaVersion(ctx, conn)
	if err != nil {
		return err
	}
	if actual != expected {
		return fmt.Errorf("schema version mismatch: database=%d application=%d", actual, expected)
	}
	return verifyAppliedMigrationIdentities(ctx, conn, all, true)
}

func schemaMigrationsTableExists(ctx context.Context, conn *pgxpool.Conn) (bool, error) {
	var exists bool
	if err := conn.QueryRow(ctx, `
		SELECT to_regclass('public.schema_migrations') IS NOT NULL
	`).Scan(&exists); err != nil {
		return false, fmt.Errorf("check schema_migrations table: %w", err)
	}
	return exists, nil
}

func currentSchemaVersion(ctx context.Context, conn *pgxpool.Conn) (int64, error) {
	var actual int64
	if err := conn.QueryRow(ctx, `
		SELECT COALESCE(MAX(version), 0)
		FROM schema_migrations
	`).Scan(&actual); err != nil {
		return 0, fmt.Errorf("read current schema version: %w", err)
	}
	return actual, nil
}

func verifyAppliedMigrationIdentities(ctx context.Context, conn *pgxpool.Conn, all []migration, requireComplete bool) error {
	expectedByVersion := make(map[int64]migration, len(all))
	for _, item := range all {
		expectedByVersion[item.version] = item
	}
	rows, err := conn.Query(ctx, `
		SELECT version, name, checksum
		FROM schema_migrations
		ORDER BY version
	`)
	if err != nil {
		return fmt.Errorf("read applied migration identities: %w", err)
	}
	defer rows.Close()
	seen := make(map[int64]struct{}, len(all))
	for rows.Next() {
		var version int64
		var name, checksum string
		if err := rows.Scan(&version, &name, &checksum); err != nil {
			return fmt.Errorf("scan applied migration identity: %w", err)
		}
		item, ok := expectedByVersion[version]
		if !ok {
			return fmt.Errorf("schema migration %d is not embedded in this application", version)
		}
		if name != item.name {
			return fmt.Errorf("schema migration %d name changed after application: database=%s application=%s", version, name, item.name)
		}
		if !item.matchesChecksum(checksum) {
			return fmt.Errorf("schema migration %d checksum changed after application: database=%s application=%s", version, checksum, item.checksum)
		}
		seen[version] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate applied migration identities: %w", err)
	}
	if requireComplete {
		for _, item := range all {
			if _, ok := seen[item.version]; !ok {
				return fmt.Errorf("schema migration %d is missing from database", item.version)
			}
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
		sql := strings.ReplaceAll(string(contents), "\r\n", "\n")
		hash := sha256.Sum256([]byte(sql))
		crlfHash := sha256.Sum256([]byte(strings.ReplaceAll(sql, "\n", "\r\n")))
		items = append(items, migration{
			version:      version,
			name:         entry.Name(),
			sql:          sql,
			checksum:     hex.EncodeToString(hash[:]),
			crlfChecksum: hex.EncodeToString(crlfHash[:]),
		})
		seen[version] = entry.Name()
	}
	sort.Slice(items, func(i, j int) bool { return items[i].version < items[j].version })
	return items, nil
}
