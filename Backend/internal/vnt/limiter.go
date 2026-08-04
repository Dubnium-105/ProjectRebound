package vnt

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

const localLimitMaximumKeys = 10_000

type LimitOperation string

const (
	LimitEnrollment LimitOperation = "enrollment"
	LimitDirectory  LimitOperation = "directory"
	LimitBootstrap  LimitOperation = "bootstrap"
	LimitHeartbeat  LimitOperation = "heartbeat"
	LimitManagement LimitOperation = "management"
)

type LimitDecision struct {
	Allowed    bool
	RetryAfter time.Duration
}

type limitMetric interface {
	VNTRateLimited(string)
}

type limitStore interface {
	Allow(context.Context, string, int, time.Duration) (bool, time.Duration, error)
}

type Limiter struct {
	store   limitStore
	local   *localLimitStore
	config  config.VNTConfig
	logger  *slog.Logger
	metrics limitMetric
}

func NewLimiter(client redis.Cmdable, cfg config.VNTConfig, logger *slog.Logger) *Limiter {
	return &Limiter{
		store: &redisLimitStore{client: client}, local: newLocalLimitStore(localLimitMaximumKeys),
		config: cfg, logger: logger,
	}
}

func (l *Limiter) SetMetrics(metrics limitMetric) { l.metrics = metrics }

func (l *Limiter) Check(ctx context.Context, operation LimitOperation, identity string) LimitDecision {
	limit, window := l.policy(operation)
	if identity == "" || limit < 1 {
		return LimitDecision{Allowed: true}
	}
	key := limitKey(operation, identity)
	allowed, retryAfter, err := l.store.Allow(ctx, key, limit, window)
	if err != nil {
		localLimit := max(1, limit/2)
		allowed, retryAfter, _ = l.local.Allow(ctx, key, localLimit, window)
		if l.logger != nil {
			l.logger.WarnContext(ctx, "Redis VNT limiter unavailable; using conservative local limit",
				"operation", operation, "error", err)
		}
	}
	if allowed {
		return LimitDecision{Allowed: true}
	}
	if retryAfter < time.Second {
		retryAfter = time.Second
	}
	if l.metrics != nil {
		l.metrics.VNTRateLimited(string(operation))
	}
	return LimitDecision{RetryAfter: retryAfter}
}

func (l *Limiter) policy(operation LimitOperation) (int, time.Duration) {
	switch operation {
	case LimitEnrollment:
		return l.config.EnrollmentRequestsPerPlayerPerHour, time.Hour
	case LimitDirectory:
		return l.config.DirectoryRequestsPerIPPerMinute, time.Minute
	case LimitBootstrap:
		return l.config.BootstrapRequestsPerPlayerPerMinute, time.Minute
	case LimitHeartbeat:
		return l.config.HeartbeatRequestsPerCredentialPerMinute, time.Minute
	case LimitManagement:
		return l.config.ManagementRequestsPerCredentialPerHour, time.Hour
	default:
		return 0, 0
	}
}

func limitKey(operation LimitOperation, identity string) string {
	sum := sha256.Sum256([]byte(identity))
	return "vnt:limit:v1:" + string(operation) + ":" + hex.EncodeToString(sum[:])
}

const vntTokenBucketScript = `
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

type redisLimitStore struct {
	client redis.Cmdable
}

func (s *redisLimitStore) Allow(
	ctx context.Context,
	key string,
	limit int,
	window time.Duration,
) (bool, time.Duration, error) {
	result, err := s.client.Eval(
		ctx, vntTokenBucketScript, []string{key}, limit, window.Milliseconds(), time.Now().UnixMilli(),
	).Result()
	if err != nil {
		return false, 0, fmt.Errorf("evaluate Redis VNT rate limit: %w", err)
	}
	values, ok := result.([]any)
	if !ok || len(values) != 2 {
		return false, 0, fmt.Errorf("unexpected Redis VNT limiter result %T", result)
	}
	allowed, err := limitRedisInteger(values[0])
	if err != nil {
		return false, 0, err
	}
	retryMilliseconds, err := limitRedisInteger(values[1])
	if err != nil {
		return false, 0, err
	}
	return allowed == 1, time.Duration(retryMilliseconds) * time.Millisecond, nil
}

func limitRedisInteger(value any) (int64, error) {
	switch typed := value.(type) {
	case int64:
		return typed, nil
	case string:
		return strconv.ParseInt(typed, 10, 64)
	default:
		return 0, fmt.Errorf("unexpected Redis integer type %T", value)
	}
}

type localLimitBucket struct {
	tokens    float64
	updatedAt time.Time
	expiresAt time.Time
}

type localLimitStore struct {
	mu      sync.Mutex
	buckets map[string]localLimitBucket
	maxKeys int
	now     func() time.Time
}

func newLocalLimitStore(maxKeys int) *localLimitStore {
	return &localLimitStore{buckets: make(map[string]localLimitBucket), maxKeys: maxKeys, now: time.Now}
}

func (s *localLimitStore) Allow(
	_ context.Context,
	key string,
	limit int,
	window time.Duration,
) (bool, time.Duration, error) {
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
		bucket = localLimitBucket{tokens: float64(limit), updatedAt: now}
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
