package loadbot

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequestFailureCategoryNormalizesIdentifiers(t *testing.T) {
	tests := map[string]string{
		requestFailureCategory("POST", "/v1/auth/bind", 429):                       "http_post_auth_bind_status_429",
		requestFailureCategory("GET", "/v1/connections/018f4f57-1234", 500):        "http_get_connection_status_500",
		requestFailureCategory("POST", "/v1/p2p-rooms/018f4f57-1234/heartbeat", 0): "http_post_room_heartbeat_transport",
		requestFailureCategory("DELETE", "/v1/p2p-rooms/018f4f57-1234", 409):       "http_delete_room_status_409",
		requestFailureCategory("POST", "/v1/p2p-rooms/018f4f57-1234/join", 422):    "http_post_room_join_status_422",
	}
	for got, want := range tests {
		if got != want {
			t.Errorf("requestFailureCategory() = %q, want %q", got, want)
		}
	}
}

func TestRunnerCollectsConcurrentRequestResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200); _, _ = w.Write([]byte("ok")) }))
	defer server.Close()
	// Leave enough time for the first requests when the full suite is running under
	// the race detector on a busy CI runner.
	cfg := Config{Scenario: "basic", ControlPlaneURL: server.URL, Clients: 3, Duration: "1s", RequestIntervalMS: 25}
	report := New(cfg).Run(t.Context())
	if report.SuccessfulRequests == 0 || report.FailedRequests != 0 || report.P95MS < 0 {
		t.Fatalf("report=%#v", report)
	}
}
