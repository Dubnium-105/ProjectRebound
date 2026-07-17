// Package relaycontrolpb provides the gRPC bindings for relay_control.proto.
// The protocol intentionally uses google.protobuf.Struct envelopes so the
// bindings remain hand-maintainable when protoc is unavailable in minimal
// build environments.
package relaycontrolpb

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/structpb"
)

const RelayControlConnectFullMethodName = "/projectrebound.relay.v1.RelayControl/Connect"

type RelayControlClient interface {
	Connect(context.Context, ...grpc.CallOption) (RelayControlConnectClient, error)
}

type relayControlClient struct{ connection grpc.ClientConnInterface }

func NewRelayControlClient(connection grpc.ClientConnInterface) RelayControlClient {
	return &relayControlClient{connection: connection}
}

func (client *relayControlClient) Connect(ctx context.Context, options ...grpc.CallOption) (RelayControlConnectClient, error) {
	stream, err := client.connection.NewStream(ctx, &RelayControlServiceDesc.Streams[0], RelayControlConnectFullMethodName, options...)
	if err != nil {
		return nil, err
	}
	return &relayControlConnectClient{ClientStream: stream}, nil
}

type RelayControlConnectClient interface {
	Send(*structpb.Struct) error
	Recv() (*structpb.Struct, error)
	grpc.ClientStream
}

type relayControlConnectClient struct{ grpc.ClientStream }

func (stream *relayControlConnectClient) Send(message *structpb.Struct) error {
	return stream.ClientStream.SendMsg(message)
}

func (stream *relayControlConnectClient) Recv() (*structpb.Struct, error) {
	message := new(structpb.Struct)
	if err := stream.ClientStream.RecvMsg(message); err != nil {
		return nil, err
	}
	return message, nil
}

type RelayControlServer interface {
	Connect(RelayControlConnectServer) error
}

type RelayControlConnectServer interface {
	Send(*structpb.Struct) error
	Recv() (*structpb.Struct, error)
	grpc.ServerStream
}

type relayControlConnectServer struct{ grpc.ServerStream }

func (stream *relayControlConnectServer) Send(message *structpb.Struct) error {
	return stream.ServerStream.SendMsg(message)
}

func (stream *relayControlConnectServer) Recv() (*structpb.Struct, error) {
	message := new(structpb.Struct)
	if err := stream.ServerStream.RecvMsg(message); err != nil {
		return nil, err
	}
	return message, nil
}

func RegisterRelayControlServer(registrar grpc.ServiceRegistrar, server RelayControlServer) {
	registrar.RegisterService(&RelayControlServiceDesc, server)
}

func relayControlConnectHandler(service any, stream grpc.ServerStream) error {
	return service.(RelayControlServer).Connect(&relayControlConnectServer{ServerStream: stream})
}

var RelayControlServiceDesc = grpc.ServiceDesc{
	ServiceName: "projectrebound.relay.v1.RelayControl",
	HandlerType: (*RelayControlServer)(nil),
	Streams: []grpc.StreamDesc{{
		StreamName: "Connect", Handler: relayControlConnectHandler,
		ServerStreams: true, ClientStreams: true,
	}},
	Metadata: "api/proto/relay_control.proto",
}
