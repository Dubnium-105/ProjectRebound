package relayruntime

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestUDPListenerPerformsBindChallenge(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	signer := newTestSigner(t, "relay-key-a")
	runtime := testRuntime(t, signer, now, func(cfg *Config) {})
	listener, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- runtime.ServeUDP(ctx, listener) }()
	client, err := net.DialUDP("udp", nil, listener.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	token := signer.sign(t, testClaims(now, "udp-host", "HOST", "relay_test", "alloc_udp"))
	if _, err := client.Write(encodeBindForTest(token)); err != nil {
		t.Fatal(err)
	}
	if err := client.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, 1280)
	length, err := client.Read(response)
	if err != nil {
		t.Fatal(err)
	}
	if length != 58 || response[5] != messageChallenge {
		t.Fatalf("UDP challenge length/type = %d/%d", length, response[5])
	}
	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("UDP listener shutdown: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("UDP listener did not stop")
	}
}
