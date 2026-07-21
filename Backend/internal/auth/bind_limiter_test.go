package auth

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/projectrebound/matchserver/internal/config"
)

type stubBindRateStore struct {
	keys  []string
	allow func(string) (bool, time.Duration, error)
}

func (s *stubBindRateStore) Allow(_ context.Context, key string, _ int, _ time.Duration) (bool, time.Duration, error) {
	s.keys = append(s.keys, key)
	return s.allow(key)
}

type stubBindLimitMetric struct{ dimension string }

func (m *stubBindLimitMetric) BindRateLimited(dimension string) { m.dimension = dimension }

func testAuthRateConfig() config.AuthConfig {
	return config.AuthConfig{BindRateLimit: config.AuthBindRateLimitConfig{
		PerIPPerMinute: 5, PerDevicePerMinute: 3, PerSteamIDPerMinute: 3,
	}}
}

func TestBindLimiterChecksHashedDimensions(t *testing.T) {
	store := &stubBindRateStore{allow: func(key string) (bool, time.Duration, error) {
		if strings.Contains(key, ":device_id:") {
			return false, 7 * time.Second, nil
		}
		return true, 0, nil
	}}
	limiter := &BindLimiter{
		store: store, local: newLocalBindRateStore(100), config: testAuthRateConfig(),
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	metric := &stubBindLimitMetric{}
	limiter.SetMetrics(metric)
	decision := limiter.Check(context.Background(), BindLimitRequest{
		IPAddress: "192.0.2.10", SteamID: "76561198950613585", DeviceID: "installation-1234",
	})
	if decision.Allowed || decision.Dimension != BindLimitDeviceID || decision.RetryAfter != 7*time.Second {
		t.Fatalf("decision = %#v", decision)
	}
	if metric.dimension != string(BindLimitDeviceID) {
		t.Fatalf("metric dimension = %q", metric.dimension)
	}
	for _, key := range store.keys {
		if strings.Contains(key, "192.0.2.10") || strings.Contains(key, "76561198950613585") || strings.Contains(key, "installation-1234") {
			t.Fatalf("rate-limit key contains raw identifier: %q", key)
		}
	}
}

func TestBindLimiterUsesConservativeLocalFallback(t *testing.T) {
	store := &stubBindRateStore{allow: func(string) (bool, time.Duration, error) {
		return false, 0, errors.New("Redis unavailable")
	}}
	config := testAuthRateConfig()
	config.BindRateLimit.PerIPPerMinute = 2
	limiter := &BindLimiter{
		store: store, local: newLocalBindRateStore(100), config: config,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	request := BindLimitRequest{IPAddress: "192.0.2.10", SteamID: "76561198950613585"}
	if decision := limiter.Check(context.Background(), request); !decision.Allowed {
		t.Fatalf("first fallback request denied: %#v", decision)
	}
	if decision := limiter.Check(context.Background(), request); decision.Allowed || decision.Dimension != BindLimitIP {
		t.Fatalf("second fallback request = %#v", decision)
	}
}

func TestLocalBindRateStoreBoundsCardinality(t *testing.T) {
	store := newLocalBindRateStore(1)
	if allowed, _, _ := store.Allow(context.Background(), "first", 1, time.Minute); !allowed {
		t.Fatal("first key denied")
	}
	if allowed, retry, _ := store.Allow(context.Background(), "second", 1, time.Minute); allowed || retry != time.Minute {
		t.Fatalf("cardinality overflow allowed=%v retry=%s", allowed, retry)
	}
}
