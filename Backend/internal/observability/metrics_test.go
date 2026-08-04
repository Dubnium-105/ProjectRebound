package observability

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMetricsExposeRequiredControlPlaneSeries(t *testing.T) {
	metrics := NewMetrics(nil)
	metrics.ObserveHTTP(http.MethodPost, "/v1/auth/bind", http.StatusBadRequest, 25*time.Millisecond)
	metrics.ObserveHTTP(http.MethodPost, "/v1/p2p-rooms/{room_id}/join", http.StatusConflict, 10*time.Millisecond)
	metrics.ObserveHTTP(http.MethodPost, "/v1/auth/refresh", http.StatusOK, 10*time.Millisecond)
	metrics.HTTPStarted()
	metrics.HTTPFinished()
	disconnect := metrics.WebSocketConnected("player-1")
	disconnect()
	disconnect = metrics.WebSocketConnected("player-1")
	disconnect()
	metrics.RefreshTokenReuse()
	metrics.BindRateLimited("device_id")
	metrics.VNTRateLimited("heartbeat")
	metrics.InviteCodeFailure()
	metrics.RelayAllocationFailed()
	metrics.SetVNTPolicy(true, []string{"1.0.0"}, []string{"0.1.0"})
	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/internal/metrics", nil))
	for _, series := range []string{
		"http_requests_total", "http_request_duration_seconds_bucket", "http_active_requests 0", "auth_bind_total 1",
		"auth_bind_failed_total 1", "active_sessions 0", "refresh_token_reuse_total 1",
		"auth_active_sessions 0", "auth_refresh_total 1", "auth_refresh_reuse_total 1",
		"p2p_rooms_active 0", "p2p_room_join_failed_total 1", "game_servers_by_state",
		"p2p_room_join_total 1", "p2p_room_heartbeat_lag_seconds 0", "game_server_heartbeat_lag_seconds 0",
		"p2p_battlelog_reports 0", "p2p_battlelog_quarantined_reports 0", "p2p_battlelog_matches_by_state",
		"relay_nodes_by_state", "relay_allocations_active 0", "relay_allocation_failed_total 1",
		"relay_migrations_total 0", "relay_migration_failed_total 0",
		"vnt_rooms_enabled 1", "vnt_rooms_active 0", "vnt_nodes_by_state", "vnt_sessions_by_state",
		"vnt_rooms_by_generation", "vnt_member_sessions_by_path", "vnt_nodes_compatible_online 0",
		"vnt_node_credentials_expiring_7d 0", "vnt_node_credentials_expired 0",
		"websocket_connections_active 0", "websocket_reconnect_total 1",
		"redis_operation_duration_seconds", "background_job_duration_seconds", "background_job_failures_total 0",
		"go_goroutines", "go_memory_alloc_bytes",
		`auth_bind_rate_limited_total{dimension="device_id"} 1`,
		`vnt_rate_limited_total{operation="heartbeat"} 1`,
		"auth_invite_code_failure_total 1",
		"auth_invite_use_total 0", "auth_risk_events_total 0", "postgres_available 0", "redis_available 0",
	} {
		if !strings.Contains(recorder.Body.String(), series) {
			t.Fatalf("metrics output does not contain %q:\n%s", series, recorder.Body.String())
		}
	}
}
