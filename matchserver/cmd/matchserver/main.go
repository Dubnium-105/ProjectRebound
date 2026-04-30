package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/projectrebound/matchserver/internal/config"
	"github.com/projectrebound/matchserver/internal/server"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to config file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	var level slog.Level
	switch cfg.Logging.Level {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))

	srv, err := server.New(cfg)
	if err != nil {
		slog.Error("failed to create server", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv.Start(ctx)

	slog.Info("matchserver started",
		"http_addr", cfg.HTTPAddr,
		"udp_rendezvous_port", cfg.UDPRendezvousPort,
		"udp_relay_port", cfg.UDPRelayPort,
		"udp_qos_port", cfg.UDPQoSPort,
	)

	<-ctx.Done()
	slog.Info("shutting down...")

	if err := srv.Shutdown(); err != nil {
		slog.Error("shutdown error", "error", err)
	}
	slog.Info("shutdown complete")
}
