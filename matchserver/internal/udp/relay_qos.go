package udp

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/projectrebound/matchserver/internal/config"
	"github.com/projectrebound/matchserver/internal/store"
)

type RelayQoSMetrics struct {
	store    *store.RelayStore
	cfg      *config.RelayConfig
}

func NewRelayQoSMetrics(relayStore *store.RelayStore, cfg *config.RelayConfig) *RelayQoSMetrics {
	return &RelayQoSMetrics{store: relayStore, cfg: cfg}
}

func (m *RelayQoSMetrics) Run(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.evaluate()
		}
	}
}

func (m *RelayQoSMetrics) evaluate() {
	if m.cfg.ForceCompression {
		return
	}
	if m.cfg.Compression == "none" {
		return
	}
	// Auto mode: compression decisions are made per-session
	// based on QoS stats tracked in the relay store
	slog.Debug("relay qos metrics evaluated")
}
