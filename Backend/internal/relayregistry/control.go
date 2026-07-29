package relayregistry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"

	relaycontrolpb "github.com/Dubnium-105/ProjectRebound/Backend/api/proto"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/config"
)

type ControlMessage struct {
	Type    string
	Payload map[string]any
}

type ControlSubscription struct {
	nodeID string
	events chan ControlMessage
	hub    *ControlHub
	once   sync.Once
}

func (s *ControlSubscription) Events() <-chan ControlMessage { return s.events }
func (s *ControlSubscription) Close() {
	s.once.Do(func() { s.hub.unregister(s) })
}

type ControlHub struct {
	mu          sync.RWMutex
	subscribers map[string]map[*ControlSubscription]struct{}
}

func NewControlHub() *ControlHub {
	return &ControlHub{subscribers: make(map[string]map[*ControlSubscription]struct{})}
}

func (h *ControlHub) Register(nodeID string) *ControlSubscription {
	subscription := &ControlSubscription{nodeID: nodeID, events: make(chan ControlMessage, 32), hub: h}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.subscribers[nodeID] == nil {
		h.subscribers[nodeID] = make(map[*ControlSubscription]struct{})
	}
	h.subscribers[nodeID][subscription] = struct{}{}
	return subscription
}

func (h *ControlHub) Publish(nodeID string, message ControlMessage) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for subscription := range h.subscribers[nodeID] {
		select {
		case subscription.events <- message:
		default:
		}
	}
}

func (h *ControlHub) unregister(subscription *ControlSubscription) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.subscribers[subscription.nodeID], subscription)
	if len(h.subscribers[subscription.nodeID]) == 0 {
		delete(h.subscribers, subscription.nodeID)
	}
}

type ControlServer struct {
	service    *Service
	hub        *ControlHub
	config     config.RelayRegistryConfig
	grpcServer *grpc.Server
	logger     *slog.Logger
	telemetry  *TelemetryStore
}

func NewControlServer(service *Service, hub *ControlHub, telemetry *TelemetryStore, authority *Authority, cfg config.RelayRegistryConfig, logger *slog.Logger) (*ControlServer, error) {
	tlsConfig, err := authority.ServerTLSConfig()
	if err != nil {
		return nil, fmt.Errorf("create relay control TLS configuration: %w", err)
	}
	server := &ControlServer{service: service, hub: hub, telemetry: telemetry, config: cfg, logger: logger}
	server.grpcServer = grpc.NewServer(grpc.Creds(credentials.NewTLS(tlsConfig)))
	relaycontrolpb.RegisterRelayControlServer(server.grpcServer, server)
	return server, nil
}

func (s *ControlServer) Run(ctx context.Context) {
	listener, err := net.Listen("tcp", s.config.ControlAddr)
	if err != nil {
		s.logger.ErrorContext(ctx, "relay control listener failed", "address", s.config.ControlAddr, "error", err)
		return
	}
	s.logger.InfoContext(ctx, "relay control listening", "address", s.config.ControlAddr, "mtls", true)
	serveErrors := make(chan error, 1)
	go func() { serveErrors <- s.grpcServer.Serve(listener) }()
	select {
	case <-ctx.Done():
		stopped := make(chan struct{})
		go func() {
			s.grpcServer.GracefulStop()
			close(stopped)
		}()
		select {
		case <-stopped:
		case <-time.After(5 * time.Second):
			s.grpcServer.Stop()
		}
	case err := <-serveErrors:
		if err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			s.logger.ErrorContext(ctx, "relay control server stopped", "error", err)
		}
	}
}

func (s *ControlServer) Connect(stream relaycontrolpb.RelayControlConnectServer) error {
	fingerprint, err := peerCertificateFingerprint(stream.Context())
	if err != nil {
		return status.Error(codes.Unauthenticated, "valid relay client certificate required")
	}
	first, err := stream.Recv()
	if err != nil {
		return status.Error(codes.InvalidArgument, "Hello must be the first relay message")
	}
	messageType, payload, err := decodeControlEnvelope(first)
	if err != nil || messageType != "Hello" {
		return status.Error(codes.InvalidArgument, "Hello must be the first relay message")
	}
	nodeID := stringField(payload, "node_id")
	softwareVersion := stringField(payload, "software_version")
	protocolVersion := int(numberField(payload, "protocol_version"))
	node, err := s.service.MarkConnecting(stream.Context(), nodeID, fingerprint, softwareVersion, protocolVersion)
	if err != nil {
		return status.Error(codes.PermissionDenied, "relay node identity rejected")
	}
	subscription := s.hub.Register(nodeID)
	defer subscription.Close()
	s.telemetry.SetConnected(nodeID, true)
	defer s.telemetry.SetConnected(nodeID, false)
	configPayload := map[string]any{
		"config_version":             node.ConfigVersion,
		"heartbeat_interval_seconds": s.config.HeartbeatIntervalSeconds,
		"lease_seconds":              s.config.UnhealthyAfterSeconds,
		"node_state":                 string(node.State),
		"drain_migrate_existing":     node.DrainMigrateExisting,
	}
	if node.DrainDeadline != nil {
		configPayload["drain_deadline"] = node.DrainDeadline.Format(time.RFC3339Nano)
		configPayload["deadline"] = node.DrainDeadline.Format(time.RFC3339Nano)
	}
	if err := sendControlMessage(stream, ControlMessage{Type: "ConfigSnapshot", Payload: configPayload}); err != nil {
		return err
	}
	keysetPayload, _ := toMap(s.service.Keyset())
	if err := sendControlMessage(stream, ControlMessage{Type: "KeysetUpdate", Payload: keysetPayload}); err != nil {
		return err
	}

	received := make(chan *structpb.Struct, 1)
	receiveErrors := make(chan error, 1)
	go func() {
		for {
			message, err := stream.Recv()
			if err != nil {
				receiveErrors <- err
				return
			}
			select {
			case received <- message:
			case <-stream.Context().Done():
				return
			}
		}
	}()
	for {
		select {
		case <-stream.Context().Done():
			return stream.Context().Err()
		case err := <-receiveErrors:
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		case message := <-received:
			if err := s.handleNodeMessage(stream.Context(), nodeID, message); err != nil {
				return err
			}
		case command := <-subscription.Events():
			if err := sendControlMessage(stream, command); err != nil {
				return err
			}
		}
	}
}

