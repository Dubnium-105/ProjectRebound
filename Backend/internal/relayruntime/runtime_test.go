package relayruntime

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/netip"
	"testing"
	"time"
)

type testSigner struct {
	keyID      string
	privateKey ed25519.PrivateKey
	publicKey  ed25519.PublicKey
}

func newTestSigner(t *testing.T, keyID string) testSigner {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return testSigner{keyID: keyID, privateKey: privateKey, publicKey: publicKey}
}

func (s testSigner) key() PublicKey {
	return PublicKey{KeyID: s.keyID, Algorithm: "EdDSA", PublicKey: base64.RawURLEncoding.EncodeToString(s.publicKey)}
}

func (s testSigner) sign(t *testing.T, claims RelayClaims) string {
	t.Helper()
	claims.Issuer, claims.Audience, claims.KeyID = "game-control-plane", "game-relay", s.keyID
	header, err := json.Marshal(map[string]string{"alg": "EdDSA", "typ": "relay+jwt", "kid": s.keyID})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(s.privateKey, []byte(unsigned)))
}

func TestRuntimeCookieBindingAndAuthorizedForwarding(t *testing.T) {
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	signer := newTestSigner(t, "relay-key-a")
	runtime := testRuntime(t, signer, now, func(cfg *Config) {})
	hostAddress := netip.MustParseAddrPort("198.51.100.10:50000")
	peerAddress := netip.MustParseAddrPort("198.51.100.11:50001")
	otherAddress := netip.MustParseAddrPort("198.51.100.12:50002")
	hostClaims := testClaims(now, "token-host", "HOST", "relay_test", "alloc_a")
	peerClaims := testClaims(now, "token-peer", "PEER", "relay_test", "alloc_a")
	hostToken := signer.sign(t, hostClaims)
	peerToken := signer.sign(t, peerClaims)

	bind := encodeBindForTest(hostToken)
	challenge := runtime.Process(bind, hostAddress)
	if len(challenge) != 1 || len(challenge[0].Packet) > len(bind) || challenge[0].Packet[5] != messageChallenge {
		t.Fatalf("challenge = %#v", challenge)
	}
	badCookie := append([]byte(nil), challenge[0].Packet[6:]...)
	badCookie[0] ^= 0xff
	if result := runtime.Process(encodeProofForTest(hostToken, badCookie), hostAddress); len(result) != 0 {
		t.Fatalf("invalid cookie was accepted: %#v", result)
	}
	handle := bindForTest(t, runtime, hostToken, hostAddress)
	hostData := encodeDataPacket(handle, RoleHost, 1, deriveDataKey(hostToken), []byte("host payload"))
	if output := runtime.Process(hostData, hostAddress); len(output) != 0 {
		t.Fatal("single bound endpoint forwarded data")
	}
	peerHandle := bindForTest(t, runtime, peerToken, peerAddress)
	if peerHandle != handle {
		t.Fatalf("allocation handles differ: %d != %d", peerHandle, handle)
	}
	select {
	case event := <-runtime.Events():
		if event.Type != "AllocationOpened" || event.AllocationID != "alloc_a" {
			t.Fatalf("opened event = %#v", event)
		}
	default:
		t.Fatal("two-sided bind did not open the allocation")
	}

	hostData = encodeDataPacket(handle, RoleHost, 2, deriveDataKey(hostToken), []byte("host payload"))
	output := runtime.Process(hostData, hostAddress)
	if len(output) != 1 || output[0].Address != peerAddress {
		t.Fatalf("host forwarding = %#v", output)
	}
	forwarded, err := decodeDataPacket(output[0].Packet)
	if err != nil || string(forwarded.Payload) != "host payload" || !verifyDataTag(deriveDataKey(peerToken), output[0].Packet, forwarded.Tag) {
		t.Fatalf("forwarded packet = %#v, %v", forwarded, err)
	}
	peerData := encodeDataPacket(handle, RolePeer, 1, deriveDataKey(peerToken), []byte("peer payload"))
	output = runtime.Process(peerData, peerAddress)
	if len(output) != 1 || output[0].Address != hostAddress {
		t.Fatalf("peer forwarding = %#v", output)
	}
	if output := runtime.Process(encodeDataPacket(handle, RoleHost, 3, deriveDataKey(hostToken), []byte("forged source")), otherAddress); len(output) != 0 {
		t.Fatal("packet from an arbitrary source was forwarded")
	}
	replay := encodeDataPacket(handle, RoleHost, 4, deriveDataKey(hostToken), []byte("once"))
	if len(runtime.Process(replay, hostAddress)) != 1 || len(runtime.Process(replay, hostAddress)) != 0 {
		t.Fatal("data sequence replay was not rejected")
	}
	if result := runtime.Process(encodeProofForTest(hostToken, runtime.cookies.Issue(hostAddress, []byte(hostToken), now)), hostAddress); len(result) != 1 {
		t.Fatal("same-address bind retry was not idempotent")
	}
	if result := bindWithoutAssertions(runtime, hostToken, otherAddress); len(result) != 0 {
		t.Fatal("relay token replay from a different endpoint was accepted")
	}
}

