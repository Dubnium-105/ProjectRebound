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
	metrics.RefreshTokenReuse()
	metrics.BindRateLimited("device_id")
	metrics.InviteCodeFailure()
	metrics.RelayAllocationFailed()
	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/internal/metrics", nil))
	for _, series := range []string{
		"http_requests_total", "http_request_duration_seconds_bucket", "auth_bind_total 1",
		"auth_bind_failed_total 1", "active_sessions 0", "refresh_token_reuse_total 1",
		"p2p_rooms_active 0", "p2p_room_join_failed_total 1", "game_servers_by_state",
		"relay_nodes_by_state", "relay_allocations_active 0", "relay_allocation_failed_total 1",
		"go_goroutines", "go_memory_alloc_bytes",
		`auth_bind_rate_limited_total{dimension="device_id"} 1`,
		"auth_invite_code_failure_total 1",
	} {
		if !strings.Contains(recorder.Body.String(), series) {
			t.Fatalf("metrics output does not contain %q:\n%s", series, recorder.Body.String())
		}
	}
}
