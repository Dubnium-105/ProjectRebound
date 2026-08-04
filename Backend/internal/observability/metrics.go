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

type vntNodeMetric struct {
	NodeID, State, Region, VNTSVersion, WrapperVersion string
	HeartbeatAge, ReachabilityAge                      int64
	ActiveRooms, MaxRooms, ReferencedRooms             int64
	Compatible                                         bool
}

type vntNodeGroupKey struct {
	State, Region, VNTSVersion, WrapperVersion string
	Compatible                                 bool
}

type Metrics struct {
	pool               *pgxpool.Pool
	relayMetricsWriter interface {
		WritePrometheus(context.Context, io.Writer) error
	}
	redisProbe func(context.Context) error

	mu             sync.RWMutex
	httpRequests   map[httpMetricKey]uint64
	httpDurations  map[durationMetricKey]*durationMetric
	redisDuration  *durationMetric
	seenWebSockets map[string]struct{}
	vntVersions    map[string]struct{}
	vntWrappers    map[string]struct{}

	httpActiveRequests         atomic.Int64
	authBindTotal              atomic.Uint64
	authBindFailedTotal        atomic.Uint64
	authRefreshTotal           atomic.Uint64
	p2pRoomJoinTotal           atomic.Uint64
	refreshTokenReuseTotal     atomic.Uint64
	inviteCodeFailureTotal     atomic.Uint64
	p2pRoomJoinFailedTotal     atomic.Uint64
	relayAllocationFailedTotal atomic.Uint64
	websocketConnectionsActive atomic.Int64
	websocketReconnectTotal    atomic.Uint64
	vntRoomsEnabled            atomic.Bool
	authBindRateLimited        map[string]uint64
	vntRateLimited             map[string]uint64
}

func NewMetrics(pool *pgxpool.Pool) *Metrics {
	return &Metrics{
		pool: pool, httpRequests: make(map[httpMetricKey]uint64),
		httpDurations:       make(map[durationMetricKey]*durationMetric),
		redisDuration:       &durationMetric{Buckets: make([]uint64, len(durationBuckets))},
		authBindRateLimited: make(map[string]uint64),
		vntRateLimited:      make(map[string]uint64),
		seenWebSockets:      make(map[string]struct{}),
		vntVersions:         make(map[string]struct{}),
		vntWrappers:         make(map[string]struct{}),
	}
}

func (m *Metrics) SetRedisProbe(probe func(context.Context) error) { m.redisProbe = probe }

func (m *Metrics) HTTPStarted()  { m.httpActiveRequests.Add(1) }
func (m *Metrics) HTTPFinished() { m.httpActiveRequests.Add(-1) }

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
	if route == "/v1/auth/refresh" {
		m.authRefreshTotal.Add(1)
	}
	if route == "/v1/p2p-rooms/{room_id}/join" {
		m.p2pRoomJoinTotal.Add(1)
	}
	if route == "/v1/p2p-rooms/{room_id}/join" && status >= http.StatusBadRequest {
		m.p2pRoomJoinFailedTotal.Add(1)
	}
}

func (m *Metrics) WebSocketConnected(playerID string) func() {
	m.mu.Lock()
	if _, seen := m.seenWebSockets[playerID]; seen {
		m.websocketReconnectTotal.Add(1)
	} else {
		m.seenWebSockets[playerID] = struct{}{}
	}
	m.mu.Unlock()
	m.websocketConnectionsActive.Add(1)
	return func() { m.websocketConnectionsActive.Add(-1) }
}

func (m *Metrics) RefreshTokenReuse() { m.refreshTokenReuseTotal.Add(1) }

func (m *Metrics) BindRateLimited(dimension string) {
	m.mu.Lock()
	m.authBindRateLimited[dimension]++
	m.mu.Unlock()
}

func (m *Metrics) VNTRateLimited(operation string) {
	m.mu.Lock()
	m.vntRateLimited[operation]++
	m.mu.Unlock()
}

func (m *Metrics) InviteCodeFailure() { m.inviteCodeFailureTotal.Add(1) }

func (m *Metrics) RelayAllocationFailed() { m.relayAllocationFailedTotal.Add(1) }