func TestRuntimeRejectsWrongNodeExpiredAndInvalidRoleTokens(t *testing.T) {
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	signer := newTestSigner(t, "relay-key-a")
	runtime := testRuntime(t, signer, now, func(cfg *Config) {})
	address := netip.MustParseAddrPort("198.51.100.10:50000")
	tests := []RelayClaims{
		testClaims(now, "wrong-node", "HOST", "relay_other", "alloc_wrong_node"),
		testClaims(now.Add(-10*time.Minute), "expired", "HOST", "relay_test", "alloc_expired"),
		testClaims(now, "wrong-role", "OBSERVER", "relay_test", "alloc_wrong_role"),
	}
	for _, claims := range tests {
		if output := bindWithoutAssertions(runtime, signer.sign(t, claims), address); len(output) != 0 {
			t.Fatalf("invalid claims were accepted: %#v", claims)
		}
	}
	if runtime.metrics.tokenInvalid.Load() != uint64(len(tests)) {
		t.Fatalf("invalid token metric = %d", runtime.metrics.tokenInvalid.Load())
	}
}

func TestRuntimeRateLimitsTotalBytesAndExpiresInMemoryAllocations(t *testing.T) {
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	current := now
	signer := newTestSigner(t, "relay-key-a")
	runtime := testRuntime(t, signer, now, func(cfg *Config) {
		cfg.HeartbeatSeconds = 2
		cfg.AllocationIdleSeconds = 2
		cfg.MaxEgressBPS = 8
	})
	runtime.now = func() time.Time { return current }
	hostAddress := netip.MustParseAddrPort("198.51.100.10:50000")
	peerAddress := netip.MustParseAddrPort("198.51.100.11:50001")
	hostClaims := testClaims(now, "limited-host", "HOST", "relay_test", "alloc_limited")
	hostClaims.MaxBPS, hostClaims.MaxPPS, hostClaims.MaxTotalBytes = 8, 1, 2
	peerClaims := hostClaims
	peerClaims.TokenID, peerClaims.EndpointRole = "limited-peer", "PEER"
	hostToken, peerToken := signer.sign(t, hostClaims), signer.sign(t, peerClaims)
	handle := bindForTest(t, runtime, hostToken, hostAddress)
	bindForTest(t, runtime, peerToken, peerAddress)
	<-runtime.Events()
	first := encodeDataPacket(handle, RoleHost, 1, deriveDataKey(hostToken), []byte{1})
	if len(runtime.Process(first, hostAddress)) != 1 {
		t.Fatal("first limited packet was rejected")
	}
	second := encodeDataPacket(handle, RoleHost, 2, deriveDataKey(hostToken), []byte{2})
	if len(runtime.Process(second, hostAddress)) != 0 {
		t.Fatal("PPS limit was not applied")
	}
	current = current.Add(time.Second)
	refilled := encodeDataPacket(handle, RoleHost, 3, deriveDataKey(hostToken), []byte{2})
	if len(runtime.Process(refilled, hostAddress)) != 1 {
		t.Fatal("rate limit did not refill")
	}
	_, egressBPS := runtime.Snapshot()
	if egressBPS != 8 {
		t.Fatalf("reported egress = %d bps", egressBPS)
	}
	current = current.Add(time.Second)
	third := encodeDataPacket(handle, RoleHost, 4, deriveDataKey(hostToken), []byte{3})
	if len(runtime.Process(third, hostAddress)) != 0 {
		t.Fatal("allocation total-byte limit was not applied")
	}
	current = current.Add(3 * time.Second)
	if closed := runtime.Sweep(); closed != 1 || runtime.metrics.activeAllocations.Load() != 0 {
		t.Fatalf("expired allocation sweep = %d, active = %d", closed, runtime.metrics.activeAllocations.Load())
	}
	select {
	case event := <-runtime.Events():
		if event.Type != "AllocationClosed" {
			t.Fatalf("close event = %#v", event)
		}
	default:
		t.Fatal("allocation close was not reported")
	}
	restarted := testRuntime(t, signer, current, func(cfg *Config) {})
	if len(restarted.allocations) != 0 || len(restarted.Process(first, hostAddress)) != 0 {
		t.Fatal("allocation state survived a runtime restart")
	}
}

