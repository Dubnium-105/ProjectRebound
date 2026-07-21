package relayruntime

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"
)

type Metrics struct {
	activeAllocations    atomic.Int64
	packetsReceived      atomic.Uint64
	packetsForwarded     atomic.Uint64
	packetsDropped       atomic.Uint64
	bytesForwarded       atomic.Uint64
	bindSuccess          atomic.Uint64
	bindFailed           atomic.Uint64
	tokenInvalid         atomic.Uint64
	tokenReplay          atomic.Uint64
	natRebind            atomic.Uint64
	authenticationFailed atomic.Uint64
	packetTooLarge       atomic.Uint64
	replayDropped        atomic.Uint64
	rateLimitDrops       atomic.Uint64
	controlConnected     atomic.Int64
	controlReconnects    atomic.Uint64
	loadState            atomic.Int32
	loadStateTransitions atomic.Uint64
}

type MetricsSnapshot struct {
	ActiveAllocations    int64
	PacketsReceived      uint64
	PacketsForwarded     uint64
	PacketsDropped       uint64
	BytesForwarded       uint64
	BindSuccess          uint64
	BindFailed           uint64
	TokenInvalid         uint64
	TokenReplay          uint64
	NATRebind            uint64
	AuthenticationFailed uint64
	PacketTooLarge       uint64
	ReplayDropped        uint64
	RateLimitDrops       uint64
	ControlReconnects    uint64
	LoadState            LoadState
	LoadStateTransitions uint64
}

func NewMetrics() *Metrics { return &Metrics{} }

func (m *Metrics) Snapshot() MetricsSnapshot {
	return MetricsSnapshot{
		ActiveAllocations: m.activeAllocations.Load(), PacketsReceived: m.packetsReceived.Load(),
		PacketsForwarded: m.packetsForwarded.Load(), PacketsDropped: m.packetsDropped.Load(),
		BytesForwarded: m.bytesForwarded.Load(), BindSuccess: m.bindSuccess.Load(),
		BindFailed: m.bindFailed.Load(), TokenInvalid: m.tokenInvalid.Load(),
		TokenReplay: m.tokenReplay.Load(), NATRebind: m.natRebind.Load(),
		AuthenticationFailed: m.authenticationFailed.Load(), PacketTooLarge: m.packetTooLarge.Load(),
		ReplayDropped:  m.replayDropped.Load(),
		RateLimitDrops: m.rateLimitDrops.Load(), ControlReconnects: m.controlReconnects.Load(),
		LoadState: loadStateFromMetric(m.loadState.Load()), LoadStateTransitions: m.loadStateTransitions.Load(),
	}
}

func (m *Metrics) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = fmt.Fprintf(w, `# TYPE relay_active_allocations gauge
relay_active_allocations %d
# TYPE relay_packets_received_total counter
relay_packets_received_total %d
# TYPE relay_packets_forwarded_total counter
relay_packets_forwarded_total %d
# TYPE relay_packets_dropped_total counter
relay_packets_dropped_total %d
# TYPE relay_bytes_forwarded_total counter
relay_bytes_forwarded_total %d
# TYPE relay_bind_success_total counter
relay_bind_success_total %d
# TYPE relay_bind_failed_total counter
relay_bind_failed_total %d
# TYPE relay_token_invalid_total counter
relay_token_invalid_total %d
# TYPE relay_token_replay_total counter
relay_token_replay_total %d
# TYPE relay_nat_rebind_total counter
relay_nat_rebind_total %d
# TYPE relay_packet_authentication_failed_total counter
relay_packet_authentication_failed_total %d
# TYPE relay_packet_too_large_total counter
relay_packet_too_large_total %d
# TYPE relay_packet_replay_dropped_total counter
relay_packet_replay_dropped_total %d
# TYPE relay_rate_limit_drops_total counter
relay_rate_limit_drops_total %d
# TYPE relay_control_connected gauge
relay_control_connected %d
# TYPE relay_control_reconnects_total counter
relay_control_reconnects_total %d
`, m.activeAllocations.Load(), m.packetsReceived.Load(), m.packetsForwarded.Load(),
			m.packetsDropped.Load(), m.bytesForwarded.Load(), m.bindSuccess.Load(),
			m.bindFailed.Load(), m.tokenInvalid.Load(), m.tokenReplay.Load(), m.natRebind.Load(),
			m.authenticationFailed.Load(), m.packetTooLarge.Load(), m.replayDropped.Load(), m.rateLimitDrops.Load(),
			m.controlConnected.Load(), m.controlReconnects.Load())
		state := loadStateFromMetric(m.loadState.Load())
		_, _ = fmt.Fprintln(w, "# TYPE relay_load_state gauge")
		for _, candidate := range []LoadState{LoadStateNormal, LoadStateDegraded, LoadStateRejectNew, LoadStateDraining} {
			value := 0
			if candidate == state {
				value = 1
			}
			_, _ = fmt.Fprintf(w, "relay_load_state{state=%q} %d\n", candidate, value)
		}
		_, _ = fmt.Fprintf(w, "# TYPE relay_load_state_transitions_total counter\nrelay_load_state_transitions_total %d\n", m.loadStateTransitions.Load())
	})
}

func (m *Metrics) setLoadState(state LoadState) {
	value := int32(0)
	switch state {
	case LoadStateDegraded:
		value = 1
	case LoadStateRejectNew:
		value = 2
	case LoadStateDraining:
		value = 3
	}
	if m.loadState.Swap(value) != value {
		m.loadStateTransitions.Add(1)
	}
}

func loadStateFromMetric(value int32) LoadState {
	switch value {
	case 1:
		return LoadStateDegraded
	case 2:
		return LoadStateRejectNew
	case 3:
		return LoadStateDraining
	default:
		return LoadStateNormal
	}
}

func (m *Metrics) RunServer(ctx context.Context, address string) error {
	server := &http.Server{Addr: address, Handler: m.Handler(), ReadHeaderTimeout: 3 * time.Second}
	result := make(chan error, 1)
	go func() { result <- server.ListenAndServe() }()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	case err := <-result:
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
}
