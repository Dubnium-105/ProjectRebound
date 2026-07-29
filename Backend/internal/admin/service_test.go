package admin

import (
	"testing"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/player"
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

func TestValidateAuditReasonRejectsMissingAndCredentialLikeText(t *testing.T) {
	if _, err := validateAuditReason(" "); err == nil {
		t.Fatal("missing reason was accepted")
	}
	if _, err := validateAuditReason("Authorization: Bearer secret"); err == nil {
		t.Fatal("credential-like reason was accepted")
	}
	if reason, err := validateAuditReason("  Work order OPS-42  "); err != nil || reason != "Work order OPS-42" {
		t.Fatalf("valid reason = %q, %v", reason, err)
	}
}
