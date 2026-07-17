package gameserver

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRegistrationAuthenticator(t *testing.T) {
	const token = "registration-token-with-at-least-32-bytes"
	authenticator, err := NewRegistrationAuthenticator("primary=" + token)
	if err != nil {
		t.Fatal(err)
	}
	handler := authenticator.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if RegistrationIssuer(r.Context()) != "primary" {
			t.Fatalf("issuer = %q", RegistrationIssuer(r.Context()))
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest("POST", "/v1/game-servers", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d", recorder.Code)
	}
}

func TestServerTokenHasHighEntropyHash(t *testing.T) {
	token, hash, err := newServerToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(token) < 64 || len(hash) != 32 || string(hash) != string(hashServerToken(token)) {
		t.Fatalf("unexpected token/hash shape")
	}
}
