package metaserver

import (
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestSafeErrorClassDoesNotExposeDatabaseDetails(t *testing.T) {
	err := &pgconn.PgError{
		Code: "23514", Detail: `failing row contains {"private_loadout":"secret"}`,
	}
	class := safeErrorClass(err)
	if class != "postgres_23514" || strings.Contains(class, "secret") {
		t.Fatalf("unsafe error class %q", class)
	}
	if class = safeErrorClass(errors.New("bearer secret")); strings.Contains(class, "secret") {
		t.Fatalf("generic error message leaked through class %q", class)
	}
}