func (m *Metrics) SetVNTPolicy(enabled bool, vntsVersions, wrapperVersions []string) {
	m.vntRoomsEnabled.Store(enabled)
	m.mu.Lock()
	m.vntVersions = stringSet(vntsVersions)
	m.vntWrappers = stringSet(wrapperVersions)
	m.mu.Unlock()
}

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
		m.writeRedisMetrics(r.Context(), w)
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
	writeGauge(w, "http_active_requests", m.httpActiveRequests.Load())
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
	writeCounter(w, "auth_refresh_total", m.authRefreshTotal.Load())
	writeCounter(w, "auth_refresh_reuse_total", m.refreshTokenReuseTotal.Load())
	writeCounter(w, "refresh_token_reuse_total", m.refreshTokenReuseTotal.Load())
	writeCounter(w, "auth_invite_code_failure_total", m.inviteCodeFailureTotal.Load())
	writeCounter(w, "p2p_room_join_failed_total", m.p2pRoomJoinFailedTotal.Load())
	writeCounter(w, "p2p_room_join_total", m.p2pRoomJoinTotal.Load())
	writeCounter(w, "relay_allocation_failed_total", m.relayAllocationFailedTotal.Load())
	writeGauge(w, "websocket_connections_active", m.websocketConnectionsActive.Load())
	writeCounter(w, "websocket_reconnect_total", m.websocketReconnectTotal.Load())
	writeEmptyHistogram(w, "background_job_duration_seconds")
	writeCounter(w, "background_job_failures_total", 0)
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
	operations := make([]string, 0, len(m.vntRateLimited))
	for operation := range m.vntRateLimited {
		operations = append(operations, operation)
	}
	sort.Strings(operations)
	_, _ = fmt.Fprintln(w, "# TYPE vnt_rate_limited_total counter")
	for _, operation := range operations {
		_, _ = fmt.Fprintf(w, "vnt_rate_limited_total{operation=%q} %d\n",
			label(operation), m.vntRateLimited[operation])
	}
	m.mu.RUnlock()
}

func (m *Metrics) writeRedisMetrics(ctx context.Context, w http.ResponseWriter) {
	available := int64(0)
	if m.redisProbe != nil {
		probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		started := time.Now()
		err := m.redisProbe(probeCtx)
		cancel()
		seconds := time.Since(started).Seconds()
		m.mu.Lock()
		m.redisDuration.Count++
		m.redisDuration.Sum += seconds
		for index, bucket := range durationBuckets {
			if seconds <= bucket {
				m.redisDuration.Buckets[index]++
			}
		}
		m.mu.Unlock()
		if err == nil {
			available = 1
		}
	}
	writeGauge(w, "redis_available", available)
	m.mu.RLock()
	_, _ = fmt.Fprintln(w, "# TYPE redis_operation_duration_seconds histogram")
	for index, bucket := range durationBuckets {
		_, _ = fmt.Fprintf(w, "redis_operation_duration_seconds_bucket{operation=%q,le=%q} %d\n", "ping",
			strconv.FormatFloat(bucket, 'f', -1, 64), m.redisDuration.Buckets[index])
	}
	_, _ = fmt.Fprintf(w, "redis_operation_duration_seconds_bucket{operation=%q,le=%q} %d\n", "ping", "+Inf", m.redisDuration.Count)
	_, _ = fmt.Fprintf(w, "redis_operation_duration_seconds_sum{operation=%q} %g\n", "ping", m.redisDuration.Sum)
	_, _ = fmt.Fprintf(w, "redis_operation_duration_seconds_count{operation=%q} %d\n", "ping", m.redisDuration.Count)
	m.mu.RUnlock()
}

