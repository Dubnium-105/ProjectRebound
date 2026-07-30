package loadbot

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var fixtureTicketSequence atomic.Uint64

type Report struct {
	Scenario               string            `json:"scenario"`
	Clients                int               `json:"clients"`
	StartedAt              time.Time         `json:"started_at"`
	FinishedAt             time.Time         `json:"finished_at"`
	SuccessfulRequests     uint64            `json:"successful_requests"`
	FailedRequests         uint64            `json:"failed_requests"`
	P50MS                  float64           `json:"p50_ms"`
	P95MS                  float64           `json:"p95_ms"`
	P99MS                  float64           `json:"p99_ms"`
	WebSocketReconnects    uint64            `json:"websocket_reconnects"`
	RoomsCreated           uint64            `json:"rooms_created"`
	RelayBindSuccess       uint64            `json:"relay_bind_success"`
	RelayBindFailures      uint64            `json:"relay_bind_failures"`
	RelayMigrationSuccess  uint64            `json:"relay_migration_success"`
	RelayMigrationAttempts uint64            `json:"relay_migration_attempts"`
	PacketsSent            uint64            `json:"packets_sent"`
	PacketsReceived        uint64            `json:"packets_received"`
	PacketsDropped         uint64            `json:"packets_dropped"`
	PacketLossPercent      float64           `json:"packet_loss_percent"`
	BytesSent              uint64            `json:"bytes_sent"`
	BytesReceived          uint64            `json:"bytes_received"`
	TokenRefreshFailures   uint64            `json:"token_refresh_failures"`
	RelayAllocations       uint64            `json:"relay_allocations"`
	RelayAllocationsClosed uint64            `json:"relay_allocations_closed"`
	SuccessRatePercent     float64           `json:"success_rate_percent"`
	DurationSeconds        float64           `json:"duration_seconds"`
	StartMemoryBytes       uint64            `json:"start_memory_bytes"`
	EndMemoryBytes         uint64            `json:"end_memory_bytes"`
	MemoryDeltaBytes       int64             `json:"memory_delta_bytes"`
	StartGoroutines        int               `json:"start_goroutines"`
	EndGoroutines          int               `json:"end_goroutines"`
	GoroutineDelta         int               `json:"goroutine_delta"`
	Failures               map[string]uint64 `json:"failures"`
}

type Runner struct {
	cfg        Config
	client     *http.Client
	mu         sync.Mutex
	report     Report
	latencies  []time.Duration
	migrations map[string]struct{}
	attempts   map[string]struct{}
}

func (r Report) WritePrometheus(w io.Writer) {
	fmt.Fprintf(w, "loadbot_requests_total{result=%q} %d\n", "success", r.SuccessfulRequests)
	fmt.Fprintf(w, "loadbot_requests_total{result=%q} %d\n", "failed", r.FailedRequests)
	fmt.Fprintf(w, "loadbot_request_latency_milliseconds{quantile=%q} %g\n", "0.50", r.P50MS)
	fmt.Fprintf(w, "loadbot_request_latency_milliseconds{quantile=%q} %g\n", "0.95", r.P95MS)
	fmt.Fprintf(w, "loadbot_request_latency_milliseconds{quantile=%q} %g\n", "0.99", r.P99MS)
	fmt.Fprintf(w, "loadbot_bytes_total{direction=%q} %d\n", "sent", r.BytesSent)
	fmt.Fprintf(w, "loadbot_bytes_total{direction=%q} %d\n", "received", r.BytesReceived)
	fmt.Fprintf(w, "loadbot_relay_allocations %d\n", r.RelayAllocations)
	fmt.Fprintf(w, "loadbot_relay_allocations_closed_total %d\n", r.RelayAllocationsClosed)
	fmt.Fprintf(w, "loadbot_relay_bind_total{result=%q} %d\n", "success", r.RelayBindSuccess)
	fmt.Fprintf(w, "loadbot_relay_bind_total{result=%q} %d\n", "failed", r.RelayBindFailures)
	fmt.Fprintf(w, "loadbot_relay_migrations_total{result=%q} %d\n", "attempted", r.RelayMigrationAttempts)
	fmt.Fprintf(w, "loadbot_relay_migrations_total{result=%q} %d\n", "success", r.RelayMigrationSuccess)
	fmt.Fprintf(w, "loadbot_relay_packets_total{result=%q} %d\n", "sent", r.PacketsSent)
	fmt.Fprintf(w, "loadbot_relay_packets_total{result=%q} %d\n", "received", r.PacketsReceived)
	fmt.Fprintf(w, "loadbot_relay_packets_total{result=%q} %d\n", "dropped", r.PacketsDropped)
	fmt.Fprintf(w, "loadbot_relay_packet_loss_percent %g\n", r.PacketLossPercent)
	fmt.Fprintf(w, "loadbot_websocket_reconnects_total %d\n", r.WebSocketReconnects)
	fmt.Fprintf(w, "loadbot_token_refresh_failures_total %d\n", r.TokenRefreshFailures)
	fmt.Fprintf(w, "loadbot_memory_delta_bytes %d\n", r.MemoryDeltaBytes)
	fmt.Fprintf(w, "loadbot_goroutine_delta %d\n", r.GoroutineDelta)
}

