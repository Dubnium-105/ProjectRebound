package relayregistry

import (
	"context"
	"log/slog"
	"time"
)

type MigrationService interface {
	MigrateFailedRelays(context.Context) (int, int, error)
}

type MigrationSweeper struct {
	service  MigrationService
	interval time.Duration
	logger   *slog.Logger
}

func NewMigrationSweeper(service MigrationService, interval time.Duration, logger *slog.Logger) *MigrationSweeper {
	return &MigrationSweeper{service: service, interval: interval, logger: logger}
}

func (s *MigrationSweeper) Run(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			planned, dispatched, err := s.service.MigrateFailedRelays(ctx)
			if err != nil {
				s.logger.ErrorContext(ctx, "relay migration sweep failed", "error", err)
				continue
			}
			if planned > 0 || dispatched > 0 {
				s.logger.InfoContext(ctx, "relay migrations processed", "planned", planned, "dispatched", dispatched)
			}
		}
	}
}
