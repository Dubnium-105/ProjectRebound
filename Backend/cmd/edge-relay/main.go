package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/projectrebound/matchserver/internal/relayruntime"
)

func main() {
	configPath := flag.String("config", "config.edge-relay.yaml", "path to the edge relay YAML configuration")
	flag.Parse()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(*configPath, logger); err != nil {
		logger.Error("edge relay stopped", "error", err)
		os.Exit(1)
	}
}

func run(configPath string, logger *slog.Logger) error {
	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithCancel(signalCtx)
	defer cancel()
	cfg, err := relayruntime.LoadConfig(configPath)
	if err != nil {
		return err
	}
	identity, err := relayruntime.LoadOrEnroll(ctx, cfg)
	if err != nil {
		return err
	}
	verifier, err := relayruntime.NewTokenVerifier(identity.Keyset)
	if err != nil {
		return err
	}
	metrics := relayruntime.NewMetrics()
	runtime, err := relayruntime.NewRuntime(identity.NodeID, cfg, verifier, metrics)
	if err != nil {
		return err
	}
	control := relayruntime.NewControlClient(cfg, identity, runtime, logger)
	errorsCh := make(chan error, 2)
	go func() { errorsCh <- runtime.RunUDP(ctx) }()
	go func() { errorsCh <- metrics.RunServer(ctx, cfg.MetricsAddr) }()
	go runtime.RunSweeper(ctx)
	go control.Run(ctx)
	logger.Info("edge relay started", "node_id", identity.NodeID, "udp_address", cfg.ListenAddr, "metrics_address", cfg.MetricsAddr)
	select {
	case <-ctx.Done():
		return nil
	case <-runtime.ShutdownRequested():
		return nil
	case err := <-errorsCh:
		if err == nil {
			return nil
		}
		return fmt.Errorf("edge relay listener: %w", err)
	}
}
