package diagnostic

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/auth"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/player"
)

type fakeReportStore struct {
	playerID string
	report   string
	err      error
	calls    int
}

func (f *fakeReportStore) Store(_ context.Context, playerID, report string) error {
	f.calls++
	f.playerID = playerID
	f.report = report
	return f.err
}

type stubAccessAuthenticator struct {
	principal auth.Principal
}

func (s stubAccessAuthenticator) AuthenticateAccess(context.Context, string) (auth.Principal, error) {
	return s.principal, nil
}

func TestSubmitStoresReportWithoutChangingContent(t *testing.T) {
	store := &fakeReportStore{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := auth.RequireAccess(stubAccessAuthenticator{principal: auth.Principal{
		Player: player.Player{ID: "p_test"},
	}}, logger)(http.HandlerFunc(NewHTTPHandler(store, logger).Submit))
	report := "=== Alpha Test — 2026-08-01 ===\nStatus: untrusted root\n"
	body, err := json.Marshal(map[string]string{"report": report})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/diagnostic/report", strings.NewReader(string(body)))
	request.Header.Set("Authorization", "Bearer test")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if store.calls != 1 || store.playerID != "p_test" || store.report != report {
		t.Fatalf("stored calls=%d player_id=%q report=%q", store.calls, store.playerID, store.report)
	}
	var result map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if ok, exists := result["ok"]; !exists || ok != true || len(result) != 1 {
		t.Fatalf("response = %#v", result)
	}
}

func TestSubmitRequiresAccessToken(t *testing.T) {
	store := &fakeReportStore{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := auth.RequireAccess(stubAccessAuthenticator{}, logger)(http.HandlerFunc(NewHTTPHandler(store, logger).Submit))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/diagnostic/report", strings.NewReader(`{"report":"text"}`)))

	if response.Code != http.StatusUnauthorized || store.calls != 0 {
		t.Fatalf("status = %d, calls = %d", response.Code, store.calls)
	}
}

func TestSubmitRejectsMissingReportField(t *testing.T) {
	store := &fakeReportStore{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := auth.RequireAccess(stubAccessAuthenticator{principal: auth.Principal{
		Player: player.Player{ID: "p_test"},
	}}, logger)(http.HandlerFunc(NewHTTPHandler(store, logger).Submit))
	request := httptest.NewRequest(http.MethodPost, "/v1/diagnostic/report", strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer test")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest || store.calls != 0 {
		t.Fatalf("status = %d, calls = %d, body = %s", response.Code, store.calls, response.Body.String())
	}
}

func TestSubmitDoesNotValidateReportContent(t *testing.T) {
	store := &fakeReportStore{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := auth.RequireAccess(stubAccessAuthenticator{principal: auth.Principal{
		Player: player.Player{ID: "p_test"},
	}}, logger)(http.HandlerFunc(NewHTTPHandler(store, logger).Submit))
	request := httptest.NewRequest(http.MethodPost, "/v1/diagnostic/report", strings.NewReader(`{"report":""}`))
	request.Header.Set("Authorization", "Bearer test")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK || store.calls != 1 || store.report != "" {
		t.Fatalf("status = %d, calls = %d, report = %q", response.Code, store.calls, store.report)
	}
}

func TestSubmitReportsStorageFailure(t *testing.T) {
	store := &fakeReportStore{err: errors.New("database unavailable")}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := auth.RequireAccess(stubAccessAuthenticator{principal: auth.Principal{
		Player: player.Player{ID: "p_test"},
	}}, logger)(http.HandlerFunc(NewHTTPHandler(store, logger).Submit))
	request := httptest.NewRequest(http.MethodPost, "/v1/diagnostic/report", strings.NewReader(`{"report":"text"}`))
	request.Header.Set("Authorization", "Bearer test")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError || store.calls != 1 {
		t.Fatalf("status = %d, calls = %d, body = %s", response.Code, store.calls, response.Body.String())
	}
}
