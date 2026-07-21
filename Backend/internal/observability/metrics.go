package observability

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

var durationBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.2, 0.5, 1, 2.5, 5}

type httpMetricKey struct {
	Method string
	Route  string
	Status int
}

type durationMetricKey struct {
	Method string
	Route  string
}

type durationMetric struct {
	Count   uint64
	Sum     float64
	Buckets []uint64
}

type Metrics struct {
	pool               *pgxpool.Pool
	relayMetricsWriter interface {
		WritePrometheus(context.Context, io.Writer) error
	}

	mu            sync.RWMutex
	httpRequests  map[httpMetricKey]uint64
	httpDurations map[durationMetricKey]*durationMetric

	authBindTotal              atomic.Uint64
	authBindFailedTotal        atomic.Uint64
	refreshTokenReuseTotal     atomic.Uint64
	inviteCodeFailureTotal     atomic.Uint64
	p2pRoomJoinFailedTotal     atomic.Uint64
	relayAllocationFailedTotal atomic.Uint64
	authBindRateLimited        map[string]uint64
}

func NewMetrics(pool *pgxpool.Pool) *Metrics {
	return &Metrics{
		pool: pool, httpRequests: make(map[httpMetricKey]uint64),
		httpDurations:       make(map[durationMetricKey]*durationMetric),
		authBindRateLimited: make(map[string]uint64),
	}
}

func (m *Metrics) ObserveHTTP(method, route string, status int, duration time.Duration) {
	if route == "" {
		route = "unmatched"
	}
	m.mu.Lock()
	m.httpRequests[httpMetricKey{Method: method, Route: route, Status: status}]++
	durationKey := durationMetricKey{Method: method, Route: route}
	metric := m.httpDurations[durationKey]
	if metric == nil {
		metric = &durationMetric{Buckets: make([]uint64, len(durationBuckets))}
		m.httpDurations[durationKey] = metric
	}
	seconds := duration.Seconds()
	metric.Count++
	metric.Sum += seconds
	for index, bucket := range durationBuckets {
		if seconds <= bucket {
			metric.Buckets[index]++
		}
	}
	m.mu.Unlock()

	if route == "/v1/auth/bind" {
		m.authBindTotal.Add(1)
		if status >= http.StatusBadRequest {
			m.authBindFailedTotal.Add(1)
		}
	}
	if route == "/v1/p2p-rooms/{room_id}/join" && status >= http.StatusBadRequest {
		m.p2pRoomJoinFailedTotal.Add(1)
	}
}

func (m *Metrics) RefreshTokenReuse() { m.refreshTokenReuseTotal.Add(1) }

func (m *Metrics) BindRateLimited(dimension string) {
	m.mu.Lock()
	m.authBindRateLimited[dimension]++
	m.mu.Unlock()
}

func (m *Metrics) InviteCodeFailure() { m.inviteCodeFailureTotal.Add(1) }

func (m *Metrics) RelayAllocationFailed() { m.relayAllocationFailedTotal.Add(1) }

func (m *Metrics) SetRelayMetricsWriter(writer interface {
	WritePrometheus(context.Context, io.Writer) error
}) {
	m.relayMetricsWriter = writer
}

func (m *Metrics) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		m.writeHTTPMetrics(w)
		m.writeApplicationCounters(w)
		m.writeDatabaseGauges(r.Context(), w)
		scrapeError := int64(0)
		if m.relayMetricsWriter != nil {
			if err := m.relayMetricsWriter.WritePrometheus(r.Context(), w); err != nil {
				scrapeError = 1
			}
		}
		writeGauge(w, "relay_node_metrics_scrape_error", scrapeError)
	})
}

func (m *Metrics) writeHTTPMetrics(w http.ResponseWriter) {
	m.mu.RLock()
	requestKeys := make([]httpMetricKey, 0, len(m.httpRequests))
	for key := range m.httpRequests {
		requestKeys = append(requestKeys, key)
	}
	durationKeys := make([]durationMetricKey, 0, len(m.httpDurations))
	for key := range m.httpDurations {
		durationKeys = append(durationKeys, key)
	}
	sort.Slice(requestKeys, func(i, j int) bool {
		return fmt.Sprint(requestKeys[i]) < fmt.Sprint(requestKeys[j])
	})
	sort.Slice(durationKeys, func(i, j int) bool {
		return fmt.Sprint(durationKeys[i]) < fmt.Sprint(durationKeys[j])
	})
	_, _ = fmt.Fprintln(w, "# TYPE http_requests_total counter")
	for _, key := range requestKeys {
		_, _ = fmt.Fprintf(w, "http_requests_total{method=%q,route=%q,status=%q} %d\n",
			label(key.Method), label(key.Route), strconv.Itoa(key.Status), m.httpRequests[key])
	}
	_, _ = fmt.Fprintln(w, "# TYPE http_request_duration_seconds histogram")
	for _, key := range durationKeys {
		metric := m.httpDurations[key]
		for index, bucket := range durationBuckets {
			_, _ = fmt.Fprintf(w, "http_request_duration_seconds_bucket{method=%q,route=%q,le=%q} %d\n",
				label(key.Method), label(key.Route), strconv.FormatFloat(bucket, 'f', -1, 64), metric.Buckets[index])
		}
		_, _ = fmt.Fprintf(w, "http_request_duration_seconds_bucket{method=%q,route=%q,le=%q} %d\n",
			label(key.Method), label(key.Route), "+Inf", metric.Count)
		_, _ = fmt.Fprintf(w, "http_request_duration_seconds_sum{method=%q,route=%q} %g\n", label(key.Method), label(key.Route), metric.Sum)
		_, _ = fmt.Fprintf(w, "http_request_duration_seconds_count{method=%q,route=%q} %d\n", label(key.Method), label(key.Route), metric.Count)
	}
	m.mu.RUnlock()
}

