package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/config"
)

type testStepUpAuthenticator struct{}

func (testStepUpAuthenticator) AuthenticateStepUp(_ context.Context, token string, principal *Principal) error {
	if token != "valid-proof" || principal == nil || principal.AdminID != "adm_test" {
		return stepUpRequired()
	}
	return nil
}

func TestAuthenticatorAcceptsOnlyDedicatedAdminToken(t *testing.T) {
	const adminToken = "admin-test-token-with-at-least-32-bytes"
	authenticator, err := NewAuthenticator(config.AdminConfig{TokenSet: "operator=" + adminToken})
	if err != nil {
		t.Fatal(err)
	}
	handler := authenticator.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if principal := PrincipalFromContext(r.Context()); principal == nil || principal.AdminID != "operator" {
			t.Fatalf("principal = %#v", principal)
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	for _, test := range []struct {
		token string
		want  int
	}{
		{token: adminToken, want: http.StatusNoContent},
		{token: "eyJhbGciOiJFZERTQSJ9.player-token.signature", want: http.StatusUnauthorized},
	} {
		req := httptest.NewRequest("GET", "/v1/admin/players", nil)
		req.Header.Set("Authorization", "Bearer "+test.token)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		if recorder.Code != test.want {
			t.Errorf("token %q status = %d", test.token, recorder.Code)
		}
	}
}

func TestNetworkGuardHidesAdminAPIFromPublicAddress(t *testing.T) {
	guard, err := NewNetworkGuard([]string{"10.0.0.0/8"}, false)
	if err != nil {
		t.Fatal(err)
	}
	handler := guard.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest("GET", "/v1/admin/players", nil)
	req.RemoteAddr = "203.0.113.9:1234"
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d", recorder.Code)
	}
}

func TestRequireStepUpRejectsMissingOrInvalidProof(t *testing.T) {
	handler := RequireStepUp(testStepUpAuthenticator{})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	for _, test := range []struct {
		name  string
		token string
		want  int
	}{
		{name: "missing", want: http.StatusForbidden},
		{name: "invalid", token: "wrong-proof", want: http.StatusForbidden},
		{name: "valid", token: "valid-proof", want: http.StatusNoContent},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/v1/admin/relay-nodes/relay_test/revoke", nil)
			req = req.WithContext(context.WithValue(req.Context(), adminPrincipalKey, &Principal{
				AdminID: "adm_test", SessionID: "adm_ses_test",
			}))
			if test.token != "" {
				req.Header.Set("X-Admin-Step-Up", test.token)
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, req)
			if recorder.Code != test.want {
				t.Fatalf("status = %d, want %d", recorder.Code, test.want)
			}
		})
	}
}
