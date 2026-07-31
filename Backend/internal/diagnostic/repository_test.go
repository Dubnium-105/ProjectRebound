package diagnostic

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

type captureExecutor struct {
	query string
	args  []any
	err   error
}

func (e *captureExecutor) Exec(_ context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	e.query = query
	e.args = args
	return pgconn.NewCommandTag("INSERT 0 1"), e.err
}

func TestRepositoryStoresPlayerAndReportVerbatim(t *testing.T) {
	executor := &captureExecutor{}
	report := "line one\r\nline two — unchanged\n"

	if err := NewRepository(executor).Store(context.Background(), "p_test", report); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(executor.query, "INSERT INTO diagnostic_reports") {
		t.Fatalf("query = %q", executor.query)
	}
	if len(executor.args) != 2 || executor.args[0] != "p_test" || executor.args[1] != report {
		t.Fatalf("args = %#v", executor.args)
	}
}

func TestRepositoryWrapsDatabaseError(t *testing.T) {
	executor := &captureExecutor{err: errors.New("database unavailable")}

	err := NewRepository(executor).Store(context.Background(), "p_test", "report")

	if err == nil || !strings.Contains(err.Error(), "store diagnostic report") {
		t.Fatalf("error = %v", err)
	}
}