func TestRuntimeDrainsWithoutBreakingExistingAllocation(t *testing.T) {
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	signer := newTestSigner(t, "relay-key-a")
	runtime := testRuntime(t, signer, now, func(cfg *Config) {})
	hostAddress := netip.MustParseAddrPort("198.51.100.10:50000")
	peerAddress := netip.MustParseAddrPort("198.51.100.11:50001")
	hostToken := signer.sign(t, testClaims(now, "host-a", "HOST", "relay_test", "alloc_a"))
	peerToken := signer.sign(t, testClaims(now, "peer-a", "PEER", "relay_test", "alloc_a"))
	handle := bindForTest(t, runtime, hostToken, hostAddress)
	bindForTest(t, runtime, peerToken, peerAddress)
	<-runtime.Events()
	runtime.SetDraining(true)
	newToken := signer.sign(t, testClaims(now, "host-b", "HOST", "relay_test", "alloc_b"))
	if output := bindWithoutAssertions(runtime, newToken, netip.MustParseAddrPort("198.51.100.12:50002")); len(output) != 0 {
		t.Fatal("draining relay accepted a new allocation")
	}
	packet := encodeDataPacket(handle, RoleHost, 1, deriveDataKey(hostToken), []byte("existing"))
	if output := runtime.Process(packet, hostAddress); len(output) != 1 {
		t.Fatal("drain interrupted an existing allocation")
	}
	runtime.RevokeAllocation("alloc_a")
	first, second := <-runtime.Events(), <-runtime.Events()
	if first.Type != "AllocationClosed" || second.Type != "DrainCompleted" {
		t.Fatalf("drain completion events = %#v, %#v", first, second)
	}
}

