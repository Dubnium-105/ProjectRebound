package vnt

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/config"
)

type stubLimitStore struct {
	allowed    bool
	retryAfter time.Duration
	err        error
	key        string
}

func (s *stubLimitStore) Allow(_ context.Context, key string, _ int, _ time.Duration) (bool, time.Duration, error) {
	s.key = key
	return s.allowed, s.retryAfter, s.err
}

type stubLimitMetric struct{ operation string }

func (m *stubLimitMetric) VNTRateLimited(operation string) { m.operation = operation }

func TestLimiterHashesIdentityAndReportsOperation(t *testing.T) {
	store := &stubLimitStore{retryAfter: 1500 * time.Millisecond}
	metric := &stubLimitMetric{}
	limiter := &Limiter{
		store: store, local: newLocalLimitStore(10), config: config.Defaults.VNT, metrics: metric,
	}
	decision := limiter.Check(t.Context(), LimitHeartbeat, "vnt_node\x00vnn_secret")
	if decision.Allowed || decision.RetryAfter != 1500*time.Millisecond {
		t.Fatalf("decision = %#v", decision)
	}
	if strings.Contains(store.key, "vnt_node") || strings.Contains(store.key, "vnn_secret") {
		t.Fatalf("rate-limit key exposes identity: %q", store.key)
	}
	if metric.operation != string(LimitHeartbeat) {
		t.Fatalf("metric operation = %q", metric.operation)
	}
}

func TestLimiterUsesConservativeLocalFallback(t *testing.T) {
	store := &stubLimitStore{err: errors.New("redis unavailable")}
	cfg := config.Defaults.VNT
	cfg.EnrollmentRequestsPerPlayerPerHour = 2
	limiter := &Limiter{store: store, local: newLocalLimitStore(10), config: cfg}
	if decision := limiter.Check(t.Context(), LimitEnrollment, "player_one"); !decision.Allowed {
		t.Fatalf("first local request = %#v", decision)
	}
	if decision := limiter.Check(t.Context(), LimitEnrollment, "player_one"); decision.Allowed || decision.RetryAfter <= 0 {
		t.Fatalf("second local request = %#v", decision)
	}
}

func TestLimiterUsesIndependentDirectoryAndBootstrapPolicies(t *testing.T) {
	cfg := config.Defaults.VNT
	cfg.DirectoryRequestsPerIPPerMinute = 17
	cfg.BootstrapRequestsPerPlayerPerMinute = 9
	limiter := &Limiter{config: cfg}
	for operation, expected := range map[LimitOperation]int{
		LimitDirectory: 17,
		LimitBootstrap: 9,
	} {
		limit, window := limiter.policy(operation)
		if limit != expected || window != time.Minute {
			t.Fatalf("policy %q = %d/%s", operation, limit, window)
		}
	}
}

type rejectingLimiter struct{}

func (rejectingLimiter) Check(context.Context, LimitOperation, string) LimitDecision {
	return LimitDecision{RetryAfter: 1500 * time.Millisecond}
}

func TestEnrollmentRateLimitRunsBeforeRepositoryAccess(t *testing.T) {
	service := &Service{limiter: rejectingLimiter{}}
	_, err := service.CreateEnrollment(t.Context(), Actor{
		PlayerID: "player_one", AccountStatus: "ACTIVE", SteamVerified: true, IntegrityTrusted: true,
	}, "node-one")
	status, code, _, details := errorDetails(err)
	if status != 429 || code != "VNT_RATE_LIMITED" || details["retry_after_seconds"] != 2 {
		t.Fatalf("rate-limit error = status %d code %q details %#v", status, code, details)
	}
}

func TestDirectoryRateLimitRunsBeforeRepositoryAccess(t *testing.T) {
	service := &Service{limiter: rejectingLimiter{}}
	ctx := WithRequestMeta(t.Context(), RequestMeta{IPAddress: "192.0.2.90"})
	_, err := service.List(ctx, ListFilter{})
	status, code, _, details := errorDetails(err)
	if status != 429 || code != "VNT_RATE_LIMITED" || details["operation"] != LimitDirectory {
		t.Fatalf("directory rate-limit error = status %d code %q details %#v", status, code, details)
	}
}
