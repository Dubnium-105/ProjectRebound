package loadbot

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRunnerCollectsConcurrentRequestResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200); _, _ = w.Write([]byte("ok")) }))
	defer server.Close()
	cfg := Config{Scenario: "basic", ControlPlaneURL: server.URL, Clients: 3, Duration: "40ms", RequestIntervalMS: 5}
	report := New(cfg).Run(t.Context())
	if report.SuccessfulRequests == 0 || report.FailedRequests != 0 || report.P95MS < 0 {
		t.Fatalf("report=%#v", report)
	}
}