func (m *Metrics) writeDatabaseGauges(ctx context.Context, w http.ResponseWriter) {
	activeSessions := int64(0)
	activeRooms := int64(0)
	activeAllocations := int64(0)
	inviteUses := int64(0)
	riskEvents := int64(0)
	relayMigrations := int64(0)
	relayMigrationFailures := int64(0)
	p2pHeartbeatLag := int64(0)
	gameServerHeartbeatLag := int64(0)
	gameServers := make(map[string]int64)
	relayNodes := make(map[string]int64)
	vntNodeStates := make(map[string]int64)
	vntSessionStates := make(map[string]int64)
	vntRoomGenerations := make(map[string]int64)
	vntMemberPaths := make(map[string]int64)
	p2pBattleLogMatches := make(map[string]int64)
	vntNodes := make([]vntNodeMetric, 0)
	vntActiveRooms := int64(0)
	vntCredentialsExpiring := int64(0)
	vntCredentialsExpired := int64(0)
	var p2pBattleLogReports int64
	var p2pBattleLogQuarantined int64
	scrapeError := int64(0)
	postgresAvailable := int64(0)
	if m.pool != nil {
		queryCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if err := m.pool.QueryRow(queryCtx, "SELECT COUNT(*) FROM auth_sessions WHERE revoked_at IS NULL AND expires_at > NOW()").Scan(&activeSessions); err != nil {
			scrapeError = 1
		}
		if err := m.pool.QueryRow(queryCtx, "SELECT COUNT(*) FROM p2p_rooms WHERE state <> 'CLOSED'").Scan(&activeRooms); err != nil {
			scrapeError = 1
		}
		if err := m.pool.QueryRow(queryCtx, "SELECT COUNT(*) FROM relay_allocations WHERE state IN ('ALLOCATED', 'BINDING', 'ACTIVE', 'MIGRATING')").Scan(&activeAllocations); err != nil {
			scrapeError = 1
		}
		queries := []struct {
			query string
			value *int64
		}{
			{"SELECT COUNT(*) FROM invite_code_uses", &inviteUses},
			{"SELECT COUNT(*) FROM auth_risk_events", &riskEvents},
			{"SELECT COUNT(*) FROM relay_migrations", &relayMigrations},
			{"SELECT COUNT(*) FROM relay_migrations WHERE state = 'FAILED'", &relayMigrationFailures},
			{"SELECT COALESCE(EXTRACT(EPOCH FROM NOW() - MIN(last_heartbeat_at)), 0)::bigint FROM p2p_rooms WHERE state <> 'CLOSED'", &p2pHeartbeatLag},
			{"SELECT COALESCE(EXTRACT(EPOCH FROM NOW() - MIN(last_heartbeat_at)), 0)::bigint FROM game_servers WHERE state <> 'OFFLINE'", &gameServerHeartbeatLag},
			{"SELECT COUNT(*) FROM p2p_battlelog_reports", &p2pBattleLogReports},
			{"SELECT COUNT(*) FROM p2p_battlelog_reports WHERE validation_status = 'QUARANTINED'", &p2pBattleLogQuarantined},
			{"SELECT COUNT(*) FROM p2p_rooms WHERE transport_kind = 'VNT' AND state <> 'CLOSED'", &vntActiveRooms},
			{`SELECT COUNT(*) FROM (
				SELECT node_id FROM vnt_node_credentials
				WHERE revoked_at IS NULL AND expires_at > NOW()
				GROUP BY node_id HAVING MAX(expires_at) <= NOW() + INTERVAL '7 days'
			) expiring`, &vntCredentialsExpiring},
			{`SELECT COUNT(*) FROM vnt_nodes node
				WHERE node.state NOT IN ('REVOKED','RETIRED')
				  AND NOT EXISTS (
					SELECT 1 FROM vnt_node_credentials credential
					WHERE credential.node_id = node.id AND credential.revoked_at IS NULL
					  AND credential.expires_at > NOW()
				  )`, &vntCredentialsExpired},
		}
		for _, item := range queries {
			if err := m.pool.QueryRow(queryCtx, item.query).Scan(item.value); err != nil {
				scrapeError = 1
			}
		}
		if err := collectStates(queryCtx, m.pool, "SELECT state, COUNT(*) FROM game_servers GROUP BY state", gameServers); err != nil {
			scrapeError = 1
		}
		if err := collectStates(queryCtx, m.pool, "SELECT state, COUNT(*) FROM relay_nodes GROUP BY state", relayNodes); err != nil {
			scrapeError = 1
		}
		if err := collectStates(queryCtx, m.pool, "SELECT state, COUNT(*) FROM vnt_nodes GROUP BY state", vntNodeStates); err != nil {
			scrapeError = 1
		}
		if err := collectStates(queryCtx, m.pool, "SELECT state, COUNT(*) FROM p2p_vnt_sessions GROUP BY state", vntSessionStates); err != nil {
			scrapeError = 1
		}
		if err := collectStates(queryCtx, m.pool, "SELECT generation::text, COUNT(*) FROM p2p_vnt_sessions GROUP BY generation", vntRoomGenerations); err != nil {
			scrapeError = 1
		}
		if err := collectStates(queryCtx, m.pool, "SELECT COALESCE(observed_path, 'UNKNOWN'), COUNT(*) FROM p2p_vnt_member_sessions GROUP BY COALESCE(observed_path, 'UNKNOWN')", vntMemberPaths); err != nil {
			scrapeError = 1
		}
		rows, err := m.pool.Query(queryCtx, `
			SELECT node.id, node.state, node.region, node.vnts_version, node.wrapper_version,
			       COALESCE(EXTRACT(EPOCH FROM NOW() - node.last_heartbeat_at)::bigint, -1),
			       COALESCE(EXTRACT(EPOCH FROM NOW() - node.last_reachable_at)::bigint, -1),
			       (
					SELECT COUNT(*) FROM p2p_vnt_sessions session
					JOIN p2p_rooms room ON room.id = session.room_id
					WHERE session.node_id = node.id AND session.state NOT IN ('FAILED','CLOSED')
					  AND room.expires_at > NOW()
			       ), node.max_rooms,
			       (SELECT COUNT(*) FROM p2p_vnt_sessions session WHERE session.node_id = node.id)
			FROM vnt_nodes node
			ORDER BY node.id
		`)
		if err != nil {
			scrapeError = 1
		} else {
			for rows.Next() {
				var item vntNodeMetric
				if err := rows.Scan(
					&item.NodeID, &item.State, &item.Region, &item.VNTSVersion, &item.WrapperVersion,
					&item.HeartbeatAge, &item.ReachabilityAge, &item.ActiveRooms, &item.MaxRooms,
					&item.ReferencedRooms,
				); err != nil {
					scrapeError = 1
					break
				}
				item.Compatible = m.vntCompatible(item.VNTSVersion, item.WrapperVersion)
				vntNodes = append(vntNodes, item)
			}
			if err := rows.Err(); err != nil {
				scrapeError = 1
			}
			rows.Close()
		}
		if err := collectStates(queryCtx, m.pool, "SELECT state, COUNT(*) FROM p2p_match_sessions GROUP BY state", p2pBattleLogMatches); err != nil {
			scrapeError = 1
		}
		if scrapeError == 0 {
			postgresAvailable = 1
		}
	}
	writeGauge(w, "active_sessions", activeSessions)
	writeGauge(w, "auth_active_sessions", activeSessions)
	writeGauge(w, "auth_invite_use_total", inviteUses)
	writeGauge(w, "auth_risk_events_total", riskEvents)
	writeGauge(w, "p2p_rooms_active", activeRooms)
	writeGauge(w, "p2p_room_heartbeat_lag_seconds", p2pHeartbeatLag)
	writeGauge(w, "p2p_battlelog_reports", p2pBattleLogReports)
	writeGauge(w, "p2p_battlelog_quarantined_reports", p2pBattleLogQuarantined)
	writeGauge(w, "game_server_heartbeat_lag_seconds", gameServerHeartbeatLag)
	writeGauge(w, "relay_allocations_active", activeAllocations)
	writeGauge(w, "relay_migrations_total", relayMigrations)
	writeGauge(w, "relay_migration_failed_total", relayMigrationFailures)
	writeStateGauges(w, "game_servers_by_state", []string{"STARTING", "READY", "RESERVED", "RUNNING", "DRAINING", "UNHEALTHY", "OFFLINE"}, gameServers)
	writeStateGauges(w, "relay_nodes_by_state", []string{"BOOTSTRAPPING", "CONNECTING", "READY", "DRAINING", "UNHEALTHY", "OFFLINE", "REVOKED"}, relayNodes)
	writeStateGauges(w, "vnt_nodes_by_state", []string{"REGISTERING", "ONLINE", "STALE", "OFFLINE", "DRAINING", "REVOKED", "RETIRED"}, vntNodeStates)
	writeStateGauges(w, "vnt_sessions_by_state", []string{"SELECTED", "HOST_CONNECTING", "HOST_READY", "READY", "ACTIVE", "REBINDING", "FAILED", "CLOSED"}, vntSessionStates)
	writeDynamicLabelGauges(w, "vnt_rooms_by_generation", "generation", vntRoomGenerations)
	writeDynamicLabelGauges(w, "vnt_member_sessions_by_path", "path", vntMemberPaths)
	writeGauge(w, "vnt_rooms_enabled", boolGauge(m.vntRoomsEnabled.Load()))
	writeGauge(w, "vnt_rooms_active", vntActiveRooms)
	writeGauge(w, "vnt_node_credentials_expiring_7d", vntCredentialsExpiring)
	writeGauge(w, "vnt_node_credentials_expired", vntCredentialsExpired)
	writeVNTNodeMetrics(w, vntNodes)
	writeStateGauges(w, "p2p_battlelog_matches_by_state", []string{"STARTING", "RUNNING", "COLLECTING", "PEER_CONFIRMED", "SELF_REPORTED", "DISPUTED", "INCOMPLETE", "ABORTED", "EXPIRED"}, p2pBattleLogMatches)
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
		writeGauge(w, "db_pool_open_connections", int64(stats.TotalConns()))
		writeGauge(w, "db_pool_in_use_connections", int64(stats.AcquiredConns()))
	}
	writeGauge(w, "control_plane_metrics_scrape_error", scrapeError)
	writeGauge(w, "postgres_available", postgresAvailable)
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

