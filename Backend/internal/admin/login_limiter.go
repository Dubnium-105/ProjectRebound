package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"sync"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/config"
	"github.com/redis/go-redis/v9"
)

const (
	adminLoginLimitWindow  = time.Minute
	adminLoginLocalMaxKeys = 10_000
)

type LoginLimitDecision struct {
	Allowed    bool
	Dimension  string
	RetryAfter time.Duration
}

type LoginLimiter interface {
	Check(context.Context, string, string) LoginLimitDecision
}

type loginRateStore interface {
	Allow(context.Context, string, int, time.Duration) (bool, time.Duration, error)
}

type DistributedLoginLimiter struct {
	store  loginRateStore
	local  *localLoginRateStore
	cfg    config.AdminConfig
	logger *slog.Logger
}

func NewLoginLimiter(client redis.Cmdable, cfg config.AdminConfig, logger *slog.Logger) *DistributedLoginLimiter {
	return &DistributedLoginLimiter{
		store:  &redisLoginRateStore{client: client},
		local:  newLocalLoginRateStore(adminLoginLocalMaxKeys),
		cfg:    cfg,
		logger: logger,
	}
}

func (l *DistributedLoginLimiter) Check(ctx context.Context, ipAddress, username string) LoginLimitDecision {
	for _, dimension := range []struct {
		name  string
		value string
		limit int
	}{
		{name: "ip", value: ipAddress, limit: l.cfg.LoginPerIPPerMinute},
		{name: "username", value: normalizeUsername(username), limit: l.cfg.LoginPerUsernamePerMinute},
	} {
		if dimension.value == "" {
			continue
		}
		key := loginLimitKey(dimension.name, dimension.value)
		allowed, retryAfter, err := l.store.Allow(ctx, key, dimension.limit, adminLoginLimitWindow)
		if err != nil {
			localLimit := max(1, dimension.limit/2)
			allowed, retryAfter, _ = l.local.Allow(ctx, key, localLimit, adminLoginLimitWindow)
			l.logger.WarnContext(ctx, "Redis administrator login limiter unavailable; using conservative local limit",
				"dimension", dimension.name, "error", err)
		}
		if !allowed {
			if retryAfter < time.Second {
				retryAfter = time.Second
			}
			return LoginLimitDecision{Dimension: dimension.name, RetryAfter: retryAfter}
		}
	}
	return LoginLimitDecision{Allowed: true}
}

func loginLimitKey(dimension, value string) string {
	hash := sha256.Sum256([]byte(value))
	return "admin:login:v1:" + dimension + ":" + hex.EncodeToString(hash[:])
}

const adminLoginTokenBucketScript = `
local values = redis.call('HMGET', KEYS[1], 'tokens', 'updated_at')
local capacity = tonumber(ARGV[1])
local window_ms = tonumber(ARGV[2])
local now_ms = tonumber(ARGV[3])
local tokens = tonumber(values[1]) or capacity
local updated_at = tonumber(values[2]) or now_ms
local elapsed = math.max(0, now_ms - updated_at)
tokens = math.min(capacity, tokens + (elapsed * capacity / window_ms))
local allowed = 0
local retry_ms = 0
if tokens >= 1 then
  tokens = tokens - 1
  allowed = 1
else
  retry_ms = math.ceil((1 - tokens) * window_ms / capacity)
end
redis.call('HSET', KEYS[1], 'tokens', tokens, 'updated_at', now_ms)
redis.call('PEXPIRE', KEYS[1], window_ms * 2)
return {allowed, retry_ms}
`

type redisLoginRateStore struct {
	client redis.Cmdable
}

func (s *redisLoginRateStore) Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, time.Duration, error) {
	result, err := s.client.Eval(
		ctx,
		adminLoginTokenBucketScript,
		[]string{key},
		limit,
		window.Milliseconds(),
		time.Now().UnixMilli(),
	).Result()
	if err != nil {
		return false, 0, fmt.Errorf("evaluate administrator login rate limit: %w", err)
	}
	values, ok := result.([]any)
	if !ok || len(values) != 2 {
		return false, 0, fmt.Errorf("unexpected administrator login limiter result %T", result)
	}
	allowed, err := loginRedisInteger(values[0])
	if err != nil {
		return false, 0, err
	}
	retryMilliseconds, err := loginRedisInteger(values[1])
	if err != nil {
		return false, 0, err
	}
	return allowed == 1, time.Duration(retryMilliseconds) * time.Millisecond, nil
}

func loginRedisInteger(value any) (int64, error) {
	switch typed := value.(type) {
	case int64:
		return typed, nil
	case string:
		return strconv.ParseInt(typed, 10, 64)
	default:
		return 0, fmt.Errorf("unexpected Redis integer type %T", value)
	}
}

type localLoginBucket struct {
	tokens    float64
	updatedAt time.Time
	expiresAt time.Time
}

type localLoginRateStore struct {
	mu      sync.Mutex
	buckets map[string]localLoginBucket
	maxKeys int
	now     func() time.Time
}

func newLocalLoginRateStore(maxKeys int) *localLoginRateStore {
	return &localLoginRateStore{
		buckets: make(map[string]localLoginBucket),
		maxKeys: maxKeys,
		now:     time.Now,
	}
}

func (s *localLoginRateStore) Allow(_ context.Context, key string, limit int, window time.Duration) (bool, time.Duration, error) {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for existingKey, bucket := range s.buckets {
		if !bucket.expiresAt.After(now) {
			delete(s.buckets, existingKey)
		}
	}
	bucket, exists := s.buckets[key]
	if !exists {
		if len(s.buckets) >= s.maxKeys {
			return false, window, nil
		}
		bucket = localLoginBucket{tokens: float64(limit), updatedAt: now}
	}
	elapsed := now.Sub(bucket.updatedAt)
	if elapsed > 0 {
		bucket.tokens = math.Min(float64(limit), bucket.tokens+elapsed.Seconds()*float64(limit)/window.Seconds())
	}
	bucket.updatedAt = now
	bucket.expiresAt = now.Add(2 * window)
	if bucket.tokens < 1 {
		retry := time.Duration(math.Ceil((1-bucket.tokens)*window.Seconds()/float64(limit))) * time.Second
		s.buckets[key] = bucket
		return false, retry, nil
	}
	bucket.tokens--
	s.buckets[key] = bucket
	return true, 0, nil
}
