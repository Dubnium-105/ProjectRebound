package matchlobby

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

type memberConnectionHTTPStub struct {
	HTTPService
	evidence MemberConnectionEvidence
	err      error
}

type joinGrantHTTPStub struct {
	HTTPService
	result GrantResult
	err    error
}

func (s joinGrantHTTPStub) JoinGrantWithIdempotency(context.Context, Actor, string, string) (GrantResult, error) {
	return s.result, s.err
}

func (s memberConnectionHTTPStub) CurrentMemberConnection(context.Context, Actor, string) (MemberConnectionEvidence, error) {
	return s.evidence, s.err
}

func memberConnectionRequest(t *testing.T) *http.Request {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/v1/match-attempts/attempt-1/members/me/connection", nil)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("attempt_id", "attempt-1")
	return request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, routeContext))
}

func joinGrantRequest(t *testing.T) *http.Request {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/v1/match-attempts/attempt-1/join-grant", nil)
	request.Header.Set("Idempotency-Key", "member-scope-proof")
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("attempt_id", "attempt-1")
	return request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, routeContext))
}

func TestJoinGrantHTTPReturnsSignedScopeAlongsideGrant(t *testing.T) {
	handler := NewHTTPHandler(joinGrantHTTPStub{result: GrantResult{
		AttemptID: "attempt-1", AuthoritySessionID: "session-1", WorldInstanceID: "world-1",
		RosterRevision: 7, RouteGeneration: 2, PlayerID: "player-1", GrantJTI: "mj-proof-1",
		EndpointHost: "127.0.0.1", EndpointPort: 47777, Grant: "opaque-grant", ConnectionGeneration: 3,
	}}, nil)
	response := httptest.NewRecorder()
	handler.JoinGrant(response, joinGrantRequest(t))

	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Pragma") != "no-cache" {
		t.Fatalf("cache headers = %v", response.Header())
	}
	body := response.Body.String()
	for _, expected := range []string{
		`"authority_session_id":"session-1"`, `"world_instance_id":"world-1"`,
		`"roster_revision":7`, `"route_generation":2`, `"player_id":"player-1"`,
		`"grant_jti":"mj-proof-1"`, `"join_grant":"opaque-grant"`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("response omitted %q: %s", expected, body)
		}
	}
}

func TestMemberConnectionHTTPReturnsNoStoreValidatedEvidence(t *testing.T) {
	handler := NewHTTPHandler(memberConnectionHTTPStub{evidence: MemberConnectionEvidence{
		AttemptID: "attempt-1", AuthoritySessionID: "session-1", WorldInstanceID: "world-1",
		RosterRevision: 7, RouteGeneration: 2, PlayerID: "player-1", Role: "MEMBER",
		GrantJTI: "grant-1", ConnectionGeneration: 3, NativeConnectionNonce: "nonce-1234567890",
		ConnectionState: "CONNECTED",
	}}, nil)
	response := httptest.NewRecorder()
	handler.MemberConnection(response, memberConnectionRequest(t))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Pragma") != "no-cache" {
		t.Fatalf("cache headers = %v", response.Header())
	}
	body := response.Body.String()
	for _, expected := range []string{"attempt-1", "session-1", "world-1", "player-1", "grant-1", "CONNECTED"} {
		if !strings.Contains(body, expected) {
			t.Fatalf("response omitted %q: %s", expected, body)
		}
	}
}

func TestMemberConnectionHTTPDoesNotExposeMissingEvidence(t *testing.T) {
	handler := NewHTTPHandler(memberConnectionHTTPStub{err: conflict("MATCH_CONNECTION_NOT_CONNECTED", "not connected", nil)}, nil)
	response := httptest.NewRecorder()
	handler.MemberConnection(response, memberConnectionRequest(t))

	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Pragma") != "no-cache" {
		t.Fatalf("cache headers = %v", response.Header())
	}
	if strings.Contains(response.Body.String(), "grant-1") {
		t.Fatalf("conflict response leaked connection evidence: %s", response.Body.String())
	}
}
