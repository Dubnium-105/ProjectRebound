package relayclient

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"time"
)

const (
	magic             = "PRLY"
	protocolVersion   = byte(2)
	messageBindInit   = byte(1)
	messageChallenge  = byte(2)
	messageBindProof  = byte(3)
	messageBindOK     = byte(4)
	messageData       = byte(5)
	nonceSize         = 16
	cookieSize        = 32
	dataHeaderSize    = 40
	maximumTokenBytes = 16 * 1024
)

type Client struct {
	connection *net.UDPConn
	handle     uint64
	role       byte
	mtu        uint16
	key        []byte
	sequence   uint64
}

func Dial(ctx context.Context, endpoint, token string, requestedMTU uint16) (*Client, error) {
	if token == "" || len(token) > maximumTokenBytes || requestedMTU < 1000 || requestedMTU > 1350 {
		return nil, errors.New("invalid Relay bind parameters")
	}
	address, err := net.ResolveUDPAddr("udp", endpoint)
	if err != nil {
		return nil, fmt.Errorf("resolve Relay endpoint: %w", err)
	}
	connection, err := net.DialUDP("udp", nil, address)
	if err != nil {
		return nil, fmt.Errorf("dial Relay endpoint: %w", err)
	}
	client := &Client{connection: connection, mtu: requestedMTU, key: deriveKey(token)}
	if err := client.bind(ctx, []byte(token)); err != nil {
		_ = connection.Close()
		return nil, err
	}
	return client, nil
}

func (c *Client) Close() error { return c.connection.Close() }

func (c *Client) Send(ctx context.Context, payload []byte) error {
	if len(payload) > int(c.mtu) {
		return errors.New("Relay payload exceeds negotiated MTU")
	}
	c.sequence++
	packet := encodeData(c.handle, c.role, c.sequence, c.key, payload)
	if err := setDeadline(ctx, c.connection); err != nil {
		return err
	}
	_, err := c.connection.Write(packet)
	return err
}

func (c *Client) Receive(ctx context.Context) ([]byte, error) {
	if err := setDeadline(ctx, c.connection); err != nil {
		return nil, err
	}
	buffer := make([]byte, dataHeaderSize+int(c.mtu))
	length, err := c.connection.Read(buffer)
	if err != nil {
		return nil, err
	}
	packet := buffer[:length]
	if len(packet) < dataHeaderSize || string(packet[:4]) != magic || packet[4] != protocolVersion || packet[5] != messageData ||
		binary.BigEndian.Uint64(packet[6:14]) != c.handle || packet[15] != 0 || !hmac.Equal(packet[24:40], authenticationTag(c.key, packet)) {
		return nil, errors.New("invalid authenticated Relay data packet")
	}
	return append([]byte(nil), packet[dataHeaderSize:]...), nil
}

func (c *Client) bind(ctx context.Context, token []byte) error {
	requestedMTU := c.mtu
	clientNonce := make([]byte, nonceSize)
	if _, err := rand.Read(clientNonce); err != nil {
		return err
	}
	init := make([]byte, 6+nonceSize+4+len(token))
	copy(init, magic)
	init[4], init[5] = protocolVersion, messageBindInit
	copy(init[6:6+nonceSize], clientNonce)
	binary.BigEndian.PutUint16(init[6+nonceSize:8+nonceSize], c.mtu)
	binary.BigEndian.PutUint16(init[8+nonceSize:10+nonceSize], uint16(len(token)))
	copy(init[10+nonceSize:], token)
	challenge, err := c.exchange(ctx, init, 6+nonceSize+4+cookieSize)
	if err != nil {
		return fmt.Errorf("Relay BIND_INIT: %w", err)
	}
	if string(challenge[:4]) != magic || challenge[4] != protocolVersion || challenge[5] != messageChallenge {
		return errors.New("invalid Relay BIND_CHALLENGE")
	}
	serverNonce := challenge[6 : 6+nonceSize]
	cookie := challenge[10+nonceSize : 10+nonceSize+cookieSize]
	proof := make([]byte, 6+nonceSize+nonceSize+2+cookieSize+2+len(token))
	copy(proof, magic)
	proof[4], proof[5] = protocolVersion, messageBindProof
	copy(proof[6:6+nonceSize], clientNonce)
	copy(proof[6+nonceSize:6+2*nonceSize], serverNonce)
	binary.BigEndian.PutUint16(proof[6+2*nonceSize:8+2*nonceSize], c.mtu)
	copy(proof[8+2*nonceSize:8+2*nonceSize+cookieSize], cookie)
	tokenLengthOffset := 8 + 2*nonceSize + cookieSize
	binary.BigEndian.PutUint16(proof[tokenLengthOffset:tokenLengthOffset+2], uint16(len(token)))
	copy(proof[tokenLengthOffset+2:], token)
	ok, err := c.exchange(ctx, proof, 17)
	if err != nil {
		return fmt.Errorf("Relay BIND_PROOF: %w", err)
	}
	if string(ok[:4]) != magic || ok[4] != protocolVersion || ok[5] != messageBindOK {
		return errors.New("invalid Relay BIND_OK")
	}
	c.handle = binary.BigEndian.Uint64(ok[6:14])
	c.role = ok[14]
	c.mtu = binary.BigEndian.Uint16(ok[15:17])
	if c.handle == 0 || (c.role != 1 && c.role != 2) || c.mtu < 1000 || c.mtu > requestedMTU {
		return errors.New("invalid Relay bind negotiation")
	}
	return nil
}

func (c *Client) exchange(ctx context.Context, packet []byte, expectedLength int) ([]byte, error) {
	if err := setDeadline(ctx, c.connection); err != nil {
		return nil, err
	}
	if _, err := c.connection.Write(packet); err != nil {
		return nil, err
	}
	response := make([]byte, expectedLength)
	length, err := c.connection.Read(response)
	if err != nil {
		return nil, err
	}
	if length != expectedLength {
		return nil, fmt.Errorf("unexpected response length %d", length)
	}
	return response, nil
}

func setDeadline(ctx context.Context, connection *net.UDPConn) error {
	maximum := time.Now().Add(10 * time.Second)
	deadline, ok := ctx.Deadline()
	if !ok || deadline.After(maximum) {
		deadline = maximum
	}
	return connection.SetDeadline(deadline)
}

func deriveKey(token string) []byte {
	mac := hmac.New(sha256.New, []byte(token))
	_, _ = mac.Write([]byte("project-rebound-relay-data-v2"))
	return mac.Sum(nil)
}

func encodeData(handle uint64, role byte, sequence uint64, key, payload []byte) []byte {
	packet := make([]byte, dataHeaderSize+len(payload))
	copy(packet, magic)
	packet[4], packet[5] = protocolVersion, messageData
	binary.BigEndian.PutUint64(packet[6:14], handle)
	packet[14], packet[15] = role, 0
	binary.BigEndian.PutUint64(packet[16:24], sequence)
	copy(packet[dataHeaderSize:], payload)
	copy(packet[24:40], authenticationTag(key, packet))
	return packet
}

func authenticationTag(key, packet []byte) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(packet[:24])
	_, _ = mac.Write(packet[dataHeaderSize:])
	return mac.Sum(nil)[:16]
}
