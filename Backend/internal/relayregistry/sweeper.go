package relayregistry

import (
	"context"
	"log/slog"
	"time"
)

type NodeExpiryService interface {
	SweepNodes(context.Context) (int64, error)
}

type Sweeper struct {
	service  NodeExpiryService
	interval time.Duration
	logger   *slog.Logger
}

func NewSweeper(service NodeExpiryService, interval time.Duration, logger *slog.Logger) *Sweeper {
	return &Sweeper{service: service, interval: interval, logger: logger}
}

func (s *Sweeper) Run(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			count, err := s.service.SweepNodes(ctx)
			if err != nil {
				s.logger.ErrorContext(ctx, "relay node expiry sweep failed", "error", err)
				continue
			}
			if count > 0 {
				s.logger.InfoContext(ctx, "relay node states expired", "updated_nodes", count)
			}
		}
	}
}
