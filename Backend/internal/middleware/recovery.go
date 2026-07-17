package middleware

import (
	"log/slog"
	"net/http"

	"github.com/projectrebound/matchserver/internal/api"
	"github.com/projectrebound/matchserver/internal/requestctx"
)

func Recovery(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				logger.ErrorContext(r.Context(), "panic recovered",
					"request_id", requestctx.RequestID(r.Context()),
					"panic", recovered,
				)
				api.WriteError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal server error.", nil)
			}
		}()
		next.ServeHTTP(w, r)
	})
}
