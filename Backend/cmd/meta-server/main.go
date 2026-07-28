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

	"github.com/projectrebound/matchserver/internal/config"
	"github.com/projectrebound/matchserver/internal/metaserver"
	"github.com/projectrebound/matchserver/internal/observability"
)

func main() {
	configPath := flag.String("config", "config.control-plane.yaml", "path to the MetaServer YAML configuration")
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
		bootstrapLogger.Error("load MetaServer configuration", "error", err)
		os.Exit(1)
	}
	logger := observability.NewLogger(os.Stderr, cfg.Logging)
	slog.SetDefault(logger)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	server, err := metaserver.New(ctx, cfg, logger)
	if err != nil {
		logger.Error("initialize MetaServer", "error", err)
		os.Exit(1)
	}
	defer server.Close()
	if err := server.Run(ctx); err != nil {
		logger.Error("MetaServer stopped with error", "error", err)
		os.Exit(1)
	}
	logger.Info("MetaServer stopped")
}
