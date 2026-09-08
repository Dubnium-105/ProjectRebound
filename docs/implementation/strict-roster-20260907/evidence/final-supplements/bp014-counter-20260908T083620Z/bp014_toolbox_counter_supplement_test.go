package relayruntime

import (
    "context"
    "fmt"
    "net"
    "os"
    "os/exec"
    "testing"
    "time"
)

// Temporary BP-014 supplement: a real Go Edge runtime owns the relay; the
// Rust test exercises Toolbox's private LegacyRoute dataplane and reads its
// actual LegacyRouteStatus counters. Removed after the isolated run.
func TestBP014ToolboxLegacyRouteCounters(t *testing.T) {
    toolbox := os.Getenv("TOOLBOX_ROOT")
    target := os.Getenv("TOOLBOX_TARGET")
    cargo := os.Getenv("TOOLBOX_CARGO")
    if toolbox == "" || target == "" || cargo == "" {
        t.Fatal("TOOLBOX_ROOT, TOOLBOX_TARGET, and TOOLBOX_CARGO are required")
    }
    now := time.Now().UTC().Truncate(time.Second)
    signer := newTestSigner(t, "bp014-toolbox-counter-key")
    runtime := testRuntime(t, signer, now, func(cfg *Config) {})
    listener, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
    if err != nil {
        t.Fatal(err)
    }
    ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
    defer cancel()
    serverDone := make(chan error, 1)
    go func() { serverDone <- runtime.ServeUDP(ctx, listener) }()
    hostToken := signer.sign(t, testClaims(now, "bp014-host", "HOST", "relay_test", "bp014_alloc"))
    memberToken := signer.sign(t, testClaims(now, "bp014-member", "PEER", "relay_test", "bp014_alloc"))
    port := listener.LocalAddr().(*net.UDPAddr).Port
    cmd := exec.CommandContext(ctx, cargo, "test", "--locked", "--features", "lab-testing", "--lib", "bp014_toolbox_legacy_route_counters", "--target-dir", target, "--", "--nocapture")
    cmd.Dir = toolbox
    cmd.Env = append(os.Environ(),
        "BP014_RELAY_HOST=127.0.0.1",
        "BP014_RELAY_PORT="+fmt.Sprint(port),
        "BP014_HOST_TOKEN="+hostToken,
        "BP014_MEMBER_TOKEN="+memberToken,
    )
    output, err := cmd.CombinedOutput()
    t.Logf("rust_toolbox_observation=%s", output)
    cancel()
    select {
    case serverErr := <-serverDone:
        if serverErr != nil {
            t.Logf("edge_shutdown=%v", serverErr)
        }
    case <-time.After(3 * time.Second):
        t.Log("edge_shutdown=timeout")
    }
    if err != nil {
        t.Fatalf("Toolbox private LegacyRoute counter test: %v", err)
    }
}