func (m *Metrics) writeApplicationCounters(w http.ResponseWriter) {
	writeCounter(w, "auth_bind_total", m.authBindTotal.Load())
	writeCounter(w, "auth_bind_failed_total", m.authBindFailedTotal.Load())
	writeCounter(w, "refresh_token_reuse_total", m.refreshTokenReuseTotal.Load())
	writeCounter(w, "auth_invite_code_failure_total", m.inviteCodeFailureTotal.Load())
	writeCounter(w, "p2p_room_join_failed_total", m.p2pRoomJoinFailedTotal.Load())
	writeCounter(w, "relay_allocation_failed_total", m.relayAllocationFailedTotal.Load())
	m.mu.RLock()
	dimensions := make([]string, 0, len(m.authBindRateLimited))
	for dimension := range m.authBindRateLimited {
		dimensions = append(dimensions, dimension)
	}
	sort.Strings(dimensions)
	_, _ = fmt.Fprintln(w, "# TYPE auth_bind_rate_limited_total counter")
	for _, dimension := range dimensions {
		_, _ = fmt.Fprintf(w, "auth_bind_rate_limited_total{dimension=%q} %d\n",
			label(dimension), m.authBindRateLimited[dimension])
	}
	m.mu.RUnlock()
}

func (m *Metrics) writeDatabaseGauges(ctx context.Context, w http.ResponseWriter) {
	activeSessions := int64(0)
	activeRooms := int64(0)
	activeAllocations := int64(0)
	gameServers := make(map[string]int64)
	relayNodes := make(map[string]int64)
	scrapeError := int64(0)
	if m.pool != nil {
		queryCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if err := m.pool.QueryRow(queryCtx, "SELECT COUNT(*) FROM auth_sessions WHERE revoked_at IS NULL AND expires_at > NOW()").Scan(&activeSessions); err != nil {
			scrapeError = 1
		}
		if err := m.pool.QueryRow(queryCtx, "SELECT COUNT(*) FROM p2p_rooms WHERE state <> 'CLOSED'").Scan(&activeRooms); err != nil {
			scrapeError = 1
		}
		if err := m.pool.QueryRow(queryCtx, "SELECT COUNT(*) FROM relay_allocations WHERE state IN ('ALLOCATED', 'BINDING', 'ACTIVE')").Scan(&activeAllocations); err != nil {
			scrapeError = 1
		}
		if err := collectStates(queryCtx, m.pool, "SELECT state, COUNT(*) FROM game_servers GROUP BY state", gameServers); err != nil {
			scrapeError = 1
		}
		if err := collectStates(queryCtx, m.pool, "SELECT state, COUNT(*) FROM relay_nodes GROUP BY state", relayNodes); err != nil {
			scrapeError = 1
		}
	}
	writeGauge(w, "active_sessions", activeSessions)
	writeGauge(w, "p2p_rooms_active", activeRooms)
	writeGauge(w, "relay_allocations_active", activeAllocations)
	writeStateGauges(w, "game_servers_by_state", []string{"STARTING", "READY", "RESERVED", "RUNNING", "DRAINING", "UNHEALTHY", "OFFLINE"}, gameServers)
	writeStateGauges(w, "relay_nodes_by_state", []string{"BOOTSTRAPPING", "CONNECTING", "READY", "DRAINING", "UNHEALTHY", "OFFLINE", "REVOKED"}, relayNodes)
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	writeGauge(w, "go_goroutines", int64(runtime.NumGoroutine()))
	writeGauge(w, "go_memory_alloc_bytes", int64(memory.Alloc))
	if m.pool != nil {
		stats := m.pool.Stat()
		_, _ = fmt.Fprintln(w, "# TYPE database_pool_connections gauge")
		_, _ = fmt.Fprintf(w, "database_pool_connections{state=%q} %d\n", "acquired", stats.AcquiredConns())
		_, _ = fmt.Fprintf(w, "database_pool_connections{state=%q} %d\n", "idle", stats.IdleConns())
		_, _ = fmt.Fprintf(w, "database_pool_connections{state=%q} %d\n", "total", stats.TotalConns())
	}
	writeGauge(w, "control_plane_metrics_scrape_error", scrapeError)
}

func collectStates(ctx context.Context, pool *pgxpool.Pool, query string, values map[string]int64) error {
	rows, err := pool.Query(ctx, query)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var state string
		var count int64
		if err := rows.Scan(&state, &count); err != nil {
			return err
		}
		values[state] = count
	}
	return rows.Err()
}

func writeCounter(w http.ResponseWriter, name string, value uint64) {
	_, _ = fmt.Fprintf(w, "# TYPE %s counter\n%s %d\n", name, name, value)
}

func writeGauge(w http.ResponseWriter, name string, value int64) {
	_, _ = fmt.Fprintf(w, "# TYPE %s gauge\n%s %d\n", name, name, value)
}

func writeStateGauges(w http.ResponseWriter, name string, states []string, values map[string]int64) {
	_, _ = fmt.Fprintf(w, "# TYPE %s gauge\n", name)
	for _, state := range states {
		_, _ = fmt.Fprintf(w, "%s{state=%q} %d\n", name, label(state), values[state])
	}
}

func label(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "\n", "\\n")
	return strings.ReplaceAll(value, "\"", "\\\"")
}
