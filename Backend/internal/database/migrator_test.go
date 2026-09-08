package database

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
	"testing/fstest"
)

func TestMigrationChecksumsAreStableAcrossCheckoutLineEndings(t *testing.T) {
	const sql = "CREATE TABLE sample (id INTEGER PRIMARY KEY);\n-- statement-breakpoint\nINSERT INTO sample VALUES (1);\n"
	var loaded [2]migration
	for index, input := range []string{sql, strings.ReplaceAll(sql, "\n", "\r\n")} {
		items, err := loadMigrations(fstest.MapFS{"000001_sample.sql": {Data: []byte(input)}})
		if err != nil {
			t.Fatal(err)
		}
		loaded[index] = items[0]
	}
	if loaded[0].checksum != loaded[1].checksum || loaded[0].sql != loaded[1].sql {
		t.Fatal("the same committed migration changes checksum or executed SQL across LF/CRLF checkouts")
	}
}

func TestMigrationFormatChecksumDoesNotAcceptSQLDrift(t *testing.T) {
	const sql = "CREATE TABLE sample (id INTEGER PRIMARY KEY);\nINSERT INTO sample VALUES (1);\n"
	items, err := loadMigrations(fstest.MapFS{"000001_sample.sql": {Data: []byte(sql)}})
	if err != nil {
		t.Fatal(err)
	}
	for _, encoded := range []string{sql, strings.ReplaceAll(sql, "\n", "\r\n")} {
		if !items[0].matchesChecksum(fmt.Sprintf("%x", sha256.Sum256([]byte(encoded)))) {
			t.Fatal("exact LF/CRLF source checksum was rejected")
		}
	}
	for _, changed := range []string{
		strings.ReplaceAll(sql, "VALUES (1)", "VALUES (2)"),
		sql + "-- another source\n",
		strings.TrimSpace(sql),
		strings.ReplaceAll(sql, "\n", "\r"),
	} {
		for _, encoded := range []string{changed, strings.ReplaceAll(changed, "\n", "\r\n")} {
			if items[0].matchesChecksum(fmt.Sprintf("%x", sha256.Sum256([]byte(encoded)))) {
				t.Fatal("a different migration source was accepted as a line-ending variant")
			}
		}
	}
}

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
