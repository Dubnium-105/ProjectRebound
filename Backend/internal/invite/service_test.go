package invite

import (
	"bytes"
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
