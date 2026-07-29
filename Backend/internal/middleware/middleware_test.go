package middleware

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/config"
)

func TestRequestIDReplacesInvalidCallerValue(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Request-Id", strings.Repeat("x", 200))
	recorder := httptest.NewRecorder()
	RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(recorder, req)
	if value := recorder.Header().Get("X-Request-Id"); !strings.HasPrefix(value, "req_") {
		t.Fatalf("X-Request-Id = %q", value)
	}
}

func TestIPRateLimiterSeparatesClients(t *testing.T) {
	limiter := NewIPRateLimiter(1, 1, false)
	now := time.Unix(100, 0)
	limiter.now = func() time.Time { return now }
	if !limiter.Allow("one") || limiter.Allow("one") == true || !limiter.Allow("two") {
		t.Fatal("unexpected limiter decision")
	}
	now = now.Add(time.Second)
	if !limiter.Allow("one") {
		t.Fatal("token did not refill")
	}
}

func TestChainReturnsEnvelopeWhenRateLimited(t *testing.T) {
	cfg := config.Defaults
	limiter := NewIPRateLimiter(1, 1, false)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}), &cfg, logger, limiter, nil)

	first := httptest.NewRequest("GET", "/", nil)
	first.RemoteAddr = "192.0.2.1:1000"
	handler.ServeHTTP(httptest.NewRecorder(), first)
	second := httptest.NewRequest("GET", "/", nil)
	second.RemoteAddr = "192.0.2.1:1001"
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, second)
	if recorder.Code != http.StatusTooManyRequests || !strings.Contains(recorder.Body.String(), `"request_id":"req_`) {
		t.Fatalf("status/body = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestCORSAllowsCredentialedAdminDevelopmentOrigin(t *testing.T) {
	cfg := config.Defaults.CORS
	handler := CORS(cfg, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	request := httptest.NewRequest(http.MethodOptions, "/v1/admin/auth/refresh", nil)
	request.Header.Set("Origin", "http://localhost:5173")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Header().Get("Access-Control-Allow-Origin") != "http://localhost:5173" {
		t.Fatalf("allow origin = %q", recorder.Header().Get("Access-Control-Allow-Origin"))
	}
	if recorder.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Fatalf("allow credentials = %q", recorder.Header().Get("Access-Control-Allow-Credentials"))
	}
}
