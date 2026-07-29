package middleware

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/config"
)

func CORS(cfg config.CORSConfig, next http.Handler) http.Handler {
	allowed := make(map[string]struct{}, len(cfg.AllowedOrigins))
	for _, origin := range cfg.AllowedOrigins {
		allowed[origin] = struct{}{}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && originAllowed(allowed, origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Add("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", strings.Join(cfg.AllowedHeaders, ", "))
			w.Header().Set("Access-Control-Max-Age", strconv.Itoa(cfg.MaxAgeSeconds))
			if cfg.AllowCredentials && origin != "*" {
				w.Header().Set("Access-Control-Allow-Credentials", "true")
			}
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func originAllowed(allowed map[string]struct{}, origin string) bool {
	if _, ok := allowed["*"]; ok {
		return true
	}
	_, ok := allowed[origin]
	return ok
}
