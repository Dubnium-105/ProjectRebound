package relayruntime

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"net/netip"
	"strings"
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
	if len(runtime.allocations) != 0 || binary.BigEndian.Uint32(challenge[0].Packet[22:26]) != 10_000 {
		t.Fatalf("challenge allocated state or returned the wrong lifetime: allocations=%d", len(runtime.allocations))
	}
	badChallenge := append([]byte(nil), challenge[0].Packet...)
	badChallenge[6+NonceSize+4] ^= 0xff
	if result := runtime.Process(encodeProofForTest(hostToken, badChallenge), hostAddress); len(result) != 0 {
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

	forgedTag := encodeDataPacket(handle, RoleHost, 2, deriveDataKey(hostToken), []byte("host payload"))
	forgedTag[24] ^= 0xff
	if output := runtime.Process(forgedTag, hostAddress); len(output) != 0 {
		t.Fatal("packet with an invalid authentication tag was forwarded")
	}
	reservedFlags := encodeDataPacket(handle, RoleHost, 2, deriveDataKey(hostToken), []byte("host payload"))
	reservedFlags[15] = 1
	if output := runtime.Process(reservedFlags, hostAddress); len(output) != 0 {
		t.Fatal("packet with unsupported flags was forwarded")
	}
	oversized := encodeDataPacket(handle, RoleHost, 2, deriveDataKey(hostToken), make([]byte, 1201))
	if output := runtime.Process(oversized, hostAddress); len(output) != 0 {
		t.Fatal("packet above the negotiated MTU was forwarded")
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
	if runtime.metrics.authenticationFailed.Load() != 1 || runtime.metrics.packetTooLarge.Load() != 1 || runtime.metrics.replayDropped.Load() != 1 {
		t.Fatalf("authentication_failed=%d too_large=%d replay_dropped=%d",
			runtime.metrics.authenticationFailed.Load(), runtime.metrics.packetTooLarge.Load(), runtime.metrics.replayDropped.Load())
	}
	if result := bindWithoutAssertions(runtime, hostToken, hostAddress); len(result) != 1 {
		t.Fatal("same-address bind retry was not idempotent")
	}
	if result := bindWithoutAssertions(runtime, hostToken, otherAddress); len(result) != 0 {
		t.Fatal("relay token replay from a different endpoint was accepted")
	}
	if runtime.metrics.tokenReplay.Load() != 1 {
		t.Fatalf("token replay metric = %d", runtime.metrics.tokenReplay.Load())
	}
}

func TestRuntimeAllowsShortNATPortRebindButRejectsLateOrCrossIPReplay(t *testing.T) {
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	current := now
	signer := newTestSigner(t, "relay-key-a")
	runtime := testRuntime(t, signer, now, func(cfg *Config) { cfg.NATRebindWindowSecs = 30 })
	runtime.now = func() time.Time { return current }
	token := signer.sign(t, testClaims(now, "nat-host", "HOST", "relay_test", "alloc_nat"))
	first := netip.MustParseAddrPort("198.51.100.10:50000")
	second := netip.MustParseAddrPort("198.51.100.10:50001")
	late := netip.MustParseAddrPort("198.51.100.10:50002")
	crossIP := netip.MustParseAddrPort("198.51.100.11:50001")
	bindForTest(t, runtime, token, first)
	current = current.Add(5 * time.Second)
	if result := bindWithoutAssertions(runtime, token, second); len(result) != 1 {
		t.Fatal("short NAT port rebind was rejected")
	}
	if runtime.allocations["alloc_nat"].host.address != second {
		t.Fatal("NAT rebind did not replace the endpoint")
	}
	if result := bindWithoutAssertions(runtime, token, crossIP); len(result) != 0 {
		t.Fatal("cross-IP token replay was accepted")
	}
	current = now.Add(31 * time.Second)
	if result := bindWithoutAssertions(runtime, token, late); len(result) != 0 {
		t.Fatal("late NAT rebind was accepted")
	}
	if runtime.allocations["alloc_nat"].host.address != second {
		t.Fatal("rejected rebind changed the endpoint")
	}
	if runtime.metrics.natRebind.Load() != 1 || runtime.metrics.tokenReplay.Load() != 2 {
		t.Fatalf("nat_rebind=%d token_replay=%d", runtime.metrics.natRebind.Load(), runtime.metrics.tokenReplay.Load())
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

func TestRuntimeV1CompatibilityIsExplicitlyOptIn(t *testing.T) {
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	signer := newTestSigner(t, "relay-key-a")
	address := netip.MustParseAddrPort("198.51.100.10:50000")
	token := signer.sign(t, testClaims(now, "v1-host", "HOST", "relay_test", "alloc_v1"))
	disabled := testRuntime(t, signer, now, func(cfg *Config) {})
	if output := disabled.Process(encodeBindV1ForTest(token), address); len(output) != 0 {
		t.Fatal("protocol v1 was accepted by default")
	}
	enabled := testRuntime(t, signer, now, func(cfg *Config) { cfg.AcceptProtocolV1 = true })
	challenge := enabled.Process(encodeBindV1ForTest(token), address)
	if len(challenge) != 1 || len(challenge[0].Packet) != 38 || challenge[0].Packet[4] != ProtocolVersionV1 {
		t.Fatalf("v1 challenge = %#v", challenge)
	}
	result := enabled.Process(encodeProofV1ForTest(token, challenge[0].Packet[6:]), address)
	if len(result) != 1 || len(result[0].Packet) != 15 || result[0].Packet[4] != ProtocolVersionV1 {
		t.Fatalf("v1 bind result = %#v", result)
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
	_, egressBPS, _ := runtime.Snapshot()
	if egressBPS != 8 {
		t.Fatalf("reported egress = %d bps", egressBPS)
	}
	current = current.Add(time.Second)
	third := encodeDataPacket(handle, RoleHost, 4, deriveDataKey(hostToken), []byte{3})
	if len(runtime.Process(third, hostAddress)) != 0 {
		t.Fatal("allocation total-byte limit was not applied")
	}
	if runtime.metrics.activeAllocations.Load() != 0 {
		t.Fatal("allocation was not closed immediately after exceeding its total-byte limit")
	}
	select {
	case event := <-runtime.Events():
		if event.Type != "AllocationClosed" {
			t.Fatalf("close event = %#v", event)
		}
	default:
		t.Fatal("allocation close was not reported")
	}
	current = current.Add(3 * time.Second)
	if closed := runtime.Sweep(); closed != 0 {
		t.Fatalf("already closed allocation was swept again: %d", closed)
	}
	restarted := testRuntime(t, signer, current, func(cfg *Config) {})
	if len(restarted.allocations) != 0 || len(restarted.Process(first, hostAddress)) != 0 {
		t.Fatal("allocation state survived a runtime restart")
	}
}

func TestRuntimeSeparatesUnverifiedLimitsAndBoundsIPState(t *testing.T) {
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	signer := newTestSigner(t, "relay-key-a")
	runtime := testRuntime(t, signer, now, func(cfg *Config) {
		cfg.BindInitPerSecond = 2
		cfg.MaxIPRateStates = 1
	})
	token := signer.sign(t, testClaims(now, "limited-init", "HOST", "relay_test", "alloc_init"))
	packet := encodeBindForTest(token)
	firstIP := netip.MustParseAddrPort("198.51.100.10:50000")
	secondIP := netip.MustParseAddrPort("198.51.100.11:50000")
	if len(runtime.Process(packet, firstIP)) != 1 || len(runtime.Process(packet, firstIP)) != 1 {
		t.Fatal("bind init allowance was too small")
	}
	if len(runtime.Process(packet, firstIP)) != 0 {
		t.Fatal("bind init rate limit was not enforced")
	}
	if len(runtime.Process(packet, secondIP)) != 0 || len(runtime.ipStates) != 1 {
		t.Fatalf("IP state cardinality was not bounded: %d", len(runtime.ipStates))
	}
}

func TestRuntimeTemporarilyBansInvalidTokenFlood(t *testing.T) {
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	signer := newTestSigner(t, "relay-key-a")
	runtime := testRuntime(t, signer, now, func(cfg *Config) {
		cfg.InvalidTokensPerMin = 1
		cfg.BindInitPerSecond = 10
		cfg.BindProofPerSecond = 10
	})
	address := netip.MustParseAddrPort("198.51.100.10:50000")
	invalidToken := strings.Repeat("x", 100)
	if len(bindWithoutAssertions(runtime, invalidToken, address)) != 0 ||
		len(bindWithoutAssertions(runtime, invalidToken, address)) != 0 {
		t.Fatal("invalid token unexpectedly bound")
	}
	state := runtime.ipStates[address.Addr()]
	if state == nil || !state.bannedUntil.After(now) {
		t.Fatal("invalid token flood did not temporarily ban the source")
	}
	if len(runtime.Process(encodeBindForTest(invalidToken), address)) != 0 {
		t.Fatal("temporarily banned source received another challenge")
	}
}

func TestRuntimeLimitsUniqueAllocationsPerIP(t *testing.T) {
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	signer := newTestSigner(t, "relay-key-a")
	runtime := testRuntime(t, signer, now, func(cfg *Config) { cfg.MaxAllocationsPerIP = 1 })
	hostAddress := netip.MustParseAddrPort("198.51.100.10:50000")
	peerAddress := netip.MustParseAddrPort("198.51.100.10:50001")
	host := signer.sign(t, testClaims(now, "same-ip-host", "HOST", "relay_test", "alloc_same_ip"))
	peer := signer.sign(t, testClaims(now, "same-ip-peer", "PEER", "relay_test", "alloc_same_ip"))
	bindForTest(t, runtime, host, hostAddress)
	bindForTest(t, runtime, peer, peerAddress)
	other := signer.sign(t, testClaims(now, "same-ip-other", "HOST", "relay_test", "alloc_other"))
	if result := bindWithoutAssertions(runtime, other, netip.MustParseAddrPort("198.51.100.10:50002")); len(result) != 0 {
		t.Fatal("per-IP allocation limit was not enforced")
	}
	if len(runtime.allocations) != 1 || len(runtime.ipAllocations[hostAddress.Addr()]) != 1 {
		t.Fatalf("allocations=%d IP allocations=%d", len(runtime.allocations), len(runtime.ipAllocations[hostAddress.Addr()]))
	}
}

func TestRuntimeDrainsExistingAllocationAtDeadline(t *testing.T) {
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
	runtime.SetDrain(now.Add(time.Second))
	if closed := runtime.Sweep(); closed != 0 {
		t.Fatalf("allocations closed before drain deadline = %d", closed)
	}
	runtime.now = func() time.Time { return now.Add(2 * time.Second) }
	if closed := runtime.Sweep(); closed != 1 {
		t.Fatalf("allocations closed at drain deadline = %d", closed)
	}
	first, second := <-runtime.Events(), <-runtime.Events()
	if first.Type != "AllocationClosed" || second.Type != "DrainCompleted" {
		t.Fatalf("drain completion events = %#v, %#v", first, second)
	}
}

func TestRuntimeOverloadStateRejectsOnlyNewAllocations(t *testing.T) {
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	signer := newTestSigner(t, "relay-key-a")
	runtime := testRuntime(t, signer, now, func(cfg *Config) {
		cfg.MaxAllocations = 2
		cfg.DegradedThresholdPct = 50
		cfg.RejectNewThresholdPct = 75
		cfg.MaxMemoryMB = 1_000_000
		cfg.MaxIngressMbps = 1_000_000
		cfg.MaxIngressPPS = 1_000_000
	})
	firstAddress := netip.MustParseAddrPort("198.51.100.10:50000")
	firstPeerAddress := netip.MustParseAddrPort("198.51.100.11:50001")
	firstToken := signer.sign(t, testClaims(now, "overload-a-host", "HOST", "relay_test", "alloc_overload_a"))
	firstPeerToken := signer.sign(t, testClaims(now, "overload-a-peer", "PEER", "relay_test", "alloc_overload_a"))
	firstHandle := bindForTest(t, runtime, firstToken, firstAddress)
	runtime.Snapshot()
	if runtime.LoadState() != LoadStateDegraded {
		t.Fatalf("load state after 50%% allocations = %s", runtime.LoadState())
	}
	secondToken := signer.sign(t, testClaims(now, "overload-b-host", "HOST", "relay_test", "alloc_overload_b"))
	bindForTest(t, runtime, secondToken, netip.MustParseAddrPort("198.51.100.12:50002"))
	runtime.Snapshot()
	if runtime.LoadState() != LoadStateRejectNew {
		t.Fatalf("load state at capacity = %s", runtime.LoadState())
	}
	thirdToken := signer.sign(t, testClaims(now, "overload-c-host", "HOST", "relay_test", "alloc_overload_c"))
	if result := bindWithoutAssertions(runtime, thirdToken, netip.MustParseAddrPort("198.51.100.13:50003")); len(result) != 0 {
		t.Fatal("REJECT_NEW state accepted a new allocation")
	}
	bindForTest(t, runtime, firstPeerToken, firstPeerAddress)
	<-runtime.Events()
	packet := encodeDataPacket(firstHandle, RoleHost, 1, deriveDataKey(firstToken), []byte("existing"))
	if output := runtime.Process(packet, firstAddress); len(output) != 1 {
		t.Fatal("REJECT_NEW state interrupted an existing allocation")
	}
	runtime.SetDraining(true)
	if runtime.LoadState() != LoadStateDraining {
		t.Fatalf("drain load state = %s", runtime.LoadState())
	}
	if runtime.metrics.loadStateTransitions.Load() < 3 {
		t.Fatalf("load-state transitions = %d", runtime.metrics.loadStateTransitions.Load())
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
	drainDeadline := now.Add(10 * time.Minute)
	if err := control.handleControlMessage(controlEnvelope("EnterDrain", map[string]any{
		"deadline": drainDeadline.Format(time.RFC3339Nano), "migrate_existing": true,
	})); err != nil {
		t.Fatal(err)
	}
	if !runtime.draining || !runtime.drainDeadline.Equal(drainDeadline) {
		t.Fatalf("control drain state = %v / %v", runtime.draining, runtime.drainDeadline)
	}
	if err := control.handleControlMessage(controlEnvelope("ExitDrain", map[string]any{})); err != nil {
		t.Fatal(err)
	}
	if runtime.draining || !runtime.drainDeadline.IsZero() {
		t.Fatal("control exit-drain did not clear the deadline")
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
	runtime.nodePacketBucket = newTokenBucket(float64(cfg.MaxIngressPPS), float64(cfg.MaxIngressPPS), now)
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
	packet := make([]byte, 26+len(token))
	copy(packet, Magic)
	packet[4], packet[5] = ProtocolVersion, messageBind
	clientNonce := sha256.Sum256([]byte(token))
	copy(packet[6:22], clientNonce[:NonceSize])
	binary.BigEndian.PutUint16(packet[22:24], 1200)
	binary.BigEndian.PutUint16(packet[24:26], uint16(len(token)))
	copy(packet[26:], token)
	return packet
}

func encodeBindV1ForTest(token string) []byte {
	packet := make([]byte, 8+len(token))
	copy(packet, Magic)
	packet[4], packet[5] = ProtocolVersionV1, messageBind
	binary.BigEndian.PutUint16(packet[6:8], uint16(len(token)))
	copy(packet[8:], token)
	return packet
}

func encodeProofV1ForTest(token string, cookie []byte) []byte {
	packet := make([]byte, 40+len(token))
	copy(packet, Magic)
	packet[4], packet[5] = ProtocolVersionV1, messageBindProof
	copy(packet[6:38], cookie)
	binary.BigEndian.PutUint16(packet[38:40], uint16(len(token)))
	copy(packet[40:], token)
	return packet
}

func encodeProofForTest(token string, challenge []byte) []byte {
	packet := make([]byte, 74+len(token))
	copy(packet, Magic)
	packet[4], packet[5] = ProtocolVersion, messageBindProof
	clientNonce := sha256.Sum256([]byte(token))
	copy(packet[6:22], clientNonce[:NonceSize])
	copy(packet[22:38], challenge[6:22])
	binary.BigEndian.PutUint16(packet[38:40], 1200)
	copy(packet[40:72], challenge[26:58])
	binary.BigEndian.PutUint16(packet[72:74], uint16(len(token)))
	copy(packet[74:], token)
	return packet
}

func bindForTest(t *testing.T, runtime *Runtime, token string, address netip.AddrPort) uint64 {
	t.Helper()
	output := bindWithoutAssertions(runtime, token, address)
	if len(output) != 1 || len(output[0].Packet) != 17 || output[0].Packet[5] != messageBindOK {
		t.Fatalf("bind result = %#v", output)
	}
	return binary.BigEndian.Uint64(output[0].Packet[6:14])
}

func bindWithoutAssertions(runtime *Runtime, token string, address netip.AddrPort) []OutboundDatagram {
	challenge := runtime.Process(encodeBindForTest(token), address)
	if len(challenge) != 1 || len(challenge[0].Packet) != 58 {
		return nil
	}
	return runtime.Process(encodeProofForTest(token, challenge[0].Packet), address)
}
