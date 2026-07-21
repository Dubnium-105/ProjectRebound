package relayruntime

import (
	"net/netip"
	"testing"
	"time"
)

func TestV2CookieIsStatelessAndAcceptsCurrentOrPreviousBucket(t *testing.T) {
	manager, err := NewCookieManager(10 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0).UTC()
	address := netip.MustParseAddrPort("198.51.100.10:50000")
	clientNonce := []byte("client-nonce-123")
	serverNonce := []byte("server-nonce-123")
	token := []byte("signed-relay-token")
	cookie := manager.IssueV2(address, clientNonce, serverNonce, token, 1200, now)
	if !manager.VerifyV2(cookie, address, clientNonce, serverNonce, token, 1200, now.Add(10*time.Second)) {
		t.Fatal("cookie was not accepted from the previous time bucket")
	}
	if manager.VerifyV2(cookie, address, clientNonce, serverNonce, token, 1200, now.Add(20*time.Second)) {
		t.Fatal("expired cookie was accepted")
	}
	if manager.VerifyV2(cookie, address, []byte("different-nonce!"), serverNonce, token, 1200, now) ||
		manager.VerifyV2(cookie, netip.MustParseAddrPort("198.51.100.11:50000"), clientNonce, serverNonce, token, 1200, now) {
		t.Fatal("cookie was not bound to the nonce and source endpoint")
	}
}
