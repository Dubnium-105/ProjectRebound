package admin

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/requestctx"
	"github.com/go-chi/chi/v5"
)

type forceAbortHTTPServiceStub struct {
	called bool
	meta   RequestMeta
	err    error
}

func (s *forceAbortHTTPServiceStub) ForceAbort(_ context.Context, attemptID, failureCode, reason string, meta RequestMeta) (any, error) {
	if s.err != nil {
		return nil, s.err
	}
	s.called = attemptID == "attempt-1" && failureCode == "OPERATOR_ABORT" && reason == "test reason"
	s.meta = meta
	return map[string]string{"attempt_id": attemptID}, nil
}

type forceAbortStepUpStub struct{}

func (forceAbortStepUpStub) AuthenticateStepUp(context.Context, string, *Principal) error { return nil }

type rejectingForceAbortStepUpStub struct{}

func (rejectingForceAbortStepUpStub) AuthenticateStepUp(context.Context, string, *Principal) error {
	return &ServiceError{Status: http.StatusForbidden, Code: "ADMIN_STEP_UP_INVALID", Message: "invalid step-up proof."}
}

type safeForceAbortError struct {
	status  int
	code    string
	message string
}

func (e safeForceAbortError) Error() string                { return e.code }
func (e safeForceAbortError) StatusCode() int              { return e.status }
func (e safeForceAbortError) ErrorCode() string            { return e.code }
func (e safeForceAbortError) ErrorMessage() string         { return e.message }
func (e safeForceAbortError) ErrorDetails() map[string]any { return nil }

func forceAbortTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestMatchAttemptForceAbortRequiresPermissionAndStepUp(t *testing.T) {
	stub := &forceAbortHTTPServiceStub{}
	handler := NewMatchAttemptHTTPHandler(stub, forceAbortTestLogger(), false)
	router := chi.NewRouter()
	router.With(
		RequirePermission("rooms.close"),
		RequireStepUp(forceAbortStepUpStub{}),
	).Post("/match-attempts/{attempt_id}/force-abort", handler.ForceAbort)
	request := httptest.NewRequest(http.MethodPost, "/match-attempts/attempt-1/force-abort", strings.NewReader(`{"failure_code":"OPERATOR_ABORT","reason":"test reason"}`))
	request = request.WithContext(context.WithValue(requestctx.WithRequestID(request.Context(), "request-1"), adminPrincipalKey, &Principal{
		AdminID: "admin-1", Permissions: []string{"rooms.close"},
	}))
	request.Header.Set("X-Admin-Step-Up", "step-up-proof")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !stub.called {
		t.Fatalf("authorized force-abort request failed: status=%d body=%s called=%v", response.Code, response.Body.String(), stub.called)
	}
	if stub.meta.AdminID != "admin-1" || stub.meta.RequestID != "request-1" {
		t.Fatalf("request metadata was not propagated: %+v", stub.meta)
	}
}

func TestMatchAttemptForceAbortRejectsMissingAuthPermissionAndStepUp(t *testing.T) {
	cases := []struct {
		name        string
		principal   *Principal
		stepUp      string
		stepService StepUpAuthenticator
		wantStatus  int
	}{
		{name: "missing principal", wantStatus: http.StatusUnauthorized},
		{name: "missing permission", principal: &Principal{AdminID: "admin-1"}, stepUp: "proof", stepService: forceAbortStepUpStub{}, wantStatus: http.StatusForbidden},
		{name: "missing step up", principal: &Principal{AdminID: "admin-1", Permissions: []string{"rooms.close"}}, wantStatus: http.StatusForbidden},
		{name: "invalid step up", principal: &Principal{AdminID: "admin-1", Permissions: []string{"rooms.close"}}, stepUp: "proof", stepService: rejectingForceAbortStepUpStub{}, wantStatus: http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := &forceAbortHTTPServiceStub{}
			handler := NewMatchAttemptHTTPHandler(stub, forceAbortTestLogger(), false)
			router := chi.NewRouter()
			if tc.stepService == nil {
				tc.stepService = forceAbortStepUpStub{}
			}
			router.With(
				RequirePermission("rooms.close"),
				RequireStepUp(tc.stepService),
			).Post("/match-attempts/{attempt_id}/force-abort", handler.ForceAbort)
			request := httptest.NewRequest(http.MethodPost, "/match-attempts/attempt-1/force-abort", strings.NewReader(`{"failure_code":"OPERATOR_ABORT","reason":"test reason"}`))
			if tc.principal != nil {
				request = request.WithContext(context.WithValue(request.Context(), adminPrincipalKey, tc.principal))
			}
			if tc.stepUp != "" {
				request.Header.Set("X-Admin-Step-Up", tc.stepUp)
			}
			response := httptest.NewRecorder()
			if tc.principal == nil {
				handler.ForceAbort(response, request)
			} else {
				router.ServeHTTP(response, request)
			}
			if response.Code != tc.wantStatus || stub.called {
				t.Fatalf("unauthorized force-abort status=%d want=%d called=%v body=%s", response.Code, tc.wantStatus, stub.called, response.Body.String())
			}
		})
	}
}

func TestMatchAttemptForceAbortPreservesServiceHTTPStatus(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		code    string
		message string
	}{
		{name: "missing attempt", status: http.StatusNotFound, code: "MATCH_ATTEMPT_NOT_FOUND", message: "The match attempt was not found."},
		{name: "wrong state", status: http.StatusConflict, code: "MATCH_ATTEMPT_NOT_ACTIVE", message: "The match attempt cannot be aborted from its current state."},
		{name: "missing reason", status: http.StatusBadRequest, code: "INVALID_REQUEST", message: "A non-empty operator reason is required."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := &forceAbortHTTPServiceStub{err: safeForceAbortError{status: tc.status, code: tc.code, message: tc.message}}
			handler := NewMatchAttemptHTTPHandler(stub, forceAbortTestLogger(), false)
			request := httptest.NewRequest(http.MethodPost, "/match-attempts/attempt-1/force-abort", strings.NewReader(`{"failure_code":"OPERATOR_ABORT","reason":"test reason"}`))
			request = request.WithContext(context.WithValue(request.Context(), adminPrincipalKey, &Principal{AdminID: "admin-1"}))
			response := httptest.NewRecorder()
			handler.ForceAbort(response, request)
			if response.Code != tc.status || !strings.Contains(response.Body.String(), tc.code) {
				t.Fatalf("service error status=%d body=%s want status=%d code=%s", response.Code, response.Body.String(), tc.status, tc.code)
			}
		})
	}
}
