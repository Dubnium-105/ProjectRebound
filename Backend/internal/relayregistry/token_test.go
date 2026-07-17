package relayregistry

import (
	"strings"
	"testing"
	"time"

	"github.com/projectrebound/matchserver/internal/config"
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
