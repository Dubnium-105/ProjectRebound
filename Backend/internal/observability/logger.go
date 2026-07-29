package observability

import (
	"io"
	"log/slog"
	"strings"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/config"
)

func NewLogger(output io.Writer, cfg config.LogConfig) *slog.Logger {
	level := slog.LevelInfo
	switch strings.ToLower(cfg.Level) {
	case "debug":
		level = slog.LevelDebug
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	return slog.New(slog.NewJSONHandler(output, &slog.HandlerOptions{
		Level:     level,
		AddSource: cfg.AddSource,
	}))
}
