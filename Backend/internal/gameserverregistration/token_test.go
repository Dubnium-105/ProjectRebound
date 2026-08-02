package gameserverregistration

import "testing"

func TestGeneratedTokenHasExpectedShapeAndHash(t *testing.T) {
	first, firstHash, err := GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	second, secondHash, err := GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	if !HasValidShape(first) || !HasValidShape(second) {
		t.Fatalf("unexpected token shape")
	}
	if first == second || string(firstHash) == string(secondHash) {
		t.Fatal("generated duplicate registration token")
	}
	if string(firstHash) != string(HashToken(first)) {
		t.Fatal("registration token hash mismatch")
	}
}

func TestRegistrationTokenShapeRejectsServerAndLegacyTokens(t *testing.T) {
	for _, token := range []string{
		"",
		"gst_abcdefghijklmnopqrstuvwxyz",
		"legacy-registration-token-with-at-least-32-bytes",
		"gsr_short",
	} {
		if HasValidShape(token) {
			t.Fatalf("accepted invalid registration token %q", token)
		}
	}
}
