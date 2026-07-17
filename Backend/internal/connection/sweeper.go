package connection

import (
	"context"
	"log/slog"
	"time"
)

type ExpiryService interface {
	SweepExpired(context.Context) (int, error)
}

type Sweeper struct {
	service  ExpiryService
	interval time.Duration
	logger   *slog.Logger
}

func NewSweeper(service ExpiryService, interval time.Duration, logger *slog.Logger) *Sweeper {
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
			count, err := s.service.SweepExpired(ctx)
			if err != nil {
				s.logger.ErrorContext(ctx, "connection expiry sweep failed", "error", err)
				continue
			}
			if count > 0 {
				s.logger.InfoContext(ctx, "connection sessions expired", "expired_connections", count)
			}
		}
	}
}
