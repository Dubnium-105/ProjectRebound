package database

import (
	"testing"
	"testing/fstest"
)

func TestLoadMigrationsSortsAndChecksums(t *testing.T) {
	items, err := loadMigrations(fstest.MapFS{
		"000002_second.sql": {Data: []byte("SELECT 2;")},
		"000001_first.sql":  {Data: []byte("SELECT 1;")},
	})
	if err != nil {
		t.Fatalf("loadMigrations() error = %v", err)
	}
	if len(items) != 2 || items[0].version != 1 || len(items[0].checksum) != 64 {
		t.Fatalf("unexpected migrations: %#v", items)
	}
}

func TestLoadMigrationsRejectsDuplicateVersion(t *testing.T) {
	_, err := loadMigrations(fstest.MapFS{
		"000001_one.sql": {Data: []byte("SELECT 1;")},
		"000001_two.sql": {Data: []byte("SELECT 2;")},
	})
	if err == nil {
		t.Fatal("loadMigrations() returned nil error")
	}
}

func TestMigrationStatementsSupportsExplicitBreakpoints(t *testing.T) {
	statements := migrationStatements("SELECT 1;\n-- statement-breakpoint\nSELECT 2;")
	if len(statements) != 2 {
		t.Fatalf("len(statements) = %d", len(statements))
	}
}
