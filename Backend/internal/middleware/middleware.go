package middleware

import (
	"log/slog"
	"net/http"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/config"
)

func Chain(next http.Handler, cfg *config.Config, logger *slog.Logger, limiter *IPRateLimiter, metrics HTTPMetrics) http.Handler {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		if cfg.HTTP.MaxRequestBodyBytes > 0 && r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, cfg.HTTP.MaxRequestBodyBytes)
		}
		next.ServeHTTP(w, r)
	})
	var wrapped http.Handler = handler
	wrapped = limiter.Middleware(wrapped)
	wrapped = CORS(cfg.CORS, wrapped)
	wrapped = AccessLog(logger, metrics, wrapped)
	wrapped = Recovery(logger, wrapped)
	wrapped = RequestID(wrapped)
	return wrapped
}
