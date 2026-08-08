package download

import (
	"context"
	"log/slog"
	"time"
)

type Worker struct {
	service  *Service
	interval time.Duration
	logger   *slog.Logger
}

func NewWorker(service *Service, interval time.Duration, logger *slog.Logger) *Worker {
	return &Worker{service: service, interval: interval, logger: logger}
}

func (w *Worker) Run(ctx context.Context) {
	if !w.service.Enabled() {
		return
	}
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	w.run(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.run(ctx)
		}
	}
}

func (w *Worker) run(ctx context.Context) {
	if err := w.service.VerifyPending(ctx, 2); err != nil && ctx.Err() == nil {
		w.logger.ErrorContext(ctx, "download object verification failed", "error", err)
	}
	if err := w.service.ExpireUploads(ctx, 20); err != nil && ctx.Err() == nil {
		w.logger.ErrorContext(ctx, "download upload expiry sweep failed", "error", err)
	}
}
