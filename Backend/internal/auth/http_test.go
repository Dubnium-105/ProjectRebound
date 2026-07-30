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
	bindInput  BindInput
}

func (s *stubHTTPService) Bind(_ context.Context, input BindInput, _ RequestMeta) (BindResult, error) {
	s.bindCalled = true
	s.bindInput = input
	return BindResult{}, nil
}

func TestBindAcceptsOptionalDeviceAndInviteFields(t *testing.T) {
	service := &stubHTTPService{}
	handler := NewHTTPHandler(service, slog.New(slog.NewTextHandler(io.Discard, nil)), false)
	req := httptest.NewRequest("POST", "/v1/auth/bind", strings.NewReader(`{
		"steam_id":"76561198950613585",
		"persona_name":"STanJK",
		"device_id":"installation-1234",
		"invite_code":"TEST-ABCD-EFGH",
		"encrypted_ticket":"012345abcdef"
	}`))
	recorder := httptest.NewRecorder()
	handler.Bind(recorder, req)
	if recorder.Code != 200 || !service.bindCalled {
		t.Fatalf("status=%d bindCalled=%v body=%s", recorder.Code, service.bindCalled, recorder.Body.String())
	}
	if service.bindInput.DeviceID != "installation-1234" ||
		service.bindInput.InviteCode != "TEST-ABCD-EFGH" ||
		service.bindInput.EncryptedTicket != "012345abcdef" {
		t.Fatalf("bind input = %#v", service.bindInput)
	}
}

func (s *stubHTTPService) Refresh(context.Context, string, RequestMeta) (RefreshResult, error) {
	return RefreshResult{}, nil
}

func (s *stubHTTPService) Logout(context.Context, string) error { return nil }

func (s *stubHTTPService) ListUserSessions(context.Context, string, string) ([]UserSession, error) {
	return nil, nil
}

func (s *stubHTTPService) RevokeUserSession(context.Context, string, string) error { return nil }

func (s *stubHTTPService) RevokeOtherUserSessions(context.Context, string, string) (int64, error) {
	return 0, nil
}

func (s *stubHTTPService) ListRiskEvents(context.Context, string, string, string, string, bool, int) (RiskEventList, error) {
	return RiskEventList{}, nil
}

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
