package relayruntime

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"
)

type Metrics struct {
	activeAllocations atomic.Int64
	packetsReceived   atomic.Uint64
	packetsForwarded  atomic.Uint64
	packetsDropped    atomic.Uint64
	bytesForwarded    atomic.Uint64
	bindSuccess       atomic.Uint64
	bindFailed        atomic.Uint64
	tokenInvalid      atomic.Uint64
	rateLimitDrops    atomic.Uint64
	controlConnected  atomic.Int64
	controlReconnects atomic.Uint64
}

type MetricsSnapshot struct {
	ActiveAllocations int64
	PacketsReceived   uint64
	PacketsForwarded  uint64
	PacketsDropped    uint64
	BytesForwarded    uint64
	BindSuccess       uint64
	BindFailed        uint64
	TokenInvalid      uint64
	RateLimitDrops    uint64
	ControlReconnects uint64
}

func NewMetrics() *Metrics { return &Metrics{} }

func (m *Metrics) Snapshot() MetricsSnapshot {
	return MetricsSnapshot{
		ActiveAllocations: m.activeAllocations.Load(), PacketsReceived: m.packetsReceived.Load(),
		PacketsForwarded: m.packetsForwarded.Load(), PacketsDropped: m.packetsDropped.Load(),
		BytesForwarded: m.bytesForwarded.Load(), BindSuccess: m.bindSuccess.Load(),
		BindFailed: m.bindFailed.Load(), TokenInvalid: m.tokenInvalid.Load(),
		RateLimitDrops: m.rateLimitDrops.Load(), ControlReconnects: m.controlReconnects.Load(),
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
# TYPE relay_rate_limit_drops_total counter
relay_rate_limit_drops_total %d
# TYPE relay_control_connected gauge
relay_control_connected %d
# TYPE relay_control_reconnects_total counter
relay_control_reconnects_total %d
`, m.activeAllocations.Load(), m.packetsReceived.Load(), m.packetsForwarded.Load(),
			m.packetsDropped.Load(), m.bytesForwarded.Load(), m.bindSuccess.Load(),
			m.bindFailed.Load(), m.tokenInvalid.Load(), m.rateLimitDrops.Load(),
			m.controlConnected.Load(), m.controlReconnects.Load())
	})
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
