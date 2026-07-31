package p2pbattlelog

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/Dubnium-105/ProjectRebound/Backend/migrations"
)

func TestMigrationKeepsP2PEvidenceSeparateFromDedicatedBattleLog(t *testing.T) {
	raw, err := fs.ReadFile(migrations.Files, "000032_p2p_battlelog.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(raw)
	for _, required := range []string{
		"CREATE TABLE p2p_match_sessions",
		"CREATE TABLE p2p_match_roster",
		"CREATE TABLE p2p_match_report_capabilities",
		"CREATE TABLE p2p_battlelog_reports",
		"CREATE TABLE p2p_battlelog_matches",
		"CREATE TABLE p2p_battlelog_participants",
		"CREATE TABLE p2p_battlelog_decisions",
		"p2p.battlelog.raw.read",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("migration does not contain %q", required)
		}
	}
	for _, forbidden := range []string{
		"ALTER TABLE battlelog_",
		"INSERT INTO battlelog_matches",
		"INSERT INTO battlelog_participants",
	} {
		if strings.Contains(sql, forbidden) {
			t.Errorf("P2P migration mutates dedicated BattleLog through %q", forbidden)
		}
	}
}
