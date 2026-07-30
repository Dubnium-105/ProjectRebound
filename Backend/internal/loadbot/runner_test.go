package loadbot

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
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

func TestVirtualClientSerializesTokenRotationWithAuthenticatedRequests(t *testing.T) {
	client := &virtualClient{
		accessToken:  "access-v1",
		refreshToken: "refresh-v1",
	}
	requestStarted := make(chan string, 1)
	releaseRequest := make(chan struct{})
	requestDone := make(chan error, 1)
	go func() {
		requestDone <- client.withAccessToken(func(accessToken string) error {
			requestStarted <- accessToken
			<-releaseRequest
			return nil
		})
	}()
	if accessToken := <-requestStarted; accessToken != "access-v1" {
		t.Fatalf("authenticated request used %q", accessToken)
	}

	rotationStarted := make(chan struct{})
	rotationInvoked := make(chan string, 1)
	rotationDone := make(chan error, 1)
	go func() {
		close(rotationStarted)
		rotationDone <- client.rotateTokens(func(refreshToken string) (string, string, error) {
			rotationInvoked <- refreshToken
			return "access-v2", "refresh-v2", nil
		})
	}()
	<-rotationStarted
	select {
	case refreshToken := <-rotationInvoked:
		t.Fatalf("rotation used %q before the authenticated request completed", refreshToken)
	case <-time.After(50 * time.Millisecond):
	}

	close(releaseRequest)
	if err := <-requestDone; err != nil {
		t.Fatal(err)
	}
	if err := <-rotationDone; err != nil {
		t.Fatal(err)
	}
	if refreshToken := <-rotationInvoked; refreshToken != "refresh-v1" {
		t.Fatalf("rotation used %q", refreshToken)
	}
	if err := client.withAccessToken(func(accessToken string) error {
		if accessToken != "access-v2" {
			t.Fatalf("next authenticated request used %q", accessToken)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
