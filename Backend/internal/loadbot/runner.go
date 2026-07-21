package loadbot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"sync"
	"time"
)

type Report struct {
	Scenario              string    `json:"scenario"`
	Clients               int       `json:"clients"`
	StartedAt             time.Time `json:"started_at"`
	FinishedAt            time.Time `json:"finished_at"`
	SuccessfulRequests    uint64    `json:"successful_requests"`
	FailedRequests        uint64    `json:"failed_requests"`
	P50MS                 float64   `json:"p50_ms"`
	P95MS                 float64   `json:"p95_ms"`
	P99MS                 float64   `json:"p99_ms"`
	WebSocketReconnects   uint64    `json:"websocket_reconnects"`
	RoomsCreated          uint64    `json:"rooms_created"`
	RelayBindSuccess      uint64    `json:"relay_bind_success"`
	RelayMigrationSuccess uint64    `json:"relay_migration_success"`
	BytesSent             uint64    `json:"bytes_sent"`
	BytesReceived         uint64    `json:"bytes_received"`
	TokenRefreshFailures  uint64    `json:"token_refresh_failures"`
}

type Runner struct {
	cfg       Config
	client    *http.Client
	mu        sync.Mutex
	report    Report
	latencies []time.Duration
}

func (r Report) WritePrometheus(w io.Writer) {
	fmt.Fprintf(w, "loadbot_requests_total{result=%q} %d\n", "success", r.SuccessfulRequests)
	fmt.Fprintf(w, "loadbot_requests_total{result=%q} %d\n", "failed", r.FailedRequests)
	fmt.Fprintf(w, "loadbot_request_latency_milliseconds{quantile=%q} %g\n", "0.50", r.P50MS)
	fmt.Fprintf(w, "loadbot_request_latency_milliseconds{quantile=%q} %g\n", "0.95", r.P95MS)
	fmt.Fprintf(w, "loadbot_request_latency_milliseconds{quantile=%q} %g\n", "0.99", r.P99MS)
	fmt.Fprintf(w, "loadbot_bytes_total{direction=%q} %d\n", "sent", r.BytesSent)
	fmt.Fprintf(w, "loadbot_bytes_total{direction=%q} %d\n", "received", r.BytesReceived)
}

func New(cfg Config) *Runner {
	return &Runner{cfg: cfg, client: &http.Client{Timeout: 10 * time.Second}}
}

func (r *Runner) Run(ctx context.Context) Report {
	duration, _ := time.ParseDuration(r.cfg.Duration)
	ctx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()
	r.report = Report{Scenario: r.cfg.Scenario, Clients: r.cfg.Clients, StartedAt: time.Now().UTC()}
	var wg sync.WaitGroup
	for id := 0; id < r.cfg.Clients; id++ {
		wg.Add(1)
		go func(id int) { defer wg.Done(); r.runClient(ctx, id) }(id)
	}
	wg.Wait()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.report.FinishedAt = time.Now().UTC()
	r.finishPercentiles()
	return r.report
}

func (r *Runner) runClient(ctx context.Context, id int) {
	ticker := time.NewTicker(time.Duration(r.cfg.RequestIntervalMS) * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.step(ctx, id)
		}
	}
}

func (r *Runner) step(ctx context.Context, id int) {
	switch r.cfg.Scenario {
	case "auth", "auth-bind":
		steamID := fmt.Sprintf("7656119%010d", id)
		r.request(ctx, http.MethodPost, "/v1/auth/bind", map[string]any{"steam_id": steamID, "persona_name": fmt.Sprintf("loadbot-%d", id), "device_id": fmt.Sprintf("loadbot-device-%d", id), "invite_code": r.cfg.Auth.InviteCode})
	default:
		r.request(ctx, http.MethodGet, "/health/live", nil)
		r.request(ctx, http.MethodGet, "/v1/p2p-rooms?state=LOBBY&limit=50", nil)
		r.request(ctx, http.MethodGet, "/v1/client/config", nil)
	}
}

func (r *Runner) request(ctx context.Context, method, path string, body any) {
	var reader io.Reader
	sent := 0
	if body != nil {
		encoded, _ := json.Marshal(body)
		sent = len(encoded)
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, r.cfg.ControlPlaneURL+path, reader)
	if err != nil {
		return
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	started := time.Now()
	resp, err := r.client.Do(req)
	latency := time.Since(started)
	if err != nil && ctx.Err() != nil {
		return
	}
	received := 0
	success := false
	if err == nil {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		received = len(data)
		_ = resp.Body.Close()
		success = resp.StatusCode >= 200 && resp.StatusCode < 300
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.latencies = append(r.latencies, latency)
	r.report.BytesSent += uint64(sent)
	r.report.BytesReceived += uint64(received)
	if success {
		r.report.SuccessfulRequests++
	} else {
		r.report.FailedRequests++
	}
}

func (r *Runner) finishPercentiles() {
	if len(r.latencies) == 0 {
		return
	}
	sort.Slice(r.latencies, func(i, j int) bool { return r.latencies[i] < r.latencies[j] })
	r.report.P50MS = percentile(r.latencies, .50)
	r.report.P95MS = percentile(r.latencies, .95)
	r.report.P99MS = percentile(r.latencies, .99)
}
func percentile(values []time.Duration, p float64) float64 {
	index := int(float64(len(values)-1) * p)
	return float64(values[index].Microseconds()) / 1000
}
