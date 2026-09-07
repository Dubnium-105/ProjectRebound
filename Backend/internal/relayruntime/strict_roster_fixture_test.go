package relayruntime

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestStrictRosterRustRelayClients(t *testing.T) {
	driver := os.Getenv("TOOLBOX_RELAY_DRIVER")
	if driver == "" {
		t.Skip("BLOCKED: set TOOLBOX_RELAY_DRIVER to the built Rust relay_interop_driver executable")
	}
	now := time.Now().UTC().Truncate(time.Second)
	signer := newTestSigner(t, "synthetic-interop-key")
	runtime := testRuntime(t, signer, now, func(cfg *Config) {})
	listener, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	serverDone := make(chan error, 1)
	go func() { serverDone <- runtime.ServeUDP(ctx, listener) }()
	fixture, err := json.Marshal(map[string]any{
		"port":       listener.LocalAddr().(*net.UDPAddr).Port,
		"host_token": signer.sign(t, testClaims(now, "interop-host", "HOST", "relay_test", "alloc_interop")),
		"peer_token": signer.sign(t, testClaims(now, "interop-peer", "PEER", "relay_test", "alloc_interop")),
	})
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, driver)
	command.Stdin = bytes.NewReader(fixture)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("Rust relay interop: %v; %s", err, output)
	}
	t.Log(string(output))
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

type relayWireVector struct {
	Name                    string `json:"name"`
	ReceiverRole            byte   `json:"receiver_role"`
	SyntheticRecipientToken string `json:"synthetic_recipient_token"`
	Handle                  uint64 `json:"handle"`
	Sequence                uint64 `json:"sequence"`
	PacketHex               string `json:"packet_hex"`
	PayloadHex              string `json:"payload_hex"`
}

// Uses the exact encoder and per-recipient re-tagging performed by the Edge
// data path. The committed JSON is also decoded by the real Rust RelayClient.
func TestStrictRosterRelayCrossLanguageFixture(t *testing.T) {
	specs := []struct {
		name, senderToken, recipientToken string
		role                              EndpointRole
		sequence                          uint64
		payload                           []byte
	}{
		{"host_to_peer", "synthetic-host-token", "synthetic-peer-token", RoleHost, 17, []byte("host-to-member")},
		{"peer_to_host", "synthetic-peer-token", "synthetic-host-token", RolePeer, 19, []byte("member-to-host")},
	}
	vectors := make([]relayWireVector, 0, len(specs))
	for _, spec := range specs {
		senderKey := deriveDataKey(spec.senderToken)
		recipientKey := deriveDataKey(spec.recipientToken)
		incoming := encodeDataPacket(7, spec.role, spec.sequence, senderKey, spec.payload)
		decoded, err := decodeDataPacket(incoming)
		if err != nil || !verifyDataTag(senderKey, incoming, decoded.Tag) {
			t.Fatalf("sender encoding failed: %v", err)
		}
		forwarded := encodeDataPacketVersion(ProtocolVersion, decoded.Handle, decoded.Role, decoded.Sequence, recipientKey, decoded.Payload)
		forwardedDecoded, err := decodeDataPacket(forwarded)
		if err != nil || !verifyDataTag(recipientKey, forwarded, forwardedDecoded.Tag) {
			t.Fatalf("recipient re-tag failed: %v", err)
		}
		if verifyDataTag(senderKey, forwarded, forwardedDecoded.Tag) {
			t.Fatal("recipient must have a distinct MAC key")
		}
		receiverRole := byte(RolePeer)
		if spec.role == RolePeer {
			receiverRole = byte(RoleHost)
		}
		vectors = append(vectors, relayWireVector{spec.name, receiverRole, spec.recipientToken, 7, spec.sequence, hex.EncodeToString(forwarded), hex.EncodeToString(spec.payload)})
	}
	path := filepath.Join("..", "..", "api", "fixtures", "relay-v2.json")
	if os.Getenv("UPDATE_RELAY_FIXTURE") == "1" {
		data, err := json.MarshalIndent(vectors, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(data, '\n'), 0644); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved []relayWireVector
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(saved, vectors) {
		t.Fatal("committed cross-language relay fixture differs from actual Edge encoding")
	}
}
