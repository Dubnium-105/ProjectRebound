package vnt

import (
	"context"
	"log/slog"
	"time"
)

type Sweeper struct {
	service  *Service
	interval time.Duration
	logger   *slog.Logger
}

func NewSweeper(service *Service, interval time.Duration, logger *slog.Logger) *Sweeper {
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
			count, err := s.service.Sweep(ctx)
			if err != nil {
				s.logger.ErrorContext(ctx, "sweep VNT node leases", "error", err)
				continue
			}
			if count > 0 {
				s.logger.InfoContext(ctx, "updated stale VNT node leases", "count", count)
			}
		}
	}
}
