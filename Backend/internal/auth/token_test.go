package auth

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/config"
)

func TestAccessTokenRoundTripAndTamperDetection(t *testing.T) {
	manager, ephemeral, err := NewTokenManager(config.Defaults.Auth, "development")
	if err != nil || !ephemeral {
		t.Fatalf("NewTokenManager() = %v, %v", ephemeral, err)
	}
	now := time.Unix(1_800_000_000, 0).UTC()
	manager.now = func() time.Time { return now }
	token, expiresAt, err := manager.Sign("p_test", "ses_test", "steam_client_asserted", "unverified", 1, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := manager.Verify(token)
	if err != nil || claims.Subject != "p_test" || claims.SessionID != "ses_test" || !expiresAt.Equal(now.Add(15*time.Minute)) {
		t.Fatalf("Verify() = %#v, %v", claims, err)
	}
	parts := strings.Split(token, ".")
	replacement := "A"
	if strings.HasSuffix(parts[1], replacement) {
		replacement = "B"
	}
	parts[1] = parts[1][:len(parts[1])-1] + replacement
	if _, err := manager.Verify(strings.Join(parts, ".")); err == nil {
		t.Fatal("tampered token was accepted")
	}
}

func TestPublicKeyVerifierCannotSign(t *testing.T) {
	cfg := config.Defaults.Auth
	signer, _, err := NewTokenManager(cfg, "development")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0).UTC()
	signer.now = func() time.Time { return now }
	token, _, err := signer.Sign(
		"p_test", "ses_test", "steam_client_asserted", "unverified", 1, time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	cfg.AccessTokenPublicKeyBase64 = base64.StdEncoding.EncodeToString(signer.publicKey)
	cfg.AccessTokenPrivateKeyBase64 = ""
	verifier, err := NewTokenVerifier(cfg)
	if err != nil {
		t.Fatal(err)
	}
	verifier.now = func() time.Time { return now }
	if _, err := verifier.Verify(token); err != nil {
		t.Fatalf("public verifier rejected signed token: %v", err)
	}
	if _, _, err := verifier.Sign(
		"p_test", "ses_test", "steam_client_asserted", "unverified", 1, time.Minute,
	); err == nil {
		t.Fatal("verifier-only token manager signed a token")
	}
}

func TestRefreshTokenHasHighEntropyAndStableHash(t *testing.T) {
	token, hash, err := NewRefreshToken()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(token, "rfr_") || len(token) < 64 || len(hash) != 32 {
		t.Fatalf("unexpected refresh token shape: len(token)=%d len(hash)=%d", len(token), len(hash))
	}
	if string(hash) != string(HashRefreshToken(token)) {
		t.Fatal("refresh token hash is not stable")
	}
}
