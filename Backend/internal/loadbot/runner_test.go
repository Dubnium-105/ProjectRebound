package loadbot

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRequestFailureCategoryNormalizesIdentifiers(t *testing.T) {
	tests := map[string]string{
		requestFailureCategory("POST", "/v1/auth/bind", 429):                                   "http_post_auth_bind_status_429",
		requestFailureCategory("GET", "/v1/connections/018f4f57-1234", 500):                    "http_get_connection_status_500",
		requestFailureCategory("POST", "/v1/match-lobbies", 409):                               "http_post_match_lobbies_status_409",
		requestFailureCategory("POST", "/v1/match-lobbies/018f4f57-1234/join", 422):            "http_post_match_lobby_join_status_422",
		requestFailureCategory("PUT", "/v1/match-lobbies/018f4f57-1234/members/me/ready", 0):   "http_put_match_lobby_ready_transport",
		requestFailureCategory("POST", "/v1/match-lobbies/018f4f57-1234/presence", 503):        "http_post_match_lobby_presence_status_503",
		requestFailureCategory("POST", "/v1/match-lobbies/018f4f57-1234/leave", 409):           "http_post_match_lobby_leave_status_409",
		requestFailureCategory("GET", "/v1/match-attempts/018f4f57-1234/host/allocation", 500): "http_get_match_attempt_status_500",
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

func TestVerifyAllocationRequiresSignedFrozenScope(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"EdDSA","typ":"match-allocation+jwt","kid":"loadbot-test"}`))
	claims := base64.RawURLEncoding.EncodeToString([]byte(`{"iss":"game-control-plane","aud":"project-rebound-match-authority","kid":"loadbot-test","attempt_id":"mat_1","lobby_id":"lby_1","hosting_kind":"P2P","authority_id":"player_1","authority_session_id":"mas_1","roster_revision":2,"route_generation":1,"nbf":` + fmt.Sprint(now-1) + `,"exp":` + fmt.Sprint(now+60) + `}`))
	unsigned := header + "." + claims
	signature := ed25519.Sign(privateKey, []byte(unsigned))
	allocation := allocationResult{
		AttemptID: "mat_1", Allocation: unsigned + "." + base64.RawURLEncoding.EncodeToString(signature),
		AdmissionKeyID: "loadbot-test", AdmissionPublicKey: base64.RawStdEncoding.EncodeToString(publicKey),
	}
	match := matchFixture{lobbyID: "lby_1", attemptID: "mat_1", host: &virtualClient{playerID: "player_1"}, rosterRevision: 2, routeGeneration: 1}
	verified, err := verifyAllocation(allocation, match)
	if err != nil || verified.AuthoritySessionID != "mas_1" {
		t.Fatalf("verifyAllocation() = %#v, %v", verified, err)
	}
	allocation.Allocation = unsigned + "." + base64.RawURLEncoding.EncodeToString([]byte("bad"))
	if _, err := verifyAllocation(allocation, match); err == nil {
		t.Fatal("tampered allocation signature was accepted")
	}
}

func TestNativeProcessNotStartedCleanupBodyRequiresExactScope(t *testing.T) {
	match := matchFixture{
		attemptID: "mat_1", authoritySession: "mas_1", rosterRevision: 7, routeGeneration: 2,
	}
	body, err := nativeProcessNotStartedCleanupBody(match)
	if err != nil {
		t.Fatal(err)
	}
	if body["world_instance_id"] != "" || body["evidence_kind"] != "native_process_not_started" ||
		body["roster_revision"] != int64(7) || body["route_generation"] != 2 || body["owned_process_id"] != uint32(0) ||
		body["process_start_fingerprint"] != "" {
		t.Fatalf("unexpected pre-native cleanup body: %#v", body)
	}
	match.authoritySession = ""
	if _, err := nativeProcessNotStartedCleanupBody(match); err == nil {
		t.Fatal("cleanup body accepted an incomplete authority scope")
	}
}
