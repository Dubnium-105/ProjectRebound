package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/loadbot"
)

func main() {
	configPath := flag.String("config", "tests/load/scenario-basic.yaml", "scenario YAML")
	reportPath := flag.String("report", "load-report.json", "JSON report path")
	prometheusPath := flag.String("prometheus-report", "load-report.prom", "Prometheus text report path")
	flag.Parse()
	cfg, err := loadbot.LoadConfig(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	report := loadbot.New(cfg).Run(ctx)
	data, _ := json.MarshalIndent(report, "", "  ")
	if err := os.WriteFile(*reportPath, data, 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	metrics, err := os.Create(*prometheusPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	report.WritePrometheus(metrics)
	_ = metrics.Close()
	fmt.Printf("scenario=%s clients=%d duration=%.1fs success=%d failed=%d success_rate=%.3f%% p50=%.1fms p95=%.1fms p99=%.1fms rooms=%d relay_allocations=%d relay_closed=%d relay_bind_success=%d relay_bind_failed=%d migrations=%d/%d packets=%d/%d loss=%.3f%% reconnects=%d refresh_failed=%d memory_delta=%d goroutine_delta=%d\n",
		report.Scenario, report.Clients, report.DurationSeconds, report.SuccessfulRequests, report.FailedRequests,
		report.SuccessRatePercent, report.P50MS, report.P95MS, report.P99MS, report.RoomsCreated,
		report.RelayAllocations, report.RelayAllocationsClosed, report.RelayBindSuccess, report.RelayBindFailures,
		report.RelayMigrationSuccess, report.RelayMigrationAttempts, report.PacketsReceived, report.PacketsSent,
		report.PacketLossPercent, report.WebSocketReconnects, report.TokenRefreshFailures, report.MemoryDeltaBytes, report.GoroutineDelta)
	if report.FailedRequests > 0 || len(report.Failures) > 0 {
		os.Exit(1)
	}
}
