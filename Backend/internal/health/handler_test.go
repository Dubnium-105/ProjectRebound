package health

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/requestctx"
)

type checkerFunc func(context.Context) error

func (f checkerFunc) Check(ctx context.Context) error { return f(ctx) }

func TestReadyNamesFailedDependencyWithoutLeakingError(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := NewHandler([]Dependency{{
		Name: "postgres",
		Checker: checkerFunc(func(context.Context) error {
			return errors.New("password=do-not-leak")
		}),
	}}, time.Second, logger)
	req := httptest.NewRequest("GET", "/health/ready", nil)
	req = req.WithContext(requestctx.WithRequestID(req.Context(), "req_ready"))
	recorder := httptest.NewRecorder()
	handler.Ready(recorder, req)
	if recorder.Code != 503 || !strings.Contains(recorder.Body.String(), "postgres") || strings.Contains(recorder.Body.String(), "do-not-leak") {
		t.Fatalf("unexpected response: %d %s", recorder.Code, recorder.Body.String())
	}
}
