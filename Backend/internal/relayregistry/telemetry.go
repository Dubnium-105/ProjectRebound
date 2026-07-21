package relayregistry

import (
	"context"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"
)

type TrafficTelemetry struct {
	ProcessID         string
	Sequence          uint64
	ReceivedAt        time.Time
	Connected         bool
	PacketsReceived   uint64
	BytesReceived     uint64
	PacketsForwarded  uint64
	PacketsDropped    uint64
	BytesForwarded    uint64
	BindSuccess       uint64
	BindFailed        uint64
	BindInit          uint64
	BindChallenge     uint64
	CookieInvalid     uint64
	TokenInvalid      uint64
	TokenReplay       uint64
	PacketAuthFailed  uint64
	PacketReplay      uint64
	PacketTooLarge    uint64
	RateLimitDrops    uint64
	ControlReconnects uint64
	LoadState         string
	LoadRatio         float64
	Goroutines        uint64
	MemoryBytes       uint64
}

type TelemetryStore struct {
	mu          sync.RWMutex
	reports     map[string]TrafficTelemetry
	connections map[string]int
}

func NewTelemetryStore() *TelemetryStore {
	return &TelemetryStore{reports: make(map[string]TrafficTelemetry), connections: make(map[string]int)}
}

func (s *TelemetryStore) SetConnected(nodeID string, connected bool) {
	s.mu.Lock()
	if connected {
		s.connections[nodeID]++
	} else if s.connections[nodeID] > 1 {
		s.connections[nodeID]--
	} else {
		delete(s.connections, nodeID)
	}
	s.mu.Unlock()
}

func (s *TelemetryStore) Record(nodeID string, payload map[string]any, receivedAt time.Time) error {
	processID := stringField(payload, "process_id")
	if processID == "" {
		return fmt.Errorf("process_id is required")
	}
	sequence, err := uint64Field(payload, "sequence")
	if err != nil || sequence == 0 {
		return fmt.Errorf("valid sequence is required")
	}
	report := TrafficTelemetry{ProcessID: processID, Sequence: sequence, ReceivedAt: receivedAt.UTC()}
	report.LoadState = stringField(payload, "load_state")
	if report.LoadState != "" && report.LoadState != "NORMAL" && report.LoadState != "DEGRADED" &&
		report.LoadState != "REJECT_NEW" && report.LoadState != "DRAINING" {
		return fmt.Errorf("invalid relay load_state")
	}
	fields := []struct {
		name   string
		target *uint64
	}{
		{"packets_received_total", &report.PacketsReceived},
		{"packets_forwarded_total", &report.PacketsForwarded},
		{"packets_dropped_total", &report.PacketsDropped},
		{"bytes_forwarded_total", &report.BytesForwarded},
		{"bind_success_total", &report.BindSuccess},
		{"bind_failed_total", &report.BindFailed},
		{"token_invalid_total", &report.TokenInvalid},
		{"rate_limit_drops_total", &report.RateLimitDrops},
		{"control_reconnects_total", &report.ControlReconnects},
	}
	for _, field := range fields {
		value, fieldErr := uint64Field(payload, field.name)
		if fieldErr != nil {
			return fieldErr
		}
		*field.target = value
	}
	optionalFields := []struct {
		name   string
		target *uint64
	}{
		{"bytes_received_total", &report.BytesReceived},
		{"bind_init_total", &report.BindInit},
		{"bind_challenge_total", &report.BindChallenge},
		{"cookie_invalid_total", &report.CookieInvalid},
		{"token_replay_total", &report.TokenReplay},
		{"packet_auth_failed_total", &report.PacketAuthFailed},
		{"packet_replay_total", &report.PacketReplay},
		{"packet_too_large_total", &report.PacketTooLarge},
		{"goroutines", &report.Goroutines},
		{"memory_bytes", &report.MemoryBytes},
	}
	for _, field := range optionalFields {
		value, present, fieldErr := optionalUint64Field(payload, field.name)
		if fieldErr != nil {
			return fieldErr
		}
		if present {
			*field.target = value
		}
	}
	if value, present, fieldErr := optionalFloat64Field(payload, "load_ratio"); fieldErr != nil {
		return fieldErr
	} else if present {
		if value < 0 || value > 10 {
			return fmt.Errorf("load_ratio is outside the accepted range")
		}
		report.LoadRatio = value
	}
	s.mu.Lock()
	current, exists := s.reports[nodeID]
	if !exists || current.ProcessID != processID || sequence > current.Sequence {
		s.reports[nodeID] = report
	}
	s.mu.Unlock()
	return nil
}

