package relayruntime

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"strconv"
	"time"
)

const (
	ProtocolVersion   byte = 2
	ProtocolVersionV1 byte = 1
	Magic                  = "PRLY"
	DataHeaderSizeV1       = 39
	DataHeaderSize         = 40
	NonceSize              = 16

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

func encodeChallengeV1(cookie []byte) []byte {
	packet := make([]byte, 6+len(cookie))
	copy(packet, Magic)
	packet[4] = ProtocolVersionV1
	packet[5] = messageChallenge
	copy(packet[6:], cookie)
	return packet
}

func encodeChallengeV2(serverNonce, cookie []byte, expiresIn time.Duration) []byte {
	packet := make([]byte, 6+NonceSize+4+sha256.Size)
	copy(packet, Magic)
	packet[4], packet[5] = ProtocolVersion, messageChallenge
	copy(packet[6:6+NonceSize], serverNonce)
	binary.BigEndian.PutUint32(packet[6+NonceSize:10+NonceSize], uint32(expiresIn/time.Millisecond))
	copy(packet[10+NonceSize:], cookie)
	return packet
}

func encodeBindOKVersion(version byte, handle uint64, role EndpointRole, mtu uint16) []byte {
	length := 15
	if version == ProtocolVersion {
		length = 17
	}
	packet := make([]byte, length)
	copy(packet, Magic)
	packet[4] = version
	packet[5] = messageBindOK
	binary.BigEndian.PutUint64(packet[6:14], handle)
	packet[14] = byte(role)
	if version == ProtocolVersion {
		binary.BigEndian.PutUint16(packet[15:17], mtu)
	}
	return packet
}

func decodeV1TokenPacket(packet []byte, expectedType byte, cookieSize int) (cookie, token []byte, err error) {
	base := 8 + cookieSize
	if len(packet) < base || string(packet[:4]) != Magic || packet[4] != ProtocolVersionV1 || packet[5] != expectedType {
		return nil, nil, errors.New("invalid relay token packet")
	}
	lengthOffset := 6 + cookieSize
	tokenLength := int(binary.BigEndian.Uint16(packet[lengthOffset : lengthOffset+2]))
	if tokenLength < 1 || len(packet) != base+tokenLength {
		return nil, nil, errors.New("invalid relay token packet length")
	}
	return packet[6:lengthOffset], packet[base:], nil
}

type bindInitPacket struct {
	ClientNonce  []byte
	RequestedMTU uint16
	Token        []byte
}

func decodeBindInitV2(packet []byte) (bindInitPacket, error) {
	const base = 6 + NonceSize + 2 + 2
	if len(packet) < base || string(packet[:4]) != Magic || packet[4] != ProtocolVersion || packet[5] != messageBind {
		return bindInitPacket{}, errors.New("invalid relay bind init")
	}
	tokenLength := int(binary.BigEndian.Uint16(packet[6+NonceSize+2 : base]))
	if tokenLength < 1 || len(packet) != base+tokenLength {
		return bindInitPacket{}, errors.New("invalid relay bind init length")
	}
	return bindInitPacket{
		ClientNonce:  packet[6 : 6+NonceSize],
		RequestedMTU: binary.BigEndian.Uint16(packet[6+NonceSize : 6+NonceSize+2]),
		Token:        packet[base:],
	}, nil
}

type bindProofPacket struct {
	ClientNonce  []byte
	ServerNonce  []byte
	RequestedMTU uint16
	Cookie       []byte
	Token        []byte
}

func decodeBindProofV2(packet []byte) (bindProofPacket, error) {
	const base = 6 + NonceSize + NonceSize + 2 + sha256.Size + 2
	if len(packet) < base || string(packet[:4]) != Magic || packet[4] != ProtocolVersion || packet[5] != messageBindProof {
		return bindProofPacket{}, errors.New("invalid relay bind proof")
	}
	tokenLength := int(binary.BigEndian.Uint16(packet[base-2 : base]))
	if tokenLength < 1 || len(packet) != base+tokenLength {
		return bindProofPacket{}, errors.New("invalid relay bind proof length")
	}
	return bindProofPacket{
		ClientNonce: packet[6 : 6+NonceSize], ServerNonce: packet[6+NonceSize : 6+2*NonceSize],
		RequestedMTU: binary.BigEndian.Uint16(packet[6+2*NonceSize : 8+2*NonceSize]),
		Cookie:       packet[8+2*NonceSize : 8+2*NonceSize+sha256.Size], Token: packet[base:],
	}, nil
}

type dataPacket struct {
	Version  byte
	Handle   uint64
	Role     EndpointRole
	Flags    byte
	Sequence uint64
	Tag      []byte
	Payload  []byte
}

func decodeDataPacket(packet []byte) (dataPacket, error) {
	if len(packet) < DataHeaderSizeV1 || string(packet[:4]) != Magic ||
		(packet[4] != ProtocolVersion && packet[4] != ProtocolVersionV1) || packet[5] != messageData {
		return dataPacket{}, errors.New("invalid relay data packet")
	}
	role := EndpointRole(packet[14])
	if role != RoleHost && role != RolePeer {
		return dataPacket{}, errors.New("invalid relay endpoint role")
	}
	if packet[4] == ProtocolVersionV1 {
		return dataPacket{
			Version: packet[4], Handle: binary.BigEndian.Uint64(packet[6:14]), Role: role,
			Sequence: binary.BigEndian.Uint64(packet[15:23]), Tag: packet[23:39], Payload: packet[39:],
		}, nil
	}
	if len(packet) < DataHeaderSize || packet[15] != 0 {
		return dataPacket{}, errors.New("invalid relay data flags")
	}
	return dataPacket{
		Version: packet[4], Handle: binary.BigEndian.Uint64(packet[6:14]), Role: role, Flags: packet[15],
		Sequence: binary.BigEndian.Uint64(packet[16:24]), Tag: packet[24:40], Payload: packet[40:],
	}, nil
}

func encodeDataPacket(handle uint64, role EndpointRole, sequence uint64, key, payload []byte) []byte {
	return encodeDataPacketVersion(ProtocolVersion, handle, role, sequence, key, payload)
}

func encodeDataPacketVersion(version byte, handle uint64, role EndpointRole, sequence uint64, key, payload []byte) []byte {
	headerSize := DataHeaderSize
	if version == ProtocolVersionV1 {
		headerSize = DataHeaderSizeV1
	}
	packet := make([]byte, headerSize+len(payload))
	copy(packet, Magic)
	packet[4] = version
	packet[5] = messageData
	binary.BigEndian.PutUint64(packet[6:14], handle)
	packet[14] = byte(role)
	if version == ProtocolVersionV1 {
		binary.BigEndian.PutUint64(packet[15:23], sequence)
		copy(packet[39:], payload)
		copy(packet[23:39], dataAuthenticationTag(key, packet))
	} else {
		packet[15] = 0
		binary.BigEndian.PutUint64(packet[16:24], sequence)
		copy(packet[40:], payload)
		copy(packet[24:40], dataAuthenticationTag(key, packet))
	}
	return packet
}

func dataAuthenticationTag(key, packet []byte) []byte {
	mac := hmac.New(sha256.New, key)
	if len(packet) >= DataHeaderSize && packet[4] == ProtocolVersion {
		mac.Write(packet[:24])
		mac.Write(packet[40:])
	} else {
		mac.Write(packet[:23])
		mac.Write(packet[39:])
	}
	return mac.Sum(nil)[:16]
}

func verifyDataTag(key, packet, supplied []byte) bool {
	return hmac.Equal(dataAuthenticationTag(key, packet), supplied)
}

func deriveDataKey(token string) []byte {
	return deriveDataKeyVersion(token, ProtocolVersion)
}

func deriveDataKeyVersion(token string, version byte) []byte {
	mac := hmac.New(sha256.New, []byte(token))
	mac.Write([]byte("project-rebound-relay-data-v" + strconv.Itoa(int(version))))
	return mac.Sum(nil)
}
