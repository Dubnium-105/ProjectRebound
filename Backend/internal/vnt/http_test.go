package vnt

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/auth"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/player"
)

type stubHTTPService struct {
	filter          ListFilter
	result          ListResult
	ownedFilter     OwnedListFilter
	ownedResult     OwnedListResult
	rotationResult  CredentialRotationResult
	rotationErr     error
	retireToken     string
	ownerActor      Actor
	recoveredNodeID string
	recoveryCode    string
}

func (s *stubHTTPService) CreateEnrollment(context.Context, Actor, string) (EnrollmentResult, error) {
	return EnrollmentResult{}, nil
}
func (s *stubHTTPService) Register(context.Context, string, RegisterInput) (RegisterResult, error) {
	return RegisterResult{}, nil
}
func (s *stubHTTPService) Recover(_ context.Context, nodeID, code string, _ RegisterInput) (RegisterResult, error) {
	s.recoveredNodeID = nodeID
	s.recoveryCode = code
	return RegisterResult{NodeID: nodeID, NodeToken: "vnn_recovered", State: StateRegistering}, nil
}
func (s *stubHTTPService) List(_ context.Context, filter ListFilter) (ListResult, error) {
	s.filter = filter
	return s.result, nil
}
func (s *stubHTTPService) ListOwned(_ context.Context, _ Actor, filter OwnedListFilter) (OwnedListResult, error) {
	s.ownedFilter = filter
	return s.ownedResult, nil
}
func (s *stubHTTPService) Heartbeat(context.Context, string, string, HeartbeatInput) error {
	return nil
}

func (s *stubHTTPService) Retire(_ context.Context, _ string, token string) (string, error) {
	s.retireToken = token
	return StateRetired, nil
}
func (s *stubHTTPService) RetireOwned(_ context.Context, actor Actor, _ string) (string, error) {
	s.ownerActor = actor
	return StateRetired, nil
}
func (s *stubHTTPService) RotateCredential(context.Context, string, string) (CredentialRotationResult, error) {
	return s.rotationResult, s.rotationErr
}

type stubAccessAuthenticator struct {
	principal auth.Principal
	err       error
}

func (s stubAccessAuthenticator) AuthenticateAccess(context.Context, string) (auth.Principal, error) {
	return s.principal, s.err
}

func TestRetireAcceptsIntegrityTrustedOwnerAccess(t *testing.T) {
	service := &stubHTTPService{}
	handler := NewHTTPHandler(service, slog.New(slog.NewTextHandler(io.Discard, nil)))
	handler.SetAccessAuthenticator(stubAccessAuthenticator{principal: auth.Principal{
		Player:    player.Player{ID: "player_owner", AccountStatus: player.AccountStatusActive},
		AuthLevel: player.AuthLevelTrusted, SteamVerified: true,
	}})
	request := httptest.NewRequest("DELETE", "/v1/vnt/nodes/vnt_one", nil)
	request.Header.Set("Authorization", "Bearer player.jwt.token")
	recorder := httptest.NewRecorder()
	handler.Retire(recorder, request)
	if recorder.Code != 200 || service.ownerActor.PlayerID != "player_owner" || !service.ownerActor.IntegrityTrusted {
		t.Fatalf("response = %d actor = %#v", recorder.Code, service.ownerActor)
	}
}

func TestRetireKeepsNodeCredentialPathSeparate(t *testing.T) {
	service := &stubHTTPService{}
	handler := NewHTTPHandler(service, slog.New(slog.NewTextHandler(io.Discard, nil)))
	request := httptest.NewRequest("DELETE", "/v1/vnt/nodes/vnt_one", nil)
	request.Header.Set("Authorization", "Bearer vnn_node_secret")
	recorder := httptest.NewRecorder()
	handler.Retire(recorder, request)
	if recorder.Code != 200 || service.retireToken != "vnn_node_secret" || service.ownerActor.PlayerID != "" {
		t.Fatalf("response = %d token = %q actor = %#v", recorder.Code, service.retireToken, service.ownerActor)
	}
}

