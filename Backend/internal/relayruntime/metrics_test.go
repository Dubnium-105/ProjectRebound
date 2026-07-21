package relayruntime

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMetricsExposeRequiredRelaySeries(t *testing.T) {
	metrics := NewMetrics()
	metrics.packetsReceived.Add(2)
	metrics.controlConnected.Store(1)
	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/metrics", nil))
	body := recorder.Body.String()
	for _, name := range []string{
		"relay_active_allocations", "relay_packets_received_total", "relay_packets_forwarded_total",
		"relay_packets_dropped_total", "relay_bytes_forwarded_total", "relay_bind_success_total",
		"relay_bind_failed_total", "relay_token_invalid_total", "relay_rate_limit_drops_total",
		"relay_token_replay_total", "relay_nat_rebind_total",
		"relay_packet_authentication_failed_total", "relay_packet_too_large_total", "relay_packet_replay_dropped_total",
		"relay_control_connected", "relay_control_reconnects_total",
		"relay_load_state", "relay_load_state_transitions_total",
	} {
		if !strings.Contains(body, name) {
			t.Errorf("metric %s is missing", name)
		}
	}
}
