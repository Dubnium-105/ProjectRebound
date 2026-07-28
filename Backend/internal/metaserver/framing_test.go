package metaserver

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

type oneByteReader struct{ reader io.Reader }

func (r oneByteReader) Read(p []byte) (int, error) {
	if len(p) > 1 {
		p = p[:1]
	}
	return r.reader.Read(p)
}

func TestReadFrameHandlesFragmentationAndCoalescing(t *testing.T) {
	var stream bytes.Buffer
	if err := WriteFrame(&stream, []byte("first"), 64); err != nil {
		t.Fatal(err)
	}
	if err := WriteFrame(&stream, []byte("second"), 64); err != nil {
		t.Fatal(err)
	}
	reader := oneByteReader{reader: &stream}
	for _, want := range []string{"first", "second"} {
		got, err := ReadFrame(reader, 64)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	}
}

func TestReadFrameRejectsInvalidLengths(t *testing.T) {
	for _, test := range []struct {
		name string
		data []byte
		err  error
	}{
		{name: "empty", data: []byte{0, 0, 0, 0}, err: ErrEmptyFrame},
		{name: "large", data: []byte{0, 0, 0, 9}, err: ErrFrameTooLarge},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := ReadFrame(bytes.NewReader(test.data), 8)
			if !errors.Is(err, test.err) {
				t.Fatalf("got %v, want %v", err, test.err)
			}
		})
	}
}

func TestReadFrameWithDeadlinesTimesOutIncompletePayload(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	go func() {
		var header [4]byte
		binary.BigEndian.PutUint32(header[:], 8)
		_, _ = client.Write(header[:])
		time.Sleep(200 * time.Millisecond)
	}()
	started := time.Now()
	_, err := ReadFrameWithDeadlines(server, 64, time.Second, 40*time.Millisecond)
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("incomplete payload error = %v, want timeout", err)
	}
	if time.Since(started) > 500*time.Millisecond {
		t.Fatal("payload deadline did not bound the incomplete frame")
	}
}

func FuzzReadFrame(f *testing.F) {
	f.Add([]byte{0, 0, 0, 1, 0x42})
	f.Add([]byte{0, 0, 0, 0})
	f.Add([]byte{0, 0, 0x10, 0})
	f.Fuzz(func(t *testing.T, input []byte) {
		payload, err := ReadFrame(bytes.NewReader(input), 4096)
		if err == nil && (len(payload) == 0 || len(payload) > 4096) {
			t.Fatalf("invalid accepted payload length: %d", len(payload))
		}
	})
}
