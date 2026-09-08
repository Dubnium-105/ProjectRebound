package relayruntime

import (
    "bytes"
    "context"
    "encoding/json"
    "net"
    "os"
    "os/exec"
    "testing"
    "time"
)

// This temporary supplement observes the Edge product Metrics state after the
// real Windows Rust RelayClient sends one packet in each direction.  It does
// not count driver-side send/receive calls and is removed after the run.
func TestBP014ProductCountersFromLiveRelay(t *testing.T) {
    driver := os.Getenv("TOOLBOX_RELAY_DRIVER")
    if driver == "" {
        t.Fatal("TOOLBOX_RELAY_DRIVER is required")
    }
    now := time.Now().UTC().Truncate(time.Second)
    signer := newTestSigner(t, "bp014-product-counter-key")
    runtime := testRuntime(t, signer, now, func(cfg *Config) {})
    listener, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
    if err != nil {
        t.Fatal(err)
    }
    ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
    defer cancel()
    serverDone := make(chan error, 1)
    go func() { serverDone <- runtime.ServeUDP(ctx, listener) }()
    before := runtime.metrics.Snapshot()
    t.Logf("relay_listener=127.0.0.1:%d", listener.LocalAddr().(*net.UDPAddr).Port)
    fixture, err := json.Marshal(map[string]any{
        "port": listener.LocalAddr().(*net.UDPAddr).Port,
        "host_token": signer.sign(t, testClaims(now, "bp014-host", "HOST", "relay_test", "bp014_alloc")),
        "peer_token": signer.sign(t, testClaims(now, "bp014-peer", "PEER", "relay_test", "bp014_alloc")),
    })
    if err != nil {
        t.Fatal(err)
    }
    command := exec.CommandContext(ctx, driver)
    command.Stdin = bytes.NewReader(fixture)
    output, err := command.CombinedOutput()
    if err != nil {
        observed := runtime.metrics.Snapshot()
        t.Logf("failure_product_stats packets_received=%d packets_forwarded=%d bind_init=%d bind_challenge=%d bind_success=%d bind_failed=%d cookie_invalid=%d token_invalid=%d", observed.PacketsReceived-before.PacketsReceived, observed.PacketsForwarded-before.PacketsForwarded, observed.BindInit-before.BindInit, observed.BindChallenge-before.BindChallenge, observed.BindSuccess-before.BindSuccess, observed.BindFailed-before.BindFailed, observed.CookieInvalid-before.CookieInvalid, observed.TokenInvalid-before.TokenInvalid)
        t.Fatalf("Rust relay interop: %v; %s", err, output)
    }
    t.Logf("driver_observation=%s", output)
    after := runtime.metrics.Snapshot()
    received := after.PacketsReceived - before.PacketsReceived
    forwarded := after.PacketsForwarded - before.PacketsForwarded
    t.Logf("BP014_PRODUCT_STATS source=relayruntime.Metrics.Snapshot packets_received_delta=%d packets_forwarded_delta=%d bytes_forwarded_delta=%d bind_success_delta=%d", received, forwarded, after.BytesForwarded-before.BytesForwarded, after.BindSuccess-before.BindSuccess)
    if received < 2 {
        t.Fatalf("live product packets_received counter did not advance: %d", received)
    }
    if forwarded < 2 {
        t.Fatalf("live product packets_forwarded counter did not record both directions: %d", forwarded)
    }
    cancel()
    select {
    case err := <-serverDone:
        if err != nil {
            t.Fatal(err)
        }
    case <-time.After(3 * time.Second):
        t.Fatal("Edge did not shut down")
    }
}