func TestRuntimeKeysetUpdatesAndControlShutdown(t *testing.T) {
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	firstSigner := newTestSigner(t, "relay-key-a")
	secondSigner := newTestSigner(t, "relay-key-b")
	runtime := testRuntime(t, firstSigner, now, func(cfg *Config) {})
	claims := testClaims(now, "keyset-token", "HOST", "relay_test", "alloc_keyset")
	secondToken := secondSigner.sign(t, claims)
	address := netip.MustParseAddrPort("198.51.100.10:50000")
	if output := bindWithoutAssertions(runtime, secondToken, address); len(output) != 0 {
		t.Fatal("unknown signing key was accepted")
	}
	control := &ControlClient{runtime: runtime}
	if err := control.handleControlMessage(controlEnvelope("KeysetUpdate", map[string]any{
		"keys": []any{
			map[string]any{"kid": firstSigner.keyID, "alg": "EdDSA", "public_key": firstSigner.key().PublicKey},
			map[string]any{"kid": secondSigner.keyID, "alg": "EdDSA", "public_key": secondSigner.key().PublicKey},
		},
	})); err != nil {
		t.Fatal(err)
	}
	if output := bindWithoutAssertions(runtime, secondToken, address); len(output) != 1 {
		t.Fatal("updated keyset was not applied")
	}
	if err := control.handleControlMessage(controlEnvelope("Shutdown", map[string]any{})); err != errRelayShutdown {
		t.Fatalf("shutdown error = %v", err)
	}
	select {
	case <-runtime.ShutdownRequested():
	default:
		t.Fatal("control shutdown was not propagated")
	}
}

func testRuntime(t *testing.T, signer testSigner, now time.Time, mutate func(*Config)) *Runtime {
	t.Helper()
	cfg := DefaultConfig
	cfg.MaxEgressBPS = 1_000_000
	cfg.IPPacketsPerSecond = 1000
	mutate(&cfg)
	verifier, err := NewTokenVerifier(Keyset{Keys: []PublicKey{signer.key()}})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime("relay_test", cfg, verifier, NewMetrics())
	if err != nil {
		t.Fatal(err)
	}
	runtime.now = func() time.Time { return now }
	runtime.nodeByteBucket = newTokenBucket(float64(cfg.MaxEgressBPS)/8, float64(cfg.MaxEgressBPS)/8, now)
	return runtime
}

func testClaims(now time.Time, tokenID, role, nodeID, allocationID string) RelayClaims {
	return RelayClaims{
		TokenID: tokenID, RelayNodeID: nodeID, AllocationID: allocationID,
		ConnectionID: "conn_" + allocationID, RoomID: "room_a", EndpointRole: role, Protocol: "UDP",
		MaxBPS: 256000, MaxPPS: 200, MaxTotalBytes: 268435456,
		NotBefore: now.Add(-5 * time.Second).Unix(), ExpiresAt: now.Add(2 * time.Minute).Unix(),
		AllocationExpiresAt: now.Add(30 * time.Minute).Unix(),
	}
}

func encodeBindForTest(token string) []byte {
	packet := make([]byte, 8+len(token))
	copy(packet, Magic)
	packet[4], packet[5] = ProtocolVersion, messageBind
	binary.BigEndian.PutUint16(packet[6:8], uint16(len(token)))
	copy(packet[8:], token)
	return packet
}

func encodeProofForTest(token string, cookie []byte) []byte {
	packet := make([]byte, 40+len(token))
	copy(packet, Magic)
	packet[4], packet[5] = ProtocolVersion, messageBindProof
	copy(packet[6:38], cookie)
	binary.BigEndian.PutUint16(packet[38:40], uint16(len(token)))
	copy(packet[40:], token)
	return packet
}

func bindForTest(t *testing.T, runtime *Runtime, token string, address netip.AddrPort) uint64 {
	t.Helper()
	output := bindWithoutAssertions(runtime, token, address)
	if len(output) != 1 || len(output[0].Packet) != 15 || output[0].Packet[5] != messageBindOK {
		t.Fatalf("bind result = %#v", output)
	}
	return binary.BigEndian.Uint64(output[0].Packet[6:14])
}

func bindWithoutAssertions(runtime *Runtime, token string, address netip.AddrPort) []OutboundDatagram {
	challenge := runtime.Process(encodeBindForTest(token), address)
	if len(challenge) != 1 || len(challenge[0].Packet) != 38 {
		return nil
	}
	return runtime.Process(encodeProofForTest(token, challenge[0].Packet[6:]), address)
}
