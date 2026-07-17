package middleware

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/projectrebound/matchserver/internal/api"
)

type rateBucket struct {
	tokens     float64
	updatedAt  time.Time
	lastSeenAt time.Time
}

type IPRateLimiter struct {
	mu          sync.Mutex
	rate        float64
	burst       float64
	trustProxy  bool
	buckets     map[string]*rateBucket
	lastCleanup time.Time
	now         func() time.Time
}

func NewIPRateLimiter(rate float64, burst int, trustProxy bool) *IPRateLimiter {
	return &IPRateLimiter{
		rate:        rate,
		burst:       float64(burst),
		trustProxy:  trustProxy,
		buckets:     make(map[string]*rateBucket),
		lastCleanup: time.Now(),
		now:         time.Now,
	}
}

func (l *IPRateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !l.Allow(clientIP(r, l.trustProxy)) {
			w.Header().Set("Retry-After", "1")
			api.WriteError(w, r, http.StatusTooManyRequests, "RATE_LIMITED", "Too many requests.", nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (l *IPRateLimiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	if now.Sub(l.lastCleanup) >= 5*time.Minute {
		for candidate, bucket := range l.buckets {
			if now.Sub(bucket.lastSeenAt) >= 10*time.Minute {
				delete(l.buckets, candidate)
			}
		}
		l.lastCleanup = now
	}
	bucket := l.buckets[key]
	if bucket == nil {
		bucket = &rateBucket{tokens: l.burst, updatedAt: now}
		l.buckets[key] = bucket
	}
	bucket.tokens += now.Sub(bucket.updatedAt).Seconds() * l.rate
	if bucket.tokens > l.burst {
		bucket.tokens = l.burst
	}
	bucket.updatedAt = now
	bucket.lastSeenAt = now
	if bucket.tokens < 1 {
		return false
	}
	bucket.tokens--
	return true
}

func clientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0]); net.ParseIP(forwarded) != nil {
			return forwarded
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil && net.ParseIP(host) != nil {
		return host
	}
	if net.ParseIP(r.RemoteAddr) != nil {
		return r.RemoteAddr
	}
	return "unknown"
}