func New(cfg Config) *Runner {
	return &Runner{cfg: cfg, client: &http.Client{Timeout: 10 * time.Second}}
}

func (r *Runner) Run(ctx context.Context) Report {
	duration, _ := time.ParseDuration(r.cfg.Duration)
	ctx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()
	var startMemory runtime.MemStats
	runtime.ReadMemStats(&startMemory)
	r.report = Report{Scenario: r.cfg.Scenario, Clients: r.cfg.Clients, StartedAt: time.Now().UTC(),
		StartMemoryBytes: startMemory.Alloc, StartGoroutines: runtime.NumGoroutine(), Failures: make(map[string]uint64)}
	r.migrations = make(map[string]struct{})
	r.attempts = make(map[string]struct{})
	if r.cfg.Scenario == "full" || r.cfg.Scenario == "p2p" || r.cfg.Scenario == "relay" || r.cfg.Scenario == "soak" {
		r.runEndToEnd(ctx)
	} else {
		r.runRequestScenario(ctx)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.report.FinishedAt = time.Now().UTC()
	r.report.DurationSeconds = r.report.FinishedAt.Sub(r.report.StartedAt).Seconds()
	var endMemory runtime.MemStats
	runtime.ReadMemStats(&endMemory)
	r.report.EndMemoryBytes = endMemory.Alloc
	r.report.MemoryDeltaBytes = int64(endMemory.Alloc) - int64(r.report.StartMemoryBytes)
	r.report.EndGoroutines = runtime.NumGoroutine()
	r.report.GoroutineDelta = r.report.EndGoroutines - r.report.StartGoroutines
	total := r.report.SuccessfulRequests + r.report.FailedRequests
	if total > 0 {
		r.report.SuccessRatePercent = float64(r.report.SuccessfulRequests) * 100 / float64(total)
	}
	if r.report.PacketsSent > 0 {
		r.report.PacketLossPercent = float64(r.report.PacketsDropped) * 100 / float64(r.report.PacketsSent)
	}
	r.finishPercentiles()
	return r.report
}

func (r *Runner) runRequestScenario(ctx context.Context) {
	var wg sync.WaitGroup
	for id := 0; id < r.cfg.Clients; id++ {
		wg.Add(1)
		go func(id int) { defer wg.Done(); r.runClient(ctx, id) }(id)
	}
	wg.Wait()
}

func (r *Runner) runClient(ctx context.Context, id int) {
	if ctx.Err() != nil {
		return
	}
	r.step(ctx, id)
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
		body := map[string]any{"steam_id": steamID, "persona_name": fmt.Sprintf("loadbot-%d", id), "device_id": fmt.Sprintf("loadbot-device-%d", id), "invite_code": r.cfg.Auth.InviteCode}
		if r.cfg.Auth.UnsafeTestTicketFixture {
			body["encrypted_ticket"] = fixtureEncryptedTicket(steamID)
		}
		r.request(ctx, http.MethodPost, "/v1/auth/bind", body)
	default:
		r.request(ctx, http.MethodGet, "/health/live", nil)
		r.request(ctx, http.MethodGet, "/v1/p2p-rooms?state=LOBBY&limit=50", nil)
		r.request(ctx, http.MethodGet, "/v1/client/config", nil)
	}
}

