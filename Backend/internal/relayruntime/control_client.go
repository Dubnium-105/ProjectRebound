package relayruntime

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"strconv"
	"time"

	"github.com/google/uuid"
	relaycontrolpb "github.com/projectrebound/matchserver/api/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/protobuf/types/known/structpb"
)

var errRelayShutdown = errors.New("relay shutdown requested")

type ControlClient struct {
	config         Config
	identity       Identity
	runtime        *Runtime
	logger         *slog.Logger
	processID      string
	reportSequence uint64
}

func NewControlClient(cfg Config, identity Identity, runtime *Runtime, logger *slog.Logger) *ControlClient {
	return &ControlClient{config: cfg, identity: identity, runtime: runtime, logger: logger, processID: uuid.NewString()}
}

func (c *ControlClient) Run(ctx context.Context) {
	backoff := time.Second
	first := true
	for ctx.Err() == nil {
		if !first {
			c.runtime.metrics.controlReconnects.Add(1)
		}
		first = false
		err := c.connectOnce(ctx)
		c.runtime.metrics.controlConnected.Store(0)
		if errors.Is(err, errRelayShutdown) || ctx.Err() != nil {
			return
		}
		c.logger.WarnContext(ctx, "relay control connection ended", "error", err, "retry_in", backoff)
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		backoff = time.Duration(math.Min(float64(30*time.Second), float64(backoff*2)))
	}
}

func (c *ControlClient) connectOnce(ctx context.Context) error {
	clientCertificate, err := c.identity.TLSCertificate()
	if err != nil {
		return err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(c.identity.CACertificatePEM)) {
		return errors.New("relay control CA certificate is invalid")
	}
	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS13, ServerName: c.config.ControlServerName,
		RootCAs: roots, Certificates: []tls.Certificate{clientCertificate},
	}
	connection, err := grpc.NewClient(c.config.ControlAddr, grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)))
	if err != nil {
		return err
	}
	defer connection.Close()
	stream, err := relaycontrolpb.NewRelayControlClient(connection).Connect(ctx)
	if err != nil {
		return err
	}
	if err := stream.Send(controlEnvelope("Hello", map[string]any{
		"node_id": c.identity.NodeID, "software_version": c.config.SoftwareVersion,
		"protocol_version": c.config.ProtocolVersion,
	})); err != nil {
		return err
	}
	c.runtime.metrics.controlConnected.Store(1)
	c.logger.InfoContext(ctx, "relay control connected", "node_id", c.identity.NodeID)

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
			case <-ctx.Done():
				return
			}
		}
	}()
	heartbeats := time.NewTicker(c.config.HeartbeatInterval())
	defer heartbeats.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-receiveErrors:
			if errors.Is(err, io.EOF) {
				return errors.New("relay control stream closed")
			}
			return err
		case message := <-received:
			if err := c.handleControlMessage(message); err != nil {
				return err
			}
		case <-heartbeats.C:
			active, egress, ingress := c.runtime.Snapshot()
			c.reportSequence++
			snapshot := c.runtime.metrics.Snapshot()
			if err := stream.Send(controlEnvelope("TrafficReport", map[string]any{
				"active_allocations": active, "current_egress_bps": egress, "current_ingress_bps": ingress,
				"load_state": string(c.runtime.LoadState()),
				"process_id": c.processID, "sequence": strconv.FormatUint(c.reportSequence, 10),
				"packets_received_total":   strconv.FormatUint(snapshot.PacketsReceived, 10),
				"packets_forwarded_total":  strconv.FormatUint(snapshot.PacketsForwarded, 10),
				"packets_dropped_total":    strconv.FormatUint(snapshot.PacketsDropped, 10),
				"bytes_forwarded_total":    strconv.FormatUint(snapshot.BytesForwarded, 10),
				"bind_success_total":       strconv.FormatUint(snapshot.BindSuccess, 10),
				"bind_failed_total":        strconv.FormatUint(snapshot.BindFailed, 10),
				"token_invalid_total":      strconv.FormatUint(snapshot.TokenInvalid, 10),
				"rate_limit_drops_total":   strconv.FormatUint(snapshot.RateLimitDrops, 10),
				"control_reconnects_total": strconv.FormatUint(snapshot.ControlReconnects, 10),
			})); err != nil {
				return err
			}
		case event := <-c.runtime.Events():
			if err := stream.Send(controlEnvelope(event.Type, map[string]any{"allocation_id": event.AllocationID})); err != nil {
				return err
			}
		}
	}
}

func (c *ControlClient) handleControlMessage(message *structpb.Struct) error {
	if message == nil {
		return errors.New("empty relay control message")
	}
	fields := message.AsMap()
	messageType, _ := fields["type"].(string)
	payload, _ := fields["payload"].(map[string]any)
	if messageType == "" || payload == nil {
		return errors.New("invalid relay control envelope")
	}
	switch messageType {
	case "ConfigSnapshot", "CertificateRotation":
		return nil
	case "KeysetUpdate":
		encoded, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		var keyset Keyset
		if err := json.Unmarshal(encoded, &keyset); err != nil {
			return err
		}
		return c.runtime.UpdateKeyset(keyset)
	case "EnterDrain":
		c.runtime.SetDraining(true)
		return nil
	case "ExitDrain":
		c.runtime.SetDraining(false)
		return nil
	case "RevokeAllocation":
		allocationID, _ := payload["allocation_id"].(string)
		c.runtime.RevokeAllocation(allocationID)
		return nil
	case "Shutdown":
		c.runtime.SetDraining(true)
		c.runtime.RequestShutdown()
		return errRelayShutdown
	default:
		return errors.New("unsupported relay control message")
	}
}

func controlEnvelope(messageType string, payload map[string]any) *structpb.Struct {
	message, _ := structpb.NewStruct(map[string]any{"type": messageType, "payload": payload})
	return message
}
