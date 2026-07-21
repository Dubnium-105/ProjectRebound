package auth

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

func TestRedisBindRateStoreIsAtomicAndExpires(t *testing.T) {
	address := os.Getenv("TEST_REDIS_ADDRESS")
	if address == "" {
		t.Skip("TEST_REDIS_ADDRESS is not set")
	}
	client := redis.NewClient(&redis.Options{Addr: address})
	t.Cleanup(func() { _ = client.Close() })
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatalf("ping test Redis: %v", err)
	}
	key := bindLimitKey(BindLimitDeviceID, "integration-"+uuid.NewString())
	t.Cleanup(func() { _ = client.Del(context.Background(), key).Err() })
	store := &redisBindRateStore{client: client}
	for attempt := 0; attempt < 2; attempt++ {
		allowed, _, err := store.Allow(ctx, key, 2, time.Minute)
		if err != nil || !allowed {
			t.Fatalf("attempt %d: allowed=%v err=%v", attempt+1, allowed, err)
		}
	}
	allowed, retryAfter, err := store.Allow(ctx, key, 2, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if allowed || retryAfter <= 0 {
		t.Fatalf("third attempt must be limited: allowed=%v retry_after=%s", allowed, retryAfter)
	}
	ttl, err := client.PTTL(ctx, key).Result()
	if err != nil || ttl <= 0 || ttl > 2*time.Minute {
		t.Fatalf("rate-limit key TTL=%s err=%v", ttl, err)
	}
}
