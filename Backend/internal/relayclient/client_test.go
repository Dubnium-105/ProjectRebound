package relayclient

import (
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/relayruntime"
)

func TestDataPacketUsesV2AuthenticatedHeader(t *testing.T) {
	key := deriveKey("relay-token")
	packet := encodeData(42, 1, 7, key, []byte("payload"))
	if len(packet) != dataHeaderSize+7 || string(packet[:4]) != magic || packet[4] != protocolVersion || packet[5] != messageData {
		t.Fatalf("invalid packet header: %x", packet)
	}
	if binary.BigEndian.Uint64(packet[6:14]) != 42 || packet[14] != 1 || packet[15] != 0 || binary.BigEndian.Uint64(packet[16:24]) != 7 {
		t.Fatalf("invalid packet fields: %x", packet[:24])
	}
	if !hmac.Equal(packet[24:40], authenticationTag(key, packet)) {
		t.Fatal("packet authentication tag does not verify")
	}
	packet[len(packet)-1] ^= 1
	if hmac.Equal(packet[24:40], authenticationTag(key, packet)) {
		t.Fatal("modified payload retained a valid tag")
	}
}

func TestClientBindsAndForwardsThroughRuntime(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := relayruntime.NewTokenVerifier(relayruntime.Keyset{Keys: []relayruntime.PublicKey{{
		KeyID: "test-key", Algorithm: "EdDSA", PublicKey: base64.RawURLEncoding.EncodeToString(publicKey),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	cfg := relayruntime.DefaultConfig
	cfg.MaxEgressBPS = 1_000_000
	cfg.MaxIngressPPS = 1000
	cfg.IPPacketsPerSecond = 1000
	runtime, err := relayruntime.NewRuntime("relay_test", cfg, verifier, relayruntime.NewMetrics())
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() { result <- runtime.ServeUDP(ctx, listener) }()

	now := time.Now().UTC()
	hostToken := signRelayToken(t, privateKey, relayruntime.RelayClaims{
		Issuer: "game-control-plane", Audience: "game-relay", KeyID: "test-key", TokenID: "host-jti",
		RelayNodeID: "relay_test", AllocationID: "alloc_test", ConnectionID: "conn_test", RoomID: "room_test",
		EndpointRole: "HOST", Protocol: "UDP", MaxBPS: 256000, MaxPPS: 100, MaxTotalBytes: 1 << 20,
		NotBefore: now.Add(-time.Second).Unix(), ExpiresAt: now.Add(time.Minute).Unix(), AllocationExpiresAt: now.Add(2 * time.Minute).Unix(),
	})
	peerClaims := relayruntime.RelayClaims{
		Issuer: "game-control-plane", Audience: "game-relay", KeyID: "test-key", TokenID: "peer-jti",
		RelayNodeID: "relay_test", AllocationID: "alloc_test", ConnectionID: "conn_test", RoomID: "room_test",
		EndpointRole: "PEER", Protocol: "UDP", MaxBPS: 256000, MaxPPS: 100, MaxTotalBytes: 1 << 20,
		NotBefore: now.Add(-time.Second).Unix(), ExpiresAt: now.Add(time.Minute).Unix(), AllocationExpiresAt: now.Add(2 * time.Minute).Unix(),
	}
	peerToken := signRelayToken(t, privateKey, peerClaims)
	host, err := Dial(ctx, listener.LocalAddr().String(), hostToken, 1200)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	peer, err := Dial(ctx, listener.LocalAddr().String(), peerToken, 1200)
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	if err := host.Send(ctx, []byte("authenticated payload")); err != nil {
		t.Fatal(err)
	}
	received, err := peer.Receive(ctx)
	if err != nil || string(received) != "authenticated payload" {
		t.Fatalf("received %q, error %v", received, err)
	}
	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Relay runtime did not stop")
	}
}

func signRelayToken(t *testing.T, privateKey ed25519.PrivateKey, claims relayruntime.RelayClaims) string {
	t.Helper()
	header, _ := json.Marshal(map[string]string{"alg": "EdDSA", "typ": "relay+jwt", "kid": claims.KeyID})
	payload, _ := json.Marshal(claims)
	encoded := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	return encoded + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, []byte(encoded)))
}

func TestDialRejectsUnsafeParametersBeforeNetworkAccess(t *testing.T) {
	for _, test := range []struct {
		token string
		mtu   uint16
	}{{"", 1200}, {"token", 999}, {"token", 1351}} {
		if _, err := Dial(t.Context(), "127.0.0.1:1", test.token, test.mtu); err == nil {
			t.Fatalf("Dial accepted token length %d and MTU %d", len(test.token), test.mtu)
		}
	}
}
