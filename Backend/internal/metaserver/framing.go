package metaserver

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

var (
	ErrEmptyFrame    = errors.New("meta protocol frame is empty")
	ErrFrameTooLarge = errors.New("meta protocol frame exceeds configured maximum")
)

// ReadFrame reads the native protocol's four-byte big-endian length prefix and
// then exactly one payload. io.ReadFull makes fragmented TCP packets and sticky
// packets behave identically.
func ReadFrame(reader io.Reader, maximum int) ([]byte, error) {
	length, err := readFrameLength(reader, maximum)
	if err != nil {
		return nil, err
	}
	return readFramePayload(reader, length)
}

// ReadFrameWithDeadlines allows a connection to remain idle while bounding the
// time allowed to finish a frame after its length prefix arrives. This prevents
// a slow sender from extending an incomplete frame for the full idle timeout.
func ReadFrameWithDeadlines(
	connection net.Conn,
	maximum int,
	headerTimeout, payloadTimeout time.Duration,
) ([]byte, error) {
	if err := connection.SetReadDeadline(time.Now().Add(headerTimeout)); err != nil {
		return nil, err
	}
	length, err := readFrameLength(connection, maximum)
	if err != nil {
		return nil, err
	}
	if err := connection.SetReadDeadline(time.Now().Add(payloadTimeout)); err != nil {
		return nil, err
	}
	return readFramePayload(connection, length)
}

func readFrameLength(reader io.Reader, maximum int) (int, error) {
	if maximum < 1 {
		return 0, errors.New("maximum frame size must be positive")
	}
	var header [4]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return 0, err
	}
	length := int(binary.BigEndian.Uint32(header[:]))
	if length == 0 {
		return 0, ErrEmptyFrame
	}
	if length > maximum {
		return 0, fmt.Errorf("%w: %d > %d", ErrFrameTooLarge, length, maximum)
	}
	return length, nil
}

func readFramePayload(reader io.Reader, length int) ([]byte, error) {
	payload := make([]byte, length)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func WriteFrame(writer io.Writer, payload []byte, maximum int) error {
	if len(payload) == 0 {
		return ErrEmptyFrame
	}
	if len(payload) > maximum {
		return fmt.Errorf("%w: %d > %d", ErrFrameTooLarge, len(payload), maximum)
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(payload)))
	if _, err := writer.Write(header[:]); err != nil {
		return err
	}
	_, err := writer.Write(payload)
	return err
}
