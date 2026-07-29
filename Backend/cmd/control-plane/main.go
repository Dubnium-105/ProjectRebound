package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/config"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/controlplane"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/observability"
)

func main() {
	configPath := flag.String("config", "config.control-plane.yaml", "path to the control-plane YAML configuration")
	healthcheckURL := flag.String("healthcheck", "", "perform one HTTP health check and exit")
	flag.Parse()
	if *healthcheckURL != "" {
		client := &http.Client{Timeout: 2 * time.Second}
		response, err := client.Get(*healthcheckURL)
		if err != nil || response.StatusCode < 200 || response.StatusCode >= 300 {
			os.Exit(1)
		}
		_ = response.Body.Close()
		return
	}

	bootstrapLogger := observability.NewLogger(os.Stderr, config.Defaults.Logging)
	cfg, err := config.Load(*configPath)
	if err != nil {
		bootstrapLogger.Error("load configuration", "error", err)
		os.Exit(1)
	}
	logger := observability.NewLogger(os.Stderr, cfg.Logging)
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	server, err := controlplane.New(ctx, cfg, logger)
	if err != nil {
		logger.Error("initialize control-plane", "error", err)
		os.Exit(1)
	}
	defer server.Close()

	if err := server.Run(ctx); err != nil {
		logger.Error("control-plane stopped with error", "error", err)
		os.Exit(1)
	}
	logger.Info("control-plane stopped")
}
