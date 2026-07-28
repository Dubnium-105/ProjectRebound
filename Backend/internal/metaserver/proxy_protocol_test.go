package metaserver

import (
	"bytes"
	"net"
	"testing"
	"time"
)

func TestProxyProtocolPreservesBufferedFrameAndClientAddress(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	go func() {
		var stream bytes.Buffer
		_, _ = stream.WriteString("PROXY TCP4 198.51.100.42 203.0.113.10 49152 443\r\n")
		_ = WriteFrame(&stream, []byte("native"), 64)
		_, _ = client.Write(stream.Bytes())
	}()
	connection, err := acceptProxyProtocolV1(server, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if got := remoteIP(connection.RemoteAddr()); got != "198.51.100.42" {
		t.Fatalf("remote IP = %q", got)
	}
	payload, err := ReadFrame(connection, 64)
	if err != nil || string(payload) != "native" {
		t.Fatalf("buffered frame = %q, %v", payload, err)
	}
}

func TestProxyProtocolRejectsSpoofingFormats(t *testing.T) {
	for _, header := range []string{
		"GET / HTTP/1.1\r\n",
		"PROXY UNKNOWN\r\n",
		"PROXY TCP4 2001:db8::1 203.0.113.10 49152 443\r\n",
		"PROXY TCP4 198.51.100.42 203.0.113.10 0 443\r\n",
	} {
		t.Run(header, func(t *testing.T) {
			server, client := net.Pipe()
			defer server.Close()
			go func() {
				_, _ = client.Write([]byte(header))
				_ = client.Close()
			}()
			if _, err := acceptProxyProtocolV1(server, time.Second); err == nil {
				t.Fatal("invalid PROXY header was accepted")
			}
		})
	}
}
