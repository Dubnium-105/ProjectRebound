package p2pbattlelog

import (
	"context"
	"log/slog"
	"time"
)

type Finalizer struct {
	service  *Service
	interval time.Duration
	logger   *slog.Logger
}

func NewFinalizer(service *Service, interval time.Duration, logger *slog.Logger) *Finalizer {
	return &Finalizer{service: service, interval: interval, logger: logger}
}

func (f *Finalizer) Run(ctx context.Context) {
	ticker := time.NewTicker(f.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			count, err := f.service.FinalizeDue(ctx)
			if err != nil {
				f.logger.ErrorContext(ctx, "P2P BattleLog finalization failed", "error", err)
				continue
			}
			if count > 0 {
				f.logger.InfoContext(ctx, "P2P BattleLog matches finalized", "match_count", count)
			}
		}
	}
}
