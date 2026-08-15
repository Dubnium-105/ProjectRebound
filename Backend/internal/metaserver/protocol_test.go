package metaserver

import (
	"bytes"
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
)

func TestDecodeRequestWrapper(t *testing.T) {
	data := protowire.AppendTag(nil, 1, protowire.VarintType)
	data = protowire.AppendVarint(data, 42)
	data = protowire.AppendTag(data, 2, protowire.BytesType)
	data = protowire.AppendString(data, "/party.party/Create")
	data = protowire.AppendTag(data, 3, protowire.BytesType)
	data = protowire.AppendBytes(data, []byte{1, 2, 3})
	got, err := DecodeRequestWrapper(data)
	if err != nil {
		t.Fatal(err)
	}
	if got.MessageID != 42 || got.RPCPath != "/party.party/Create" || len(got.Message) != 3 {
		t.Fatalf("unexpected wrapper: %+v", got)
	}
}

func TestEncodeStatusMessagePreservesExplicitZero(t *testing.T) {
	if got := EncodeStatusMessage(0); !bytes.Equal(got, []byte{0x08, 0x00}) {
		t.Fatalf("success status wire = %x, want 0800", got)
	}
	if got := EncodeStatusMessage(404); !bytes.Equal(got, []byte{0x08, 0x94, 0x03}) {
		t.Fatalf("nonzero status wire = %x, want 089403", got)
	}
}

func TestSerialFrameWriterBoundsPendingBytes(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writer := newSerialFrameWriter(server, 64, 12, time.Second)
	go writer.run(ctx)
	if err := writer.enqueue([]byte("1234")); err != nil {
		t.Fatal(err)
	}
	if err := writer.enqueue([]byte("5678")); !errors.Is(err, ErrWriteQueueFull) {
		t.Fatalf("second queued frame error = %v, want %v", err, ErrWriteQueueFull)
	}
}

func TestKeepaliveAndUnknownRPCCompatibility(t *testing.T) {
	if !isKeepaliveFrame([]byte("//")) || isKeepaliveFrame([]byte("/")) {
		t.Fatal("native keepalive classification is incompatible")
	}
	metrics := NewMetaMetrics()
	server := &TCPServer{metrics: metrics}
	response := server.dispatch(
		context.Background(),
		GateSession{PlayerID: "player"},
		RequestWrapper{MessageID: 42, RPCPath: "/unknown.Service/Method"},
	)
	if response.MessageID != 42 || response.RPCPath != "/unknown.Service/Method" ||
		response.ErrorCode != rpcUnknownError {
		t.Fatalf("unexpected unknown RPC response: %#v", response)
	}
}
