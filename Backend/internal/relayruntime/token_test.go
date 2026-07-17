package relayruntime

import (
	"testing"
	"time"
)

func TestTokenVerifierKeepsCachedKeysDuringControlDisconnect(t *testing.T) {
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	signer := newTestSigner(t, "relay-key-a")
	verifier, err := NewTokenVerifier(Keyset{Keys: []PublicKey{signer.key()}})
	if err != nil {
		t.Fatal(err)
	}
	token := signer.sign(t, testClaims(now, "offline-token", "HOST", "relay_test", "alloc_offline"))
	if _, err := verifier.Verify(token, "relay_test", now); err != nil {
		t.Fatalf("cached key did not verify a token: %v", err)
	}
	if _, err := verifier.Verify(token, "relay_test", now.Add(3*time.Minute)); err == nil {
		t.Fatal("cached key bypassed token expiry")
	}
}
