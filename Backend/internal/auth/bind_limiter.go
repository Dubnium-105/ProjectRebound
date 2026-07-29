package auth

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
	bindLimitWindow       = time.Minute
	localBindLimitMaxKeys = 10_000
)

type BindLimitDimension string

const (
	BindLimitIP       BindLimitDimension = "ip"
	BindLimitSteamID  BindLimitDimension = "steam_id"
	BindLimitDeviceID BindLimitDimension = "device_id"
	BindLimitIPSteam  BindLimitDimension = "ip_steam_id"
	BindLimitIPDevice BindLimitDimension = "ip_device_id"
)

type BindLimitRequest struct {
	IPAddress string
	SteamID   string
	DeviceID  string
}

type BindLimitDecision struct {
	Allowed    bool
	Dimension  BindLimitDimension
	RetryAfter time.Duration
}

type bindLimitMetric interface {
	BindRateLimited(string)
}

type bindRateStore interface {
	Allow(context.Context, string, int, time.Duration) (bool, time.Duration, error)
}

type BindLimiter struct {
	store   bindRateStore
	local   *localBindRateStore
	config  config.AuthConfig
	logger  *slog.Logger
	metrics bindLimitMetric
}

func NewBindLimiter(client redis.Cmdable, cfg config.AuthConfig, logger *slog.Logger) *BindLimiter {
	return &BindLimiter{
		store:  &redisBindRateStore{client: client},
		local:  newLocalBindRateStore(localBindLimitMaxKeys),
		config: cfg,
		logger: logger,
	}
}

func (l *BindLimiter) SetMetrics(metrics bindLimitMetric) { l.metrics = metrics }

func (l *BindLimiter) Check(ctx context.Context, request BindLimitRequest) BindLimitDecision {
	dimensions := []struct {
		name  BindLimitDimension
		value string
		limit int
	}{
		{name: BindLimitIP, value: request.IPAddress, limit: l.config.BindRateLimit.PerIPPerMinute},
		{name: BindLimitSteamID, value: request.SteamID, limit: l.config.BindRateLimit.PerSteamIDPerMinute},
		{name: BindLimitIPSteam, value: request.IPAddress + "\x00" + request.SteamID, limit: l.config.BindRateLimit.PerSteamIDPerMinute},
	}
	if request.DeviceID != "" {
		dimensions = append(dimensions,
			struct {
				name  BindLimitDimension
				value string
				limit int
			}{name: BindLimitDeviceID, value: request.DeviceID, limit: l.config.BindRateLimit.PerDevicePerMinute},
			struct {
				name  BindLimitDimension
				value string
				limit int
			}{name: BindLimitIPDevice, value: request.IPAddress + "\x00" + request.DeviceID, limit: l.config.BindRateLimit.PerDevicePerMinute},
		)
	}

	for _, dimension := range dimensions {
		if dimension.value == "" || dimension.limit < 1 {
			continue
		}
		key := bindLimitKey(dimension.name, dimension.value)
		allowed, retryAfter, err := l.store.Allow(ctx, key, dimension.limit, bindLimitWindow)
		if err != nil {
			localLimit := max(1, dimension.limit/2)
			allowed, retryAfter, _ = l.local.Allow(ctx, key, localLimit, bindLimitWindow)
			l.logger.WarnContext(ctx, "Redis bind limiter unavailable; using conservative local limit",
				"dimension", dimension.name, "error", err)
		}
		if !allowed {
			if retryAfter < time.Second {
				retryAfter = time.Second
			}
			if l.metrics != nil {
				l.metrics.BindRateLimited(string(dimension.name))
			}
			return BindLimitDecision{Dimension: dimension.name, RetryAfter: retryAfter}
		}
	}
	return BindLimitDecision{Allowed: true}
}

func bindLimitKey(dimension BindLimitDimension, value string) string {
	sum := sha256.Sum256([]byte(value))
	return "auth:bind:v1:" + string(dimension) + ":" + hex.EncodeToString(sum[:])
}

const tokenBucketScript = `
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

type redisBindRateStore struct {
	client redis.Cmdable
}

func (s *redisBindRateStore) Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, time.Duration, error) {
	result, err := s.client.Eval(ctx, tokenBucketScript, []string{key}, limit, window.Milliseconds(), time.Now().UnixMilli()).Result()
	if err != nil {
		return false, 0, fmt.Errorf("evaluate Redis bind rate limit: %w", err)
	}
	values, ok := result.([]any)
	if !ok || len(values) != 2 {
		return false, 0, fmt.Errorf("unexpected Redis bind limiter result %T", result)
	}
	allowed, err := redisInteger(values[0])
	if err != nil {
		return false, 0, err
	}
	retryMilliseconds, err := redisInteger(values[1])
	if err != nil {
		return false, 0, err
	}
	return allowed == 1, time.Duration(retryMilliseconds) * time.Millisecond, nil
}

func redisInteger(value any) (int64, error) {
	switch typed := value.(type) {
	case int64:
		return typed, nil
	case string:
		return strconv.ParseInt(typed, 10, 64)
	default:
		return 0, fmt.Errorf("unexpected Redis integer type %T", value)
	}
}

type localTokenBucket struct {
	tokens    float64
	updatedAt time.Time
	expiresAt time.Time
}

type localBindRateStore struct {
	mu      sync.Mutex
	buckets map[string]localTokenBucket
	maxKeys int
	now     func() time.Time
}

func newLocalBindRateStore(maxKeys int) *localBindRateStore {
	return &localBindRateStore{buckets: make(map[string]localTokenBucket), maxKeys: maxKeys, now: time.Now}
}

func (s *localBindRateStore) Allow(_ context.Context, key string, limit int, window time.Duration) (bool, time.Duration, error) {
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
		bucket = localTokenBucket{tokens: float64(limit), updatedAt: now}
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
