package relayruntime

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
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

func TestTokenVerifierRequiresTrustedKeysetSignature(t *testing.T) {
	first := newTestSigner(t, "first")
	second := newTestSigner(t, "second")
	verifier, err := NewTokenVerifier(Keyset{Keys: []PublicKey{first.key()}})
	if err != nil {
		t.Fatal(err)
	}
	keyset := Keyset{Version: 1, GeneratedAt: time.Now().UTC(), SignedBy: "first", Keys: []PublicKey{first.key(), second.key()}}
	body, _ := json.Marshal(keyset)
	keyset.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(first.privateKey, body))
	if err := verifier.Update(keyset); err != nil {
		t.Fatal(err)
	}
	tampered := keyset
	tampered.Version = 2
	if err := verifier.Update(tampered); err == nil {
		t.Fatal("tampered keyset was accepted")
	}
}
