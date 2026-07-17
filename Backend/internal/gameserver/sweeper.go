package gameserver

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
			updated, err := s.service.SweepStale(ctx)
			if err != nil {
				s.logger.ErrorContext(ctx, "game server expiry sweep failed", "error", err)
				continue
			}
			if updated > 0 {
				s.logger.InfoContext(ctx, "game server states expired", "updated_servers", updated)
			}
		}
	}
}
