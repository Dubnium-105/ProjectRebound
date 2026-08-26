package migrations

import (
	"strings"
	"testing"
)

func TestStrictRosterMigrationRejectsPartialSecurityConfirmations(t *testing.T) {
	sqlBytes, err := Files.ReadFile("000042_match_lobbies.sql")
	if err != nil {
		t.Fatal(err)
	}
	sqlText := string(sqlBytes)
	for _, required := range []string{
		"idempotency_request_hash IS NOT NULL",
		"game_binary_sha256 IS NOT NULL",
		"payload_route_generation INTEGER CHECK (payload_route_generation > 0)",
	} {
		if !strings.Contains(sqlText, required) {
			t.Fatalf("strict-roster migration is missing %q", required)
		}
	}
}
