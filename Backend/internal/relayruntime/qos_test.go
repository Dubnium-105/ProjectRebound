package relayruntime

import (
	"bytes"
	"net/netip"
	"testing"
	"time"
)

func TestQoSResponseCannotAmplifyAndDoesNotAffectRelayProtocol(t *testing.T) {
	now := time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC)
	signer := newTestSigner(t, "relay-key-qos")
	runtime := testRuntime(t, signer, now, func(cfg *Config) {
		cfg.QoSEnabled = true
		cfg.QoSPacketsPerSecond = 2
		cfg.QoSMaxRequestBytes = 64
	})
	address := netip.MustParseAddrPort("198.51.100.20:50000")
	request := append([]byte{0x59, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}, []byte("echo")...)
	output := runtime.Process(request, address)
	if len(output) != 1 || len(output[0].Packet) > len(request) {
		t.Fatalf("QoS response amplified or was not returned: %#v", output)
	}
	if !bytes.Equal(output[0].Packet, []byte{0x95, 0x00, 'e', 'c', 'h', 'o'}) {
		t.Fatalf("unexpected QoS response: %x", output[0].Packet)
	}
	if got := runtime.Process([]byte{0x59, 0}, address); len(got) != 0 {
		t.Fatalf("malformed QoS request returned data: %#v", got)
	}
}

func TestQoSPerIPRateLimit(t *testing.T) {
	now := time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC)
	signer := newTestSigner(t, "relay-key-qos-limit")
	runtime := testRuntime(t, signer, now, func(cfg *Config) {
		cfg.QoSPacketsPerSecond = 1
		cfg.QoSMaxRequestBytes = 64
	})
	address := netip.MustParseAddrPort("198.51.100.21:50000")
	request := []byte{0x59, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	if len(runtime.Process(request, address)) != 1 {
		t.Fatal("first QoS request was rejected")
	}
	if len(runtime.Process(request, address)) != 0 {
		t.Fatal("rate-limited QoS request was answered")
	}
	if runtime.Metrics().Snapshot().QoSRateLimited != 1 {
		t.Fatal("QoS rate-limit metric was not incremented")
	}
}
