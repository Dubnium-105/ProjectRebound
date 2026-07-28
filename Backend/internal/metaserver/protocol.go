package metaserver

import (
	"errors"

	metaprotocol "github.com/projectrebound/matchserver/internal/metaserver/protocol"
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
	output, _ := proto.Marshal(&metaprotocol.ResponseWrapper{
		MessageId: response.MessageID, RpcPath: response.RPCPath,
		ErrorCode: response.ErrorCode, Message: response.Message,
	})
	return output
}

func EncodeStatusMessage(status int32) []byte {
	output, _ := proto.Marshal(&metaprotocol.StatusResponse{StatusCode: status})
	return output
}
