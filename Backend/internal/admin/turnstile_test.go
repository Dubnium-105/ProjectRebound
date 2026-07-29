package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/config"
)

func TestTurnstileVerifierValidatesServerResponse(t *testing.T) {
	var receivedRemoteIP string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.Form.Get("secret") != "server-secret" ||
			r.Form.Get("response") != "browser-token" ||
			r.Form.Get("idempotency_key") == "" {
			t.Fatalf("unexpected Siteverify form: %#v", r.Form)
		}
		receivedRemoteIP = r.Form.Get("remoteip")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success":  true,
			"hostname": "admin.example.com",
			"action":   "admin_login",
		})
	}))
	defer server.Close()

	cfg := config.Defaults.Admin
	cfg.TurnstileSiteKey = "public-site-key"
	cfg.TurnstileSecretKey = "server-secret"
	cfg.TurnstileVerifyURL = server.URL
	verifier := NewCloudflareTurnstileVerifier(cfg)
	result, err := verifier.Verify(t.Context(), "browser-token", "192.0.2.5")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success || receivedRemoteIP != "192.0.2.5" {
		t.Fatalf("result/IP = %#v / %q", result, receivedRemoteIP)
	}
}

func TestTurnstileVerifierRejectsHostnameAndActionMismatch(t *testing.T) {
	for _, test := range []struct {
		name     string
		hostname string
		action   string
		code     string
	}{
		{name: "hostname", hostname: "attacker.example", action: "admin_login", code: "hostname-mismatch"},
		{name: "action", hostname: "admin.example.com", action: "signup", code: "action-mismatch"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"success":  true,
					"hostname": test.hostname,
					"action":   test.action,
				})
			}))
			defer server.Close()
			cfg := config.Defaults.Admin
			cfg.TurnstileSiteKey = "public-site-key"
			cfg.TurnstileSecretKey = "server-secret"
			cfg.TurnstileVerifyURL = server.URL
			result, err := NewCloudflareTurnstileVerifier(cfg).Verify(t.Context(), "browser-token", "")
			if err != nil {
				t.Fatal(err)
			}
			if result.Success || len(result.ErrorCodes) != 1 || result.ErrorCodes[0] != test.code {
				t.Fatalf("result = %#v", result)
			}
		})
	}
}

func TestTurnstileVerifierFailsClosedWhenNotConfigured(t *testing.T) {
	verifier := NewCloudflareTurnstileVerifier(config.Defaults.Admin)
	if verifier.Configured() {
		t.Fatal("verifier unexpectedly configured")
	}
	if _, err := verifier.Verify(t.Context(), "browser-token", ""); err != ErrTurnstileUnavailable {
		t.Fatalf("error = %v", err)
	}
}

func TestTurnstileVerifierRejectsMissingTokenWithoutSiteverifyRequest(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls++
	}))
	defer server.Close()
	cfg := config.Defaults.Admin
	cfg.TurnstileSiteKey = "public-site-key"
	cfg.TurnstileSecretKey = "server-secret"
	cfg.TurnstileVerifyURL = server.URL
	result, err := NewCloudflareTurnstileVerifier(cfg).Verify(t.Context(), "  ", "192.0.2.5")
	if err != nil {
		t.Fatal(err)
	}
	if result.Success || calls != 0 ||
		len(result.ErrorCodes) != 1 || result.ErrorCodes[0] != "invalid-input-response" {
		t.Fatalf("missing token result/calls = %#v / %d", result, calls)
	}
}

func TestTurnstileVerifierPreservesTimeoutOrDuplicateRejection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": false, "error-codes": []string{"timeout-or-duplicate"},
		})
	}))
	defer server.Close()
	cfg := config.Defaults.Admin
	cfg.TurnstileSiteKey = "public-site-key"
	cfg.TurnstileSecretKey = "server-secret"
	cfg.TurnstileVerifyURL = server.URL
	result, err := NewCloudflareTurnstileVerifier(cfg).Verify(t.Context(), "spent-token", "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Success || len(result.ErrorCodes) != 1 ||
		result.ErrorCodes[0] != "timeout-or-duplicate" {
		t.Fatalf("duplicate token result = %#v", result)
	}
}

func TestTurnstileVerifierRetriesTransientFailureWithSameIdempotencyKey(t *testing.T) {
	var mutex sync.Mutex
	var calls int
	var idempotencyKeys []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		mutex.Lock()
		calls++
		idempotencyKeys = append(idempotencyKeys, r.Form.Get("idempotency_key"))
		currentCall := calls
		mutex.Unlock()
		if currentCall == 1 {
			http.Error(w, "temporary", http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true, "hostname": "admin.example.com", "action": "admin_login",
		})
	}))
	defer server.Close()

	cfg := config.Defaults.Admin
	cfg.TurnstileSiteKey = "public-site-key"
	cfg.TurnstileSecretKey = "server-secret"
	cfg.TurnstileVerifyURL = server.URL
	result, err := NewCloudflareTurnstileVerifier(cfg).Verify(t.Context(), "browser-token", "")
	if err != nil || !result.Success {
		t.Fatalf("retry result = %#v, %v", result, err)
	}
	if calls != 2 || idempotencyKeys[0] == "" || idempotencyKeys[0] != idempotencyKeys[1] {
		t.Fatalf("calls/idempotency keys = %d / %#v", calls, idempotencyKeys)
	}
}

func TestTurnstileVerifierDoesNotRetryPermanentHTTPFailure(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		http.Error(w, "bad request", http.StatusBadRequest)
	}))
	defer server.Close()

	cfg := config.Defaults.Admin
	cfg.TurnstileSiteKey = "public-site-key"
	cfg.TurnstileSecretKey = "server-secret"
	cfg.TurnstileVerifyURL = server.URL
	if _, err := NewCloudflareTurnstileVerifier(cfg).Verify(t.Context(), "browser-token", ""); err == nil {
		t.Fatal("permanent Siteverify failure was accepted")
	}
	if calls != 1 {
		t.Fatalf("permanent failure calls = %d", calls)
	}
}