func TestRecoverReturnsReplacementOnce(t *testing.T) {
	service := &stubHTTPService{}
	handler := NewHTTPHandler(service, slog.New(slog.NewTextHandler(io.Discard, nil)))
	request := httptest.NewRequest("POST", "/v1/vnt/nodes/vnt_one/recover", strings.NewReader(`{
		"advertised_host":"203.0.113.10","port":29872,"region":"hk","location":"Hong Kong",
		"vnts_version":"1","wrapper_version":"1","server_key_fingerprint":"sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		"supported_transports":["tcp","udp"],"max_rooms":10
	}`))
	request.Header.Set("Authorization", "VNTEnrollment vne_recovery")
	recorder := httptest.NewRecorder()
	handler.Recover(recorder, request)
	if recorder.Code != 200 || recorder.Header().Get("Cache-Control") != "no-store" ||
		service.recoveryCode != "vne_recovery" || !strings.Contains(recorder.Body.String(), `"node_token":"vnn_recovered"`) {
		t.Fatalf("response = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestRateLimitedResponseSetsRetryAfter(t *testing.T) {
	service := &stubHTTPService{rotationErr: &ServiceError{
		Status: 429, Code: "VNT_RATE_LIMITED", Message: "Too many VNT requests.",
		Details: map[string]any{"retry_after_seconds": 7},
	}}
	handler := NewHTTPHandler(service, slog.New(slog.NewTextHandler(io.Discard, nil)))
	recorder := httptest.NewRecorder()
	handler.RotateCredential(recorder, httptest.NewRequest("POST", "/v1/vnt/nodes/vnt_one/credential/rotate", nil))
	if recorder.Code != 429 || recorder.Header().Get("Retry-After") != "7" {
		t.Fatalf("response = %d, Retry-After = %q", recorder.Code, recorder.Header().Get("Retry-After"))
	}
}

func TestListReturnsCursorAndForwardsFilters(t *testing.T) {
	service := &stubHTTPService{result: ListResult{
		Items:      []PublicNode{{NodeID: "vnt_one", Host: "203.0.113.10", Port: 29872}},
		NextCursor: "vnt_next",
	}}
	handler := NewHTTPHandler(service, slog.New(slog.NewTextHandler(io.Discard, nil)))
	recorder := httptest.NewRecorder()
	handler.List(recorder, httptest.NewRequest(
		"GET", "/v1/vnt/nodes?status=ONLINE&region=hk&cursor=vnt_previous&limit=25", nil,
	))

	if recorder.Code != 200 {
		t.Fatalf("response = %d %s", recorder.Code, recorder.Body.String())
	}
	if service.filter != (ListFilter{Status: "ONLINE", Region: "hk", Cursor: "vnt_previous", Limit: 25}) {
		t.Fatalf("filter = %#v", service.filter)
	}
	body := recorder.Body.String()
	for _, expected := range []string{`"node_id":"vnt_one"`, `"next_cursor":"vnt_next"`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("response does not contain %s: %s", expected, body)
		}
	}
}

func TestListRejectsNonNumericLimit(t *testing.T) {
	handler := NewHTTPHandler(&stubHTTPService{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	recorder := httptest.NewRecorder()
	handler.List(recorder, httptest.NewRequest("GET", "/v1/vnt/nodes?limit=many", nil))
	if recorder.Code != 400 || !strings.Contains(recorder.Body.String(), "INVALID_REQUEST") {
		t.Fatalf("response = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestListOwnedReturnsOnlyOwnerView(t *testing.T) {
	service := &stubHTTPService{ownedResult: OwnedListResult{
		Items:      []OwnedNode{{NodeID: "vnt_owned", Host: "203.0.113.10", State: StateOffline}},
		NextCursor: "vnt_next",
	}}
	handler := NewHTTPHandler(service, slog.New(slog.NewTextHandler(io.Discard, nil)))
	recorder := httptest.NewRecorder()
	handler.ListOwned(recorder, httptest.NewRequest("GET", "/v1/users/me/vnt-nodes?status=OFFLINE&cursor=vnt_previous&limit=25", nil))
	if recorder.Code != 200 || service.ownedFilter != (OwnedListFilter{Status: StateOffline, Cursor: "vnt_previous", Limit: 25}) {
		t.Fatalf("response = %d %s, filter = %#v", recorder.Code, recorder.Body.String(), service.ownedFilter)
	}
	body := recorder.Body.String()
	for _, forbidden := range []string{"node_token", "secret_hash", "owner_player_id"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("owned node response exposed %q: %s", forbidden, body)
		}
	}
}

func TestRotateCredentialReturnsOverlapDeadlineAndNoStore(t *testing.T) {
	service := &stubHTTPService{rotationResult: CredentialRotationResult{
		NodeToken: "vnn_replacement", CredentialExpiresAt: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
		PreviousValidUntil: time.Date(2026, 8, 4, 12, 1, 0, 0, time.UTC),
	}}
	handler := NewHTTPHandler(service, slog.New(slog.NewTextHandler(io.Discard, nil)))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest("POST", "/v1/vnt/nodes/vnt_one/credential/rotate", nil)
	request.Header.Set("Authorization", "Bearer vnn_current")
	handler.RotateCredential(recorder, request)

	if recorder.Code != 200 || recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("response = %d, Cache-Control = %q", recorder.Code, recorder.Header().Get("Cache-Control"))
	}
	body := recorder.Body.String()
	for _, expected := range []string{`"node_token":"vnn_replacement"`, `"previous_valid_until":"2026-08-04T12:01:00Z"`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("response does not contain %s: %s", expected, body)
		}
	}
}
