package relayregistry

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/config"
)

func TestRelayTokenIsSignedScopedAndExpiring(t *testing.T) {
	manager, err := NewRelayTokenManager(config.Defaults.RelayRegistry, "development")
	if err != nil || !manager.Ephemeral() {
		t.Fatalf("manager = %#v, %v", manager, err)
	}
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return now }
	base := RelayClaims{
		RelayNodeID: "relay_a", AllocationID: "alloc_a", ConnectionID: "conn_a", RoomID: "room_a",
		Protocol: "UDP", MaxBPS: 256000, MaxPPS: 200, MaxTotalBytes: 268435456,
		AllocationExpiresAt: now.Add(30 * time.Minute).Unix(),
	}
	host := base
	host.EndpointRole = "HOST"
	hostToken, expiresAt, err := manager.Sign(host, 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	peer := base
	peer.EndpointRole = "PEER"
	peerToken, _, err := manager.Sign(peer, 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if hostToken == peerToken || expiresAt != now.Add(2*time.Minute) {
		t.Fatalf("tokens were not independently scoped: %v", expiresAt)
	}
	claims, err := manager.Verify(hostToken, "relay_a")
	if err != nil || claims.EndpointRole != "HOST" || claims.TokenID == "" {
		t.Fatalf("claims = %#v, %v", claims, err)
	}
	if _, err := manager.Verify(hostToken, "relay_b"); err == nil {
		t.Fatal("token was accepted by the wrong relay node")
	}
	parts := strings.Split(hostToken, ".")
	parts[1] = parts[1][:len(parts[1])-1] + "A"
	if _, err := manager.Verify(strings.Join(parts, "."), "relay_a"); err == nil {
		t.Fatal("tampered token was accepted")
	}
	manager.now = func() time.Time { return now.Add(3 * time.Minute) }
	if _, err := manager.Verify(hostToken, "relay_a"); err == nil {
		t.Fatal("expired token was accepted")
	}
}

func TestRelaySigningKeyRotationKeepsPreviousTokensValid(t *testing.T) {
	_, first, _ := ed25519.GenerateKey(rand.Reader)
	_, second, _ := ed25519.GenerateKey(rand.Reader)
	cfg := config.Defaults.RelayRegistry
	cfg.RelayTokenKeyID = "first"
	cfg.RelayTokenPrivateKeyBase64 = base64.RawStdEncoding.EncodeToString(first)
	cfg.RelayTokenRotationKeys = "second=" + base64.RawStdEncoding.EncodeToString(second)
	manager, err := NewRelayTokenManager(cfg, "production")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return now }
	claims := RelayClaims{RelayNodeID: "relay_a", AllocationID: "alloc_a", ConnectionID: "conn_a", RoomID: "room_a", EndpointRole: "HOST", Protocol: "UDP", MaxBPS: 1, MaxPPS: 1, MaxTotalBytes: 1, AllocationExpiresAt: now.Add(time.Hour).Unix()}
	oldToken, _, _ := manager.Sign(claims, time.Minute)
	before := manager.Keyset()
	if len(before.Keys) != 2 || before.SignedBy != "first" || before.Signature == "" {
		t.Fatalf("staged keyset = %#v", before)
	}
	if err := manager.Activate("second"); err != nil {
		t.Fatal(err)
	}
	newToken, _, _ := manager.Sign(claims, time.Minute)
	newClaims, err := manager.Verify(newToken, "relay_a")
	if err != nil || newClaims.KeyID != "second" {
		t.Fatalf("new token claims = %#v, %v", newClaims, err)
	}
	if _, err := manager.Verify(oldToken, "relay_a"); err != nil {
		t.Fatalf("previous token was rejected during verify-only window: %v", err)
	}
	if manager.Keyset().Version != before.Version+1 || manager.Keyset().SignedBy != "second" {
		t.Fatal("keyset version did not advance on activation")
	}
}
