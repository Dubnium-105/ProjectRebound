package health

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/api"
)

type Checker interface {
	Check(context.Context) error
}

type Dependency struct {
	Name    string
	Checker Checker
}

type Handler struct {
	dependencies []Dependency
	timeout      time.Duration
	logger       *slog.Logger
}

func NewHandler(dependencies []Dependency, timeout time.Duration, logger *slog.Logger) *Handler {
	return &Handler{dependencies: dependencies, timeout: timeout, logger: logger}
}

func (h *Handler) Live(w http.ResponseWriter, r *http.Request) {
	api.WriteData(w, r, http.StatusOK, map[string]string{"status": "live"})
}

func (h *Handler) Ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()

	failed := make([]string, 0)
	for _, dependency := range h.dependencies {
		if err := dependency.Checker.Check(ctx); err != nil {
			h.logger.WarnContext(r.Context(), "readiness dependency failed", "dependency", dependency.Name, "error", err)
			failed = append(failed, dependency.Name)
		}
	}
	if len(failed) > 0 {
		api.WriteError(w, r, http.StatusServiceUnavailable, "NOT_READY", "Service is not ready.", map[string]any{
			"dependencies": failed,
		})
		return
	}
	api.WriteData(w, r, http.StatusOK, map[string]string{"status": "ready"})
}
