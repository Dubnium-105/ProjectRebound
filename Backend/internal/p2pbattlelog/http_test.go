package p2pbattlelog

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

type fakeHTTPService struct {
	submitted   bool
	matchID     string
	reportID    string
	reportToken string
	raw         []byte
}

func (f *fakeHTTPService) ActiveMatch(context.Context, Actor, string) (MatchSession, error) {
	return MatchSession{}, nil
}
func (f *fakeHTTPService) IssueCapability(context.Context, Actor, string) (CapabilityResult, error) {
	return CapabilityResult{}, nil
}
func (f *fakeHTTPService) UpdatePresence(context.Context, Actor, string, PresenceInput) (PresenceResult, error) {
	return PresenceResult{}, nil
}
func (f *fakeHTTPService) SubmitReport(_ context.Context, _ Actor, matchID, reportID, token string, raw []byte) (ReportResult, error) {
	f.submitted = true
	f.matchID, f.reportID, f.reportToken = matchID, reportID, token
	f.raw = bytes.Clone(raw)
	return ReportResult{MatchID: matchID, ReportID: reportID, Status: "ACCEPTED", Warnings: []Warning{}}, nil
}
func (f *fakeHTTPService) Result(context.Context, Actor, string) (FinalizedResult, error) {
	return FinalizedResult{}, nil
}

func TestSubmitReportRejectsNonJSONBeforeService(t *testing.T) {
	service := &fakeHTTPService{}
	router := reportTestRouter(service, 32)
	request := httptest.NewRequest(http.MethodPut, "/matches/match/reports/report", bytes.NewBufferString("{}"))
	request.Header.Set("Content-Type", "text/plain")
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)
	if response.Code != http.StatusUnsupportedMediaType || service.submitted {
		t.Fatalf("status=%d submitted=%v body=%s", response.Code, service.submitted, response.Body.String())
	}
}

func TestSubmitReportRejectsOversizeBodyBeforeService(t *testing.T) {
	service := &fakeHTTPService{}
	router := reportTestRouter(service, 8)
	request := httptest.NewRequest(http.MethodPut, "/matches/match/reports/report", bytes.NewBufferString(`{"more":true}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge || service.submitted {
		t.Fatalf("status=%d submitted=%v body=%s", response.Code, service.submitted, response.Body.String())
	}
}

func TestSubmitReportForwardsDirectBodyAndCapabilityHeader(t *testing.T) {
	service := &fakeHTTPService{}
	router := reportTestRouter(service, 1024)
	body := []byte(`{"schema":"project-rebound.p2p-battlelog.raw"}`)
	request := httptest.NewRequest(http.MethodPut, "/matches/p2pm_a/reports/report.a", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	request.Header.Set(reportTokenHeader, "p2r_secret")
	response := httptest.NewRecorder()

	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !service.submitted {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if service.matchID != "p2pm_a" || service.reportID != "report.a" ||
		service.reportToken != "p2r_secret" || !bytes.Equal(service.raw, body) {
		t.Fatalf("forwarded match=%q report=%q token=%q raw=%s", service.matchID, service.reportID, service.reportToken, service.raw)
	}
}

func reportTestRouter(service HTTPService, maximum int) http.Handler {
	handler := NewHTTPHandler(service, slog.Default(), maximum)
	router := chi.NewRouter()
	router.Put("/matches/{match_id}/reports/{report_id}", handler.SubmitReport)
	return router
}
