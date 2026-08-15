package metaserver

import (
	"errors"

	metaprotocol "github.com/Dubnium-105/ProjectRebound/Backend/internal/metaserver/protocol"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

type RequestWrapper struct {
	MessageID int32
	RPCPath   string
	Message   []byte
}

type ResponseWrapper struct {
	MessageID int32
	RPCPath   string
	ErrorCode int32
	Message   []byte
}

// The wrapper codec uses generated static Go messages. Protocol definitions
// are never loaded from disk at runtime, and unknown fields remain
// forward-compatible.
func DecodeRequestWrapper(data []byte) (RequestWrapper, error) {
	var wire metaprotocol.RequestWrapper
	if err := proto.Unmarshal(data, &wire); err != nil {
		return RequestWrapper{}, err
	}
	result := RequestWrapper{
		MessageID: wire.GetMessageId(),
		RPCPath:   wire.GetRpcPath(),
		Message:   append([]byte(nil), wire.GetMessage()...),
	}
	if result.RPCPath == "" {
		return RequestWrapper{}, errors.New("RPCPath is required")
	}
	return result, nil
}

func EncodeResponseWrapper(response ResponseWrapper) []byte {
	// The original protobufjs server serializes ErrorCode whenever the property
	// is present, including success (18 00). Boundary's native async task starts
	// with 404 and only overwrites it when field 3 is present, so generated Go
	// proto3 output (which omits scalar zero) makes a successful archive update
	// complete as UNKNOWN FAILURE. Keep ordinary proto3 omission rules for the
	// other fields, but always put the transport completion code on the wire.
	var output []byte
	if response.MessageID != 0 {
		output = protowire.AppendTag(output, 1, protowire.VarintType)
		output = protowire.AppendVarint(output, uint64(response.MessageID))
	}
	if response.RPCPath != "" {
		output = protowire.AppendTag(output, 2, protowire.BytesType)
		output = protowire.AppendString(output, response.RPCPath)
	}
	output = protowire.AppendTag(output, 3, protowire.VarintType)
	output = protowire.AppendVarint(output, uint64(response.ErrorCode))
	if len(response.Message) != 0 {
		output = protowire.AppendTag(output, 4, protowire.BytesType)
		output = protowire.AppendBytes(output, response.Message)
	}
	return output
}

func EncodeStatusMessage(status int32) []byte {
	// Boundary distinguishes an explicit success field (08 00) from an empty
	// response. Generated proto3 serializers omit scalar zero values, which
	// leaves the client's async completion code at its initial 404 and makes a
	// successfully persisted equipment change surface as "UNKNOWN FAILURE".
	output := protowire.AppendTag(nil, 1, protowire.VarintType)
	return protowire.AppendVarint(output, uint64(status))
}
