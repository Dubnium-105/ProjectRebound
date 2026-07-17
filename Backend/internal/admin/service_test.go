package admin

import (
	"testing"

	"github.com/projectrebound/matchserver/internal/player"
)

func TestParseAccountStatusNormalizesAndRejectsUnknown(t *testing.T) {
	status, err := parseAccountStatus(" banned ", false)
	if err != nil || status != player.AccountStatusBanned {
		t.Fatalf("parseAccountStatus() = %q, %v", status, err)
	}
	if _, err := parseAccountStatus("SUSPENDED", false); err == nil {
		t.Fatal("unknown status was accepted")
	}
}
