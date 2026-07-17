package auth

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
)

type stubHTTPService struct {
	bindCalled bool
}

func (s *stubHTTPService) Bind(context.Context, BindInput, RequestMeta) (BindResult, error) {
	s.bindCalled = true
	return BindResult{}, nil
}

func (s *stubHTTPService) Refresh(context.Context, string, RequestMeta) (RefreshResult, error) {
	return RefreshResult{}, nil
}

func (s *stubHTTPService) Logout(context.Context, string) error { return nil }

func (s *stubHTTPService) AuditBindDecodeFailure(context.Context, RequestMeta) {}

func TestBindRejectsClientSuppliedPrivilegeFields(t *testing.T) {
	service := &stubHTTPService{}
	handler := NewHTTPHandler(service, slog.New(slog.NewTextHandler(io.Discard, nil)), false)
	req := httptest.NewRequest("POST", "/v1/auth/bind", strings.NewReader(`{
		"steam_id":"76561198950613585",
		"persona_name":"STanJK",
		"is_vip":true
	}`))
	recorder := httptest.NewRecorder()
	handler.Bind(recorder, req)
	if recorder.Code != 400 || service.bindCalled {
		t.Fatalf("status=%d bindCalled=%v body=%s", recorder.Code, service.bindCalled, recorder.Body.String())
	}
}