func (s *ControlServer) handleNodeMessage(ctx context.Context, nodeID string, message *structpb.Struct) error {
	messageType, payload, err := decodeControlEnvelope(message)
	if err != nil {
		return status.Error(codes.InvalidArgument, "invalid relay message envelope")
	}
	switch messageType {
	case "Heartbeat", "CapacityReport", "TrafficReport":
		_, err := s.service.Heartbeat(ctx, nodeID, HeartbeatInput{
			ActiveAllocations: int(numberField(payload, "active_allocations")),
			CurrentEgressBPS:  int64(numberField(payload, "current_egress_bps")),
			CurrentIngressBPS: int64(numberField(payload, "current_ingress_bps")),
			LoadState:         LoadState(stringField(payload, "load_state")),
		})
		if err != nil {
			return status.Error(codes.FailedPrecondition, "relay heartbeat rejected")
		}
		// TrafficReport existed before detailed counters were standardized. Keep
		// capacity-only reports compatible and record telemetry when the new
		// process identity is present.
		if messageType == "TrafficReport" && stringField(payload, "process_id") != "" {
			if err := s.telemetry.Record(nodeID, payload, time.Now()); err != nil {
				return status.Error(codes.InvalidArgument, "invalid relay traffic report")
			}
		}
	case "AllocationOpened":
		if err := s.service.AllocationOpened(ctx, nodeID, stringField(payload, "allocation_id")); err != nil {
			return status.Error(codes.FailedPrecondition, "allocation open report rejected")
		}
	case "AllocationClosed":
		if err := s.service.AllocationClosed(ctx, nodeID, stringField(payload, "allocation_id")); err != nil {
			return status.Error(codes.FailedPrecondition, "allocation close report rejected")
		}
	case "RuntimeError", "DrainCompleted":
	case "KeysetLoaded":
		if err := s.service.KeysetLoaded(ctx, nodeID, int64(numberField(payload, "keyset_version"))); err != nil {
			return status.Error(codes.FailedPrecondition, "relay keyset acknowledgement rejected")
		}
	default:
		return status.Error(codes.InvalidArgument, "unsupported relay node message type")
	}
	return nil
}

func peerCertificateFingerprint(ctx context.Context) (string, error) {
	peerInfo, ok := peer.FromContext(ctx)
	if !ok {
		return "", errors.New("peer information unavailable")
	}
	tlsInfo, ok := peerInfo.AuthInfo.(credentials.TLSInfo)
	if !ok || len(tlsInfo.State.PeerCertificates) == 0 {
		return "", errors.New("TLS client certificate unavailable")
	}
	fingerprint := sha256.Sum256(tlsInfo.State.PeerCertificates[0].Raw)
	return hex.EncodeToString(fingerprint[:]), nil
}

func decodeControlEnvelope(message *structpb.Struct) (string, map[string]any, error) {
	if message == nil {
		return "", nil, errors.New("empty message")
	}
	fields := message.AsMap()
	messageType, _ := fields["type"].(string)
	payload, _ := fields["payload"].(map[string]any)
	if strings.TrimSpace(messageType) == "" || payload == nil {
		return "", nil, errors.New("type and payload are required")
	}
	return messageType, payload, nil
}

func sendControlMessage(stream relaycontrolpb.RelayControlConnectServer, message ControlMessage) error {
	envelope, err := structpb.NewStruct(map[string]any{"type": message.Type, "payload": message.Payload})
	if err != nil {
		return err
	}
	return stream.Send(envelope)
}

func stringField(payload map[string]any, name string) string {
	value, _ := payload[name].(string)
	return strings.TrimSpace(value)
}

func numberField(payload map[string]any, name string) float64 {
	value, _ := payload[name].(float64)
	return value
}

func toMap(value any) (map[string]any, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	err = json.Unmarshal(encoded, &result)
	return result, err
}
