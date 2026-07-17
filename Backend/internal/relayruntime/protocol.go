package relayruntime

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
)

const (
	ProtocolVersion byte = 1
	Magic                = "PRLY"
	DataHeaderSize       = 39

	messageBind      byte = 1
	messageChallenge byte = 2
	messageBindProof byte = 3
	messageBindOK    byte = 4
	messageData      byte = 5
)

type EndpointRole byte

const (
	RoleHost EndpointRole = 1
	RolePeer EndpointRole = 2
)

func parseRole(value string) (EndpointRole, bool) {
	switch value {
	case "HOST":
		return RoleHost, true
	case "PEER":
		return RolePeer, true
	default:
		return 0, false
	}
}

func encodeChallenge(cookie []byte) []byte {
	packet := make([]byte, 6+len(cookie))
	copy(packet, Magic)
	packet[4] = ProtocolVersion
	packet[5] = messageChallenge
	copy(packet[6:], cookie)
	return packet
}

func encodeBindOK(handle uint64, role EndpointRole) []byte {
	packet := make([]byte, 15)
	copy(packet, Magic)
	packet[4] = ProtocolVersion
	packet[5] = messageBindOK
	binary.BigEndian.PutUint64(packet[6:14], handle)
	packet[14] = byte(role)
	return packet
}

func decodeTokenPacket(packet []byte, expectedType byte, cookieSize int) (cookie, token []byte, err error) {
	base := 8 + cookieSize
	if len(packet) < base || string(packet[:4]) != Magic || packet[4] != ProtocolVersion || packet[5] != expectedType {
		return nil, nil, errors.New("invalid relay token packet")
	}
	lengthOffset := 6 + cookieSize
	tokenLength := int(binary.BigEndian.Uint16(packet[lengthOffset : lengthOffset+2]))
	if tokenLength < 1 || len(packet) != base+tokenLength {
		return nil, nil, errors.New("invalid relay token packet length")
	}
	return packet[6:lengthOffset], packet[base:], nil
}

type dataPacket struct {
	Handle   uint64
	Role     EndpointRole
	Sequence uint64
	Tag      []byte
	Payload  []byte
}

func decodeDataPacket(packet []byte) (dataPacket, error) {
	if len(packet) < DataHeaderSize || string(packet[:4]) != Magic || packet[4] != ProtocolVersion || packet[5] != messageData {
		return dataPacket{}, errors.New("invalid relay data packet")
	}
	role := EndpointRole(packet[14])
	if role != RoleHost && role != RolePeer {
		return dataPacket{}, errors.New("invalid relay endpoint role")
	}
	return dataPacket{
		Handle: binary.BigEndian.Uint64(packet[6:14]), Role: role,
		Sequence: binary.BigEndian.Uint64(packet[15:23]), Tag: packet[23:39], Payload: packet[39:],
	}, nil
}

func encodeDataPacket(handle uint64, role EndpointRole, sequence uint64, key, payload []byte) []byte {
	packet := make([]byte, DataHeaderSize+len(payload))
	copy(packet, Magic)
	packet[4] = ProtocolVersion
	packet[5] = messageData
	binary.BigEndian.PutUint64(packet[6:14], handle)
	packet[14] = byte(role)
	binary.BigEndian.PutUint64(packet[15:23], sequence)
	copy(packet[39:], payload)
	copy(packet[23:39], dataAuthenticationTag(key, packet))
	return packet
}

func dataAuthenticationTag(key, packet []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(packet[:23])
	mac.Write(packet[39:])
	return mac.Sum(nil)[:16]
}

func verifyDataTag(key, packet, supplied []byte) bool {
	return hmac.Equal(dataAuthenticationTag(key, packet), supplied)
}

func deriveDataKey(token string) []byte {
	mac := hmac.New(sha256.New, []byte(token))
	mac.Write([]byte("project-rebound-relay-data-v1"))
	return mac.Sum(nil)
}