func (s *TelemetryStore) Snapshot() map[string]TrafficTelemetry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make(map[string]TrafficTelemetry, len(s.reports))
	for nodeID, report := range s.reports {
		report.Connected = s.connections[nodeID] > 0
		result[nodeID] = report
	}
	for nodeID, count := range s.connections {
		if _, exists := result[nodeID]; !exists {
			result[nodeID] = TrafficTelemetry{Connected: count > 0}
		}
	}
	return result
}

func uint64Field(payload map[string]any, name string) (uint64, error) {
	switch value := payload[name].(type) {
	case string:
		parsed, err := strconv.ParseUint(value, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("%s must be an unsigned integer", name)
		}
		return parsed, nil
	case float64:
		if value < 0 || value > 1<<53-1 || value != math.Trunc(value) {
			return 0, fmt.Errorf("%s must be an unsigned integer", name)
		}
		return uint64(value), nil
	default:
		return 0, fmt.Errorf("%s is required", name)
	}
}

func optionalUint64Field(payload map[string]any, name string) (uint64, bool, error) {
	if _, exists := payload[name]; !exists {
		return 0, false, nil
	}
	value, err := uint64Field(payload, name)
	return value, true, err
}

func optionalFloat64Field(payload map[string]any, name string) (float64, bool, error) {
	value, exists := payload[name]
	if !exists {
		return 0, false, nil
	}
	switch typed := value.(type) {
	case string:
		parsed, err := strconv.ParseFloat(typed, 64)
		if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
			return 0, true, fmt.Errorf("%s must be a finite number", name)
		}
		return parsed, true, nil
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return 0, true, fmt.Errorf("%s must be a finite number", name)
		}
		return typed, true, nil
	default:
		return 0, true, fmt.Errorf("%s must be a finite number", name)
	}
}

type RelayMetricsWriter struct {
	repository *Repository
	telemetry  *TelemetryStore
	now        func() time.Time
}

func NewRelayMetricsWriter(repository *Repository, telemetry *TelemetryStore) *RelayMetricsWriter {
	return &RelayMetricsWriter{repository: repository, telemetry: telemetry, now: time.Now}
}

