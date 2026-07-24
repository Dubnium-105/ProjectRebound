package admin

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestGovernanceNormalizersPreservePermissionKeyCase(t *testing.T) {
	roles := normalizedUniqueRoleNames([]string{" viewer ", "VIEWER", "operations"})
	if strings.Join(roles, ",") != "OPERATIONS,VIEWER" {
		t.Fatalf("normalized roles = %#v", roles)
	}
	permissions := normalizedUniquePermissionKeys([]string{
		" players.read ",
		"players.read",
		"updates.publish",
	})
	if strings.Join(permissions, ",") != "players.read,updates.publish" {
		t.Fatalf("normalized permission keys = %#v", permissions)
	}
}

func TestGovernedAdministratorJSONUsesSafePublicFields(t *testing.T) {
	encoded, err := json.Marshal(GovernedAdmin{
		ID:          "adm_test",
		Username:    "operator",
		DisplayName: "Operator",
		Status:      AdminStatusActive,
		MFAEnabled:  true,
		Roles:       []string{"VIEWER"},
		CreatedAt:   time.Unix(1, 0).UTC(),
		UpdatedAt:   time.Unix(2, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	body := string(encoded)
	for _, expected := range []string{
		`"id":"adm_test"`,
		`"display_name":"Operator"`,
		`"mfa_enabled":true`,
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("public JSON is missing %s: %s", expected, body)
		}
	}
	for _, forbidden := range []string{
		"DisplayName",
		"password",
		"secret",
		"recovery",
	} {
		if strings.Contains(body, forbidden) {
			t.Errorf("public JSON contains forbidden field %q: %s", forbidden, body)
		}
	}
}

func TestGovernanceAuditValueDoesNotContainCredentials(t *testing.T) {
	encoded, err := json.Marshal(governedAdminAuditValue(GovernedAdmin{
		Username: "operator", DisplayName: "Operator", Status: AdminStatusActive,
		MFAEnabled: true, Roles: []string{"OPERATIONS"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	body := string(encoded)
	for _, forbidden := range []string{"password", "secret", "recovery", "totp"} {
		if strings.Contains(strings.ToLower(body), forbidden) {
			t.Fatalf("audit value contains credential-like key %q: %s", forbidden, body)
		}
	}
}