func fixtureEncryptedTicket(steamID string) string {
	nonce := fixtureTicketSequence.Add(1)
	return hex.EncodeToString([]byte(fmt.Sprintf("%s|%d-%d", steamID, time.Now().UnixNano(), nonce)))
}

func (r *Runner) request(ctx context.Context, method, path string, body any) {
	_ = r.requestJSON(ctx, method, path, "", nil, body, nil)
}

func (r *Runner) requestJSON(ctx context.Context, method, path, accessToken string, headers map[string]string, body, result any) error {
	var reader io.Reader
	sent := 0
	if body != nil {
		encoded, _ := json.Marshal(body)
		sent = len(encoded)
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, r.cfg.ControlPlaneURL+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if accessToken != "" {
		req.Header.Set("Authorization", "Bearer "+accessToken)
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	started := time.Now()
	resp, err := r.client.Do(req)
	latency := time.Since(started)
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	received := 0
	success := false
	statusCode := 0
	if err == nil {
		statusCode = resp.StatusCode
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		received = len(data)
		_ = resp.Body.Close()
		success = resp.StatusCode >= 200 && resp.StatusCode < 300
		if success && result != nil {
			if decodeErr := json.Unmarshal(data, result); decodeErr != nil {
				success = false
				err = decodeErr
			}
		}
		if !success && err == nil {
			err = fmt.Errorf("HTTP %d for %s %s", resp.StatusCode, method, path)
		}
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
		r.report.Failures[requestFailureCategory(method, path, statusCode)]++
	}
	return err
}

func requestFailureCategory(method, path string, statusCode int) string {
	route := "other"
	switch {
	case path == "/v1/auth/bind":
		route = "auth_bind"
	case path == "/v1/auth/refresh":
		route = "auth_refresh"
	case path == "/v1/connections":
		route = "connections"
	case strings.HasPrefix(path, "/v1/connections/"):
		route = "connection"
	case path == "/v1/p2p-rooms":
		route = "rooms"
	case strings.HasSuffix(path, "/join"):
		route = "room_join"
	case strings.HasSuffix(path, "/heartbeat"):
		route = "room_heartbeat"
	case strings.HasPrefix(path, "/v1/p2p-rooms/"):
		route = "room"
	}
	result := "transport"
	if statusCode > 0 {
		result = fmt.Sprintf("status_%d", statusCode)
	}
	return fmt.Sprintf("http_%s_%s_%s", strings.ToLower(method), route, result)
}

func (r *Runner) recordFailure(category string) {
	r.mu.Lock()
	r.report.Failures[category]++
	r.mu.Unlock()
}

func (r *Runner) recordMigration(id string) {
	if id == "" {
		return
	}
	r.mu.Lock()
	if _, exists := r.migrations[id]; !exists {
		r.migrations[id] = struct{}{}
		r.report.RelayMigrationSuccess++
	}
	r.mu.Unlock()
}

func (r *Runner) recordMigrationAttempt(id string) {
	if id == "" {
		return
	}
	r.mu.Lock()
	if _, exists := r.attempts[id]; !exists {
		r.attempts[id] = struct{}{}
		r.report.RelayMigrationAttempts++
	}
	r.mu.Unlock()
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
