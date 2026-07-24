package admin

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type loginAuditCaptureExecutor struct {
	args []any
}

func (e *loginAuditCaptureExecutor) Exec(_ context.Context, _ string, args ...any) (pgconn.CommandTag, error) {
	e.args = args
	return pgconn.NewCommandTag("INSERT 0 1"), nil
}

func (e *loginAuditCaptureExecutor) QueryRow(context.Context, string, ...any) pgx.Row {
	return nil
}

func TestInsertLoginAuditNormalizesNilTurnstileErrorCodes(t *testing.T) {
	executor := &loginAuditCaptureExecutor{}
	repository := &AuthRepository{}

	err := repository.InsertLoginAudit(t.Context(), executor, LoginAudit{
		ID:        "adla_test",
		EventType: "PASSWORD_ACCEPTED",
		Result:    "SUCCESS",
	})
	if err != nil {
		t.Fatalf("InsertLoginAudit() error = %v", err)
	}
	if len(executor.args) != 15 {
		t.Fatalf("Exec() argument count = %d, want 15", len(executor.args))
	}
	errorCodes, ok := executor.args[10].([]string)
	if !ok {
		t.Fatalf("Turnstile error codes type = %T, want []string", executor.args[10])
	}
	if errorCodes == nil {
		t.Fatal("Turnstile error codes must be a non-nil empty slice")
	}
	if len(errorCodes) != 0 {
		t.Fatalf("Turnstile error codes = %v, want empty", errorCodes)
	}
}
