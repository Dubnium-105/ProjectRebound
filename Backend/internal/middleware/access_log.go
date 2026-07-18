package middleware

import (
	"bufio"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/projectrebound/matchserver/internal/requestctx"
)

type HTTPMetrics interface {
	ObserveHTTP(method, route string, status int, duration time.Duration)
}

type responseRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *responseRecorder) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *responseRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("HTTP response writer does not support hijacking")
	}
	connection, buffer, err := hijacker.Hijack()
	if err == nil && w.status == 0 {
		w.status = http.StatusSwitchingProtocols
	}
	return connection, buffer, err
}

func (w *responseRecorder) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *responseRecorder) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(data)
	w.bytes += n
	return n, err
}

func AccessLog(logger *slog.Logger, metrics HTTPMetrics, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		recorder := &responseRecorder{ResponseWriter: w}
		next.ServeHTTP(recorder, r)
		status := recorder.status
		if status == 0 {
			status = http.StatusOK
		}
		duration := time.Since(started)
		route := chi.RouteContext(r.Context()).RoutePattern()
		if metrics != nil {
			metrics.ObserveHTTP(r.Method, route, status, duration)
		}
		logger.InfoContext(r.Context(), "http request",
			"request_id", requestctx.RequestID(r.Context()),
			"method", r.Method,
			"path", r.URL.Path,
			"status", status,
			"response_bytes", recorder.bytes,
			"duration_ms", duration.Milliseconds(),
		)
	})
}
