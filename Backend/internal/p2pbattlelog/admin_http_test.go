package p2pbattlelog

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

type fakeAdminHTTPService struct {
	raw AdminRawEvidence
	err error
}

func (f fakeAdminHTTPService) MatchEvidence(context.Context, string) (AdminMatchEvidence, error) {
	return AdminMatchEvidence{}, f.err
}

func (f fakeAdminHTTPService) RawEvidence(context.Context, string) (AdminRawEvidence, error) {
	return f.raw, f.err
}

func TestAdminRawEvidenceDisablesCaching(t *testing.T) {
	service := fakeAdminHTTPService{raw: AdminRawEvidence{
		EvidenceID: "p2pr_test", MatchID: "p2pm_test", ReporterPlayerID: "player_test",
		RawSHA256:        "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ValidationStatus: "ACCEPTED", Snapshot: json.RawMessage(`{"schema_version":3}`),
	}}
	handler := NewAdminHTTPHandler(service, slog.Default())
	router := chi.NewRouter()
	router.Get("/reports/{evidence_id}/raw", handler.RawEvidence)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/reports/p2pr_test/raw", nil))

	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d cache=%q body=%s", response.Code, response.Header().Get("Cache-Control"), response.Body.String())
	}
	if !json.Valid(response.Body.Bytes()) {
		t.Fatalf("invalid JSON response: %s", response.Body.String())
	}
}

func TestAdminRawEvidenceMapsMissingRow(t *testing.T) {
	handler := NewAdminHTTPHandler(fakeAdminHTTPService{err: pgx.ErrNoRows}, slog.Default())
	router := chi.NewRouter()
	router.Get("/reports/{evidence_id}/raw", handler.RawEvidence)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/reports/missing/raw", nil))

	if response.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
