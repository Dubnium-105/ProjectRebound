package metaserver

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type MetaMetrics struct {
	connectionsActive atomic.Int64
	connectionsTotal  atomic.Uint64
	malformedTotal    atomic.Uint64
	ticketReplayTotal atomic.Uint64
	rateLimitedTotal  atomic.Uint64
	gateIssuedTotal   atomic.Uint64
	gateConsumedTotal atomic.Uint64
	loadoutConflicts  atomic.Uint64

	mu                sync.RWMutex
	rpcRequests       map[string]uint64
	rpcDurations      map[string]durationStat
	httpTotals        map[string]uint64
	httpDurations     map[string]durationStat
	matchOutcomes     map[string]uint64
	matchQueueLatency durationStat
	queueProbe        func(context.Context) (int64, error)
}

type durationStat struct {
	Count uint64
	Sum   float64
}

func NewMetaMetrics() *MetaMetrics {
	return &MetaMetrics{
		rpcRequests:   make(map[string]uint64),
		rpcDurations:  make(map[string]durationStat),
		httpTotals:    make(map[string]uint64),
		httpDurations: make(map[string]durationStat),
		matchOutcomes: make(map[string]uint64),
	}
}

func (m *MetaMetrics) ObserveHTTP(method, route string, status int, duration time.Duration) {
	m.mu.Lock()
	m.httpTotals[fmt.Sprintf("%s|%s|%d", method, route, status)]++
	key := method + "|" + route
	stat := m.httpDurations[key]
	stat.Count++
	stat.Sum += duration.Seconds()
	m.httpDurations[key] = stat
	m.mu.Unlock()
}

func (m *MetaMetrics) SetQueueProbe(probe func(context.Context) (int64, error)) {
	m.queueProbe = probe
}

func (m *MetaMetrics) RPC(path string, duration time.Duration) {
	m.mu.Lock()
	m.rpcRequests[path]++
	stat := m.rpcDurations[path]
	stat.Count++
	stat.Sum += duration.Seconds()
	m.rpcDurations[path] = stat
	m.mu.Unlock()
}

func (m *MetaMetrics) LoadoutConflict() { m.loadoutConflicts.Add(1) }

func (m *MetaMetrics) MatchOutcome(
	outcome string,
	count uint64,
	queueLatency time.Duration,
) {
	m.mu.Lock()
	m.matchOutcomes[outcome] += count
	if outcome == "matched" {
		m.matchQueueLatency.Count += count
		m.matchQueueLatency.Sum += queueLatency.Seconds()
	}
	m.mu.Unlock()
}

func (m *MetaMetrics) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = fmt.Fprintf(w, "# TYPE meta_tcp_connections_active gauge\nmeta_tcp_connections_active %d\n", m.connectionsActive.Load())
		_, _ = fmt.Fprintf(w, "# TYPE meta_tcp_connections_total counter\nmeta_tcp_connections_total %d\n", m.connectionsTotal.Load())
		_, _ = fmt.Fprintf(w, "# TYPE meta_malformed_frames_total counter\nmeta_malformed_frames_total %d\n", m.malformedTotal.Load())
		_, _ = fmt.Fprintf(w, "# TYPE meta_gate_ticket_replay_total counter\nmeta_gate_ticket_replay_total %d\n", m.ticketReplayTotal.Load())
		_, _ = fmt.Fprintf(w, "# TYPE meta_rate_limited_total counter\nmeta_rate_limited_total %d\n", m.rateLimitedTotal.Load())
		_, _ = fmt.Fprintf(w, "# TYPE meta_gate_ticket_issued_total counter\nmeta_gate_ticket_issued_total %d\n", m.gateIssuedTotal.Load())
		_, _ = fmt.Fprintf(w, "# TYPE meta_gate_ticket_consumed_total counter\nmeta_gate_ticket_consumed_total %d\n", m.gateConsumedTotal.Load())
		_, _ = fmt.Fprintf(w, "# TYPE meta_loadout_revision_conflicts_total counter\nmeta_loadout_revision_conflicts_total %d\n", m.loadoutConflicts.Load())
		if m.queueProbe != nil {
			if depth, err := m.queueProbe(r.Context()); err == nil {
				_, _ = fmt.Fprintf(w, "# TYPE meta_match_queue_depth gauge\nmeta_match_queue_depth %d\n", depth)
			}
		}
		m.mu.RLock()
		rpcKeys := sortedMetricKeys(m.rpcRequests)
		for _, path := range rpcKeys {
			_, _ = fmt.Fprintf(w, "meta_rpc_requests_total{rpc=%q} %d\n", path, m.rpcRequests[path])
			stat := m.rpcDurations[path]
			_, _ = fmt.Fprintf(w, "meta_rpc_duration_seconds_count{rpc=%q} %d\n", path, stat.Count)
			_, _ = fmt.Fprintf(w, "meta_rpc_duration_seconds_sum{rpc=%q} %g\n", path, stat.Sum)
		}
		httpKeys := sortedMetricKeys(m.httpTotals)
		for _, key := range httpKeys {
			parts := strings.Split(key, "|")
			_, _ = fmt.Fprintf(
				w, "meta_http_requests_total{method=%q,route=%q,status=%q} %d\n",
				parts[0], parts[1], parts[2], m.httpTotals[key],
			)
		}
		httpDurationKeys := sortedDurationMetricKeys(m.httpDurations)
		for _, key := range httpDurationKeys {
			parts := strings.SplitN(key, "|", 2)
			stat := m.httpDurations[key]
			_, _ = fmt.Fprintf(
				w, "meta_http_request_duration_seconds_count{method=%q,route=%q} %d\n",
				parts[0], parts[1], stat.Count,
			)
			_, _ = fmt.Fprintf(
				w, "meta_http_request_duration_seconds_sum{method=%q,route=%q} %g\n",
				parts[0], parts[1], stat.Sum,
			)
		}
		matchKeys := sortedMetricKeys(m.matchOutcomes)
		for _, outcome := range matchKeys {
			_, _ = fmt.Fprintf(
				w, "meta_matchmaking_outcomes_total{outcome=%q} %d\n",
				outcome, m.matchOutcomes[outcome],
			)
		}
		_, _ = fmt.Fprintf(
			w,
			"meta_matchmaking_queue_duration_seconds_count %d\nmeta_matchmaking_queue_duration_seconds_sum %g\n",
			m.matchQueueLatency.Count, m.matchQueueLatency.Sum,
		)
		m.mu.RUnlock()
	})
}

func sortedDurationMetricKeys(values map[string]durationStat) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedMetricKeys(values map[string]uint64) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