func writeEmptyHistogram(w http.ResponseWriter, name string) {
	_, _ = fmt.Fprintf(w, "# TYPE %s histogram\n%s_bucket{le=%q} 0\n%s_sum 0\n%s_count 0\n", name, name, "+Inf", name, name)
}

func writeStateGauges(w http.ResponseWriter, name string, states []string, values map[string]int64) {
	_, _ = fmt.Fprintf(w, "# TYPE %s gauge\n", name)
	for _, state := range states {
		_, _ = fmt.Fprintf(w, "%s{state=%q} %d\n", name, label(state), values[state])
	}
}

func writeDynamicLabelGauges(w http.ResponseWriter, name, labelName string, values map[string]int64) {
	_, _ = fmt.Fprintf(w, "# TYPE %s gauge\n", name)
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		_, _ = fmt.Fprintf(w, "%s{%s=%q} %d\n", name, labelName, label(key), values[key])
	}
}

func writeVNTNodeMetrics(w http.ResponseWriter, nodes []vntNodeMetric) {
	groups := make(map[vntNodeGroupKey]int64)
	compatibleOnline := int64(0)
	for _, node := range nodes {
		key := vntNodeGroupKey{
			State: node.State, Region: node.Region, VNTSVersion: node.VNTSVersion,
			WrapperVersion: node.WrapperVersion, Compatible: node.Compatible,
		}
		groups[key]++
		if node.State == "ONLINE" && node.Compatible && node.ActiveRooms < node.MaxRooms {
			compatibleOnline++
		}
	}
	_, _ = fmt.Fprintln(w, "# TYPE vnt_nodes gauge")
	groupKeys := make([]vntNodeGroupKey, 0, len(groups))
	for key := range groups {
		groupKeys = append(groupKeys, key)
	}
	sort.Slice(groupKeys, func(i, j int) bool { return fmt.Sprint(groupKeys[i]) < fmt.Sprint(groupKeys[j]) })
	for _, key := range groupKeys {
		_, _ = fmt.Fprintf(w, "vnt_nodes{state=%q,region=%q,vnts_version=%q,wrapper_version=%q,compatible=%q} %d\n",
			label(key.State), label(key.Region), label(key.VNTSVersion), label(key.WrapperVersion),
			strconv.FormatBool(key.Compatible), groups[key])
	}
	writeGauge(w, "vnt_nodes_compatible_online", compatibleOnline)
	for _, name := range []string{
		"vnt_node_heartbeat_age_seconds", "vnt_node_reachability_age_seconds",
		"vnt_node_active_rooms", "vnt_node_max_rooms", "vnt_node_referenced_rooms",
		"vnt_node_capacity_ratio", "vnt_node_version_compatible",
	} {
		_, _ = fmt.Fprintf(w, "# TYPE %s gauge\n", name)
	}
	for _, node := range nodes {
		labels := fmt.Sprintf("node_id=%q,state=%q,region=%q", label(node.NodeID), label(node.State), label(node.Region))
		_, _ = fmt.Fprintf(w, "vnt_node_heartbeat_age_seconds{%s} %d\n", labels, node.HeartbeatAge)
		_, _ = fmt.Fprintf(w, "vnt_node_reachability_age_seconds{%s} %d\n", labels, node.ReachabilityAge)
		_, _ = fmt.Fprintf(w, "vnt_node_active_rooms{%s} %d\n", labels, node.ActiveRooms)
		_, _ = fmt.Fprintf(w, "vnt_node_max_rooms{%s} %d\n", labels, node.MaxRooms)
		_, _ = fmt.Fprintf(w, "vnt_node_referenced_rooms{%s} %d\n", labels, node.ReferencedRooms)
		ratio := float64(node.ActiveRooms) / float64(max(node.MaxRooms, 1))
		_, _ = fmt.Fprintf(w, "vnt_node_capacity_ratio{%s} %g\n", labels, ratio)
		_, _ = fmt.Fprintf(w, "vnt_node_version_compatible{%s} %d\n", labels, boolGauge(node.Compatible))
	}
}

func (m *Metrics) vntCompatible(vntsVersion, wrapperVersion string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(m.vntVersions) == 0 || len(m.vntWrappers) == 0 {
		return false
	}
	_, vntsAllowed := m.vntVersions[vntsVersion]
	_, wrapperAllowed := m.vntWrappers[wrapperVersion]
	return vntsAllowed && wrapperAllowed
}

func stringSet(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result[value] = struct{}{}
		}
	}
	return result
}

func boolGauge(value bool) int64 {
	if value {
		return 1
	}
	return 0
}

func label(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "\n", "\\n")
	return strings.ReplaceAll(value, "\"", "\\\"")
}
