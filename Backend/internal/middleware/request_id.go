package middleware

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/projectrebound/matchserver/internal/requestctx"
)

var requestIDPattern = regexp.MustCompile(`^req_[A-Za-z0-9_-]{1,120}$`)

func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := r.Header.Get("X-Request-Id")
		if !requestIDPattern.MatchString(requestID) {
			requestID = "req_" + strings.ReplaceAll(uuid.NewString(), "-", "")
		}
		w.Header().Set("X-Request-Id", requestID)
		next.ServeHTTP(w, r.WithContext(requestctx.WithRequestID(r.Context(), requestID)))
	})
}