func (m *RelayMetricsWriter) WritePrometheus(ctx context.Context, w io.Writer) error {
	queryCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	nodes, err := m.repository.List(queryCtx, ListFilter{Limit: 10000})
	if err != nil {
		return err
	}
	reports := m.telemetry.Snapshot()
	now := m.now().UTC()
	writeMetricType(w, "relay_node_info", "gauge")
	writeMetricType(w, "relay_node_state", "gauge")
	writeMetricType(w, "relay_node_active_allocations", "gauge")
	writeMetricType(w, "relay_node_max_allocations", "gauge")
	writeMetricType(w, "relay_node_current_egress_bps", "gauge")
	writeMetricType(w, "relay_node_current_ingress_bps", "gauge")
	writeMetricType(w, "relay_node_heartbeat_age_seconds", "gauge")
	writeMetricType(w, "relay_node_lease_remaining_seconds", "gauge")
	writeMetricType(w, "relay_node_control_connected", "gauge")
	writeMetricType(w, "relay_node_telemetry_report_age_seconds", "gauge")
	writeMetricType(w, "relay_node_load_state", "gauge")
	writeMetricType(w, "relay_node_load_ratio", "gauge")
	writeMetricType(w, "relay_node_goroutines", "gauge")
	writeMetricType(w, "relay_node_memory_bytes", "gauge")
	for _, name := range []string{
		"relay_node_packets_received_total", "relay_node_packets_forwarded_total",
		"relay_node_packets_dropped_total", "relay_node_bytes_received_total", "relay_node_bytes_forwarded_total",
		"relay_node_bind_success_total", "relay_node_bind_failed_total",
		"relay_node_bind_init_total", "relay_node_bind_challenge_total", "relay_node_cookie_invalid_total",
		"relay_node_token_invalid_total", "relay_node_token_replay_total",
		"relay_node_packet_auth_failed_total", "relay_node_packet_replay_dropped_total", "relay_node_packet_too_large_total",
		"relay_node_rate_limit_drops_total",
		"relay_node_control_reconnects_total",
	} {
		writeMetricType(w, name, "counter")
	}
	for _, node := range nodes {
		labels := nodeMetricLabels(node)
		_, _ = fmt.Fprintf(w, "relay_node_info%s 1\n", labels)
		_, _ = fmt.Fprintf(w, "relay_node_state%s 1\n", addMetricLabel(labels, "state", string(node.State)))
		_, _ = fmt.Fprintf(w, "relay_node_active_allocations%s %d\n", labels, node.ActiveAllocations)
		_, _ = fmt.Fprintf(w, "relay_node_max_allocations%s %d\n", labels, node.MaxAllocations)
		_, _ = fmt.Fprintf(w, "relay_node_current_egress_bps%s %d\n", labels, node.CurrentEgressBPS)
		_, _ = fmt.Fprintf(w, "relay_node_current_ingress_bps%s %d\n", labels, node.CurrentIngressBPS)
		loadState := node.LoadState
		if loadState == "" {
			loadState = LoadStateNormal
		}
		_, _ = fmt.Fprintf(w, "relay_node_load_state%s 1\n", addMetricLabel(labels, "state", string(loadState)))
		if node.LastHeartbeatAt != nil {
			_, _ = fmt.Fprintf(w, "relay_node_heartbeat_age_seconds%s %g\n", labels, nonnegativeSeconds(now.Sub(*node.LastHeartbeatAt)))
		}
		if node.LeaseExpiresAt != nil {
			_, _ = fmt.Fprintf(w, "relay_node_lease_remaining_seconds%s %g\n", labels, node.LeaseExpiresAt.Sub(now).Seconds())
		}
		report, exists := reports[node.ID]
		connected := 0
		if report.Connected {
			connected = 1
		}
		_, _ = fmt.Fprintf(w, "relay_node_control_connected%s %d\n", labels, connected)
		if !exists || report.ProcessID == "" {
			continue
		}
		_, _ = fmt.Fprintf(w, "relay_node_telemetry_report_age_seconds%s %g\n", labels, nonnegativeSeconds(now.Sub(report.ReceivedAt)))
		_, _ = fmt.Fprintf(w, "relay_node_load_ratio%s %g\n", labels, report.LoadRatio)
		_, _ = fmt.Fprintf(w, "relay_node_goroutines%s %d\n", labels, report.Goroutines)
		_, _ = fmt.Fprintf(w, "relay_node_memory_bytes%s %d\n", labels, report.MemoryBytes)
		writeNodeCounter(w, "relay_node_packets_received_total", labels, report.PacketsReceived)
		writeNodeCounter(w, "relay_node_bytes_received_total", labels, report.BytesReceived)
		writeNodeCounter(w, "relay_node_packets_forwarded_total", labels, report.PacketsForwarded)
		writeNodeCounter(w, "relay_node_packets_dropped_total", labels, report.PacketsDropped)
		writeNodeCounter(w, "relay_node_bytes_forwarded_total", labels, report.BytesForwarded)
		writeNodeCounter(w, "relay_node_bind_success_total", labels, report.BindSuccess)
		writeNodeCounter(w, "relay_node_bind_failed_total", labels, report.BindFailed)
		writeNodeCounter(w, "relay_node_bind_init_total", labels, report.BindInit)
		writeNodeCounter(w, "relay_node_bind_challenge_total", labels, report.BindChallenge)
		writeNodeCounter(w, "relay_node_cookie_invalid_total", labels, report.CookieInvalid)
		writeNodeCounter(w, "relay_node_token_invalid_total", labels, report.TokenInvalid)
		writeNodeCounter(w, "relay_node_token_replay_total", labels, report.TokenReplay)
		writeNodeCounter(w, "relay_node_packet_auth_failed_total", labels, report.PacketAuthFailed)
		writeNodeCounter(w, "relay_node_packet_replay_dropped_total", labels, report.PacketReplay)
		writeNodeCounter(w, "relay_node_packet_too_large_total", labels, report.PacketTooLarge)
		writeNodeCounter(w, "relay_node_rate_limit_drops_total", labels, report.RateLimitDrops)
		writeNodeCounter(w, "relay_node_control_reconnects_total", labels, report.ControlReconnects)
	}
	return nil
}

func nodeMetricLabels(node Node) string {
	values := []string{
		"node_id=\"" + metricLabel(node.ID) + "\"",
		"display_name=\"" + metricLabel(node.DisplayName) + "\"",
		"region=\"" + metricLabel(node.Region) + "\"",
		"zone=\"" + metricLabel(node.Zone) + "\"",
		"provider=\"" + metricLabel(node.Provider) + "\"",
	}
	return "{" + strings.Join(values, ",") + "}"
}

func addMetricLabel(labels, name, value string) string {
	return strings.TrimSuffix(labels, "}") + "," + name + "=\"" + metricLabel(value) + "\"}"
}

func metricLabel(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "\n", "\\n")
	return strings.ReplaceAll(value, "\"", "\\\"")
}

func writeMetricType(w io.Writer, name, metricType string) {
	_, _ = fmt.Fprintf(w, "# TYPE %s %s\n", name, metricType)
}

func writeNodeCounter(w io.Writer, name, labels string, value uint64) {
	_, _ = fmt.Fprintf(w, "%s%s %d\n", name, labels, value)
}

func nonnegativeSeconds(duration time.Duration) float64 {
	if duration < 0 {
		return 0
	}
	return duration.Seconds()
}
