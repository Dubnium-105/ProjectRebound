package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"github.com/projectrebound/matchserver/internal/loadbot"
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
	fmt.Printf("scenario=%s clients=%d success=%d failed=%d p50=%.1fms p95=%.1fms p99=%.1fms\n", report.Scenario, report.Clients, report.SuccessfulRequests, report.FailedRequests, report.P50MS, report.P95MS, report.P99MS)
	if report.FailedRequests > 0 {
		os.Exit(1)
	}
}
