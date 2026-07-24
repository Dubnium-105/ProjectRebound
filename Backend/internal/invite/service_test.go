package invite

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestPlaintextCodeGenerationAndHashing(t *testing.T) {
	first, err := newPlaintextCode()
	if err != nil {
		t.Fatal(err)
	}
	second, err := newPlaintextCode()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(first, "INV-") || first == second {
		t.Fatalf("generated codes first=%q second=%q", first, second)
	}
	if !bytes.Equal(hashCode(first), hashCode(strings.ToLower(first))) || bytes.Contains(hashCode(first), []byte(first)) {
		t.Fatal("invite code hash is not normalized and one-way")
	}
}

func TestInvalidCodeErrorCarriesAuthenticationMarker(t *testing.T) {
	var marker interface{ InvalidInviteCode() bool }
	if !errors.As(ErrInvalidCode, &marker) || !marker.InvalidInviteCode() {
		t.Fatal("ErrInvalidCode does not carry the authentication marker")
	}
}

func TestValidateReasonRejectsCredentials(t *testing.T) {
	if _, err := validateReason(""); err == nil {
		t.Fatal("missing reason was accepted")
	}
	if _, err := validateReason("token=secret"); err == nil {
		t.Fatal("credential-like reason was accepted")
	}
	if reason, err := validateReason("  Campaign launch  "); err != nil || reason != "Campaign launch" {
		t.Fatalf("valid reason = %q, %v", reason, err)
	}
}

func TestCreateRejectsOversizedBatchBeforeDatabaseAccess(t *testing.T) {
	service := &Service{}
	_, err := service.Create(context.Background(), CreateInput{
		BatchName: "campaign", Quantity: 101, MaxUses: 1, Reason: "Campaign launch",
	}, RequestMeta{AdminID: "adm_test"})
	var serviceError *ServiceError
	if !errors.As(err, &serviceError) || serviceError.Code != "INVALID_REQUEST" {
		t.Fatalf("Create error = %#v, want INVALID_REQUEST", err)
	}
}

func TestMaskIPAddressReturnsNetworkSummary(t *testing.T) {
	for input, expected := range map[string]string{
		"203.0.113.42":          "203.0.113.0/24",
		"2001:db8:abcd:1234::9": "2001:db8:abcd::/48",
		"not-an-ip":             "",
	} {
		if actual := maskIPAddress(input); actual != expected {
			t.Errorf("maskIPAddress(%q) = %q, want %q", input, actual, expected)
		}
	}
}
