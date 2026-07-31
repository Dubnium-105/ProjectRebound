package metaserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMetaMetricsExposeOperationalSignals(t *testing.T) {
	metrics := NewMetaMetrics()
	metrics.connectionsActive.Store(2)
	metrics.connectionsTotal.Store(3)
	metrics.malformedTotal.Store(4)
	metrics.ticketReplayTotal.Store(5)
	metrics.gateIssuedTotal.Store(6)
	metrics.gateConsumedTotal.Store(7)
	metrics.LoadoutConflict()
	metrics.ObserveHTTP(http.MethodGet, "/v1/meta/regions", http.StatusOK, 25*time.Millisecond)
	metrics.RPC("GetProfile", 10*time.Millisecond)
	metrics.MatchOutcome("matched", 1, 2*time.Second)
	metrics.MatchOutcome("connection_timeout", 1, 0)
	metrics.BattleLogOutcome("PVE", "ACCEPTED", false)
	metrics.BattleLogOutcome("PVE", "ACCEPTED", true)
	metrics.SetQueueProbe(func(context.Context) (int64, error) { return 8, nil })

	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/internal/metrics", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		"meta_tcp_connections_active 2",
		"meta_tcp_connections_total 3",
		"meta_malformed_frames_total 4",
		"meta_gate_ticket_replay_total 5",
		"meta_gate_ticket_issued_total 6",
		"meta_gate_ticket_consumed_total 7",
		"meta_loadout_revision_conflicts_total 1",
		"meta_match_queue_depth 8",
		`meta_http_requests_total{method="GET",route="/v1/meta/regions",status="200"} 1`,
		`meta_rpc_requests_total{rpc="GetProfile"} 1`,
		`meta_matchmaking_outcomes_total{outcome="matched"} 1`,
		`meta_matchmaking_outcomes_total{outcome="connection_timeout"} 1`,
		`meta_battlelog_reports_total{match_type="PVE",status="ACCEPTED",duplicate="false"} 1`,
		`meta_battlelog_reports_total{match_type="PVE",status="ACCEPTED",duplicate="true"} 1`,
		"meta_matchmaking_queue_duration_seconds_count 1",
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("metrics output does not contain %q:\n%s", expected, body)
		}
	}
}
