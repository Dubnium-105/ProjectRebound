package gameserver

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type stubHTTPService struct {
	server Server
}

func (s *stubHTTPService) IssueRegistrationCredential(context.Context, RegistrationCredentialInput) (RegistrationCredentialResult, error) {
	return RegistrationCredentialResult{}, nil
}
func (s *stubHTTPService) Register(context.Context, RegistrationInput, string) (RegistrationResult, error) {
	return RegistrationResult{}, nil
}
func (s *stubHTTPService) VerifySignedRequest(context.Context, SignedRequestInput) (SignedRequestPrincipal, error) {
	return SignedRequestPrincipal{}, nil
}
func (s *stubHTTPService) RotateCredential(context.Context, string, string, string) (CredentialRotationResult, error) {
	return CredentialRotationResult{}, nil
}
func (s *stubHTTPService) Heartbeat(context.Context, string, string, HeartbeatInput) (Server, error) {
	return s.server, nil
}
func (s *stubHTTPService) Deregister(context.Context, string, string) error { return nil }
func (s *stubHTTPService) Get(context.Context, string) (Server, error)      { return s.server, nil }
func (s *stubHTTPService) List(context.Context, ListFilter) (ListResult, error) {
	return ListResult{Items: []Server{s.server}}, nil
}
func (s *stubHTTPService) HeartbeatInterval() int { return 15 }

func TestPublicGameServerResponseDoesNotLeakCredentials(t *testing.T) {
	service := &stubHTTPService{server: Server{
		ID: "gs_test", InstanceID: "secret-instance", DisplayName: "Public",
		Region: "us-west", Mode: "tdm", Version: "1.0.0", PublicHost: "203.0.113.10",
		PublicPort: 7777, MaxPlayers: 12, State: StateReady,
		ServerTokenHash: []byte("secret-hash"), RegistrationIssuer: "secret-issuer",
		LastHeartbeatAt: time.Now(),
	}}
	handler := NewHTTPHandler(service, slog.New(slog.NewTextHandler(io.Discard, nil)))
	recorder := httptest.NewRecorder()
	handler.Get(recorder, httptest.NewRequest("GET", "/v1/game-servers/gs_test", nil))
	body := recorder.Body.String()
	for _, forbidden := range []string{"secret-instance", "secret-hash", "secret-issuer", "server_token"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("public response leaked %q: %s", forbidden, body)
		}
	}
}
