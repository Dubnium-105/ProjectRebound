package admin

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/gameserver"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/gameserverregistration"
)

type registrationOnlineHTTPStub struct {
	OnlineHTTPService
	input  GameServerRegistrationInput
	meta   RequestMeta
	result GameServerRegistrationResult
}

func (s *registrationOnlineHTTPStub) CreateGameServerRegistration(
	_ context.Context,
	input GameServerRegistrationInput,
	meta RequestMeta,
) (GameServerRegistrationResult, error) {
	s.input = input
	s.meta = meta
	return s.result, nil
}

func TestAdministrativeGameServerResponseDoesNotExposeTokenHash(t *testing.T) {
	encoded, err := json.Marshal(administrativeGameServer(gameserver.Server{
		ID: "gs_test", ServerTokenHash: []byte("credential-hash"),
	}))
	if err != nil {
		t.Fatal(err)
	}
	body := string(encoded)
	if strings.Contains(body, "credential-hash") ||
		strings.Contains(body, "token_hash") ||
		strings.Contains(body, "registration_issuer") {
		t.Fatalf("administrator game-server response exposed registration credentials: %s", body)
	}
}

func TestCreateGameServerRegistrationReturnsPlaintextOnceAndDisablesCaching(t *testing.T) {
	const plaintext = "gsr_abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-"
	expiresAt := time.Now().UTC().Add(time.Hour)
	service := &registrationOnlineHTTPStub{result: GameServerRegistrationResult{
		Credential: gameserverregistration.Credential{
			ID:         "gsrt_0123456789abcdef0123456789abcdef",
			InstanceID: "hk-prod-001", ExpiresAt: expiresAt,
		},
		Plaintext: plaintext,
	}}
	handler := NewOnlineHTTPHandler(
		service, slog.New(slog.NewTextHandler(io.Discard, nil)), false,
	)
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/admin/game-servers/registration-tokens",
		strings.NewReader(`{"instance_id":"hk-prod-001","expires_in_hours":24,"reason":"Provision OPS-4812"}`),
	)
	request = request.WithContext(context.WithValue(request.Context(), adminPrincipalKey, &Principal{AdminID: "adm_test"}))
	recorder := httptest.NewRecorder()
	handler.CreateGameServerRegistration(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status/body = %d %s", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Cache-Control") != "no-store" || recorder.Header().Get("Pragma") != "no-cache" {
		t.Fatalf("cache headers = %#v", recorder.Header())
	}
	body := recorder.Body.String()
	if strings.Count(body, plaintext) != 1 || strings.Contains(body, "token_hash") {
		t.Fatalf("unexpected credential response: %s", body)
	}
	if service.input.InstanceID != "hk-prod-001" || service.input.ExpiresInHours != 24 || service.meta.AdminID != "adm_test" {
		t.Fatalf("service input/meta = %#v %#v", service.input, service.meta)
	}
}
