package diagnostic

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/Dubnium-105/ProjectRebound/Backend/migrations"
)

func TestDiagnosticReportMigrationKeepsRawReportUnindexed(t *testing.T) {
	raw, err := fs.ReadFile(migrations.Files, "000033_diagnostic_reports.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(raw)
	for _, required := range []string{
		"CREATE TABLE diagnostic_reports",
		"id SERIAL PRIMARY KEY",
		"player_id TEXT NOT NULL",
		"report TEXT NOT NULL",
		"created_at TIMESTAMPTZ DEFAULT now()",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("migration does not contain %q", required)
		}
	}
	if strings.Contains(strings.ToUpper(sql), "CREATE INDEX") {
		t.Error("diagnostic report migration must not index report content")
	}
}
