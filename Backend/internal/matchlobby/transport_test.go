package matchlobby

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

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/p2proom"
	"github.com/go-chi/chi/v5"
)

type transportHTTPStub struct {
	HTTPService
	projection TransportProjection
}

func (s transportHTTPStub) Transport(context.Context, Actor, TransportScopeRequest) (TransportProjection, error) {
	return s.projection, nil
}
func (s transportHTTPStub) TransportVNTBootstrap(context.Context, Actor, TransportScopeRequest) (p2proom.VNTBootstrap, error) {
	return p2proom.VNTBootstrap{}, nil
}
func (s transportHTTPStub) TransportVNTPresence(context.Context, Actor, TransportScopeRequest, p2proom.VNTPresenceInput) (TransportRoomProjection, error) {
	return s.projection.Room, nil
}
func (s transportHTTPStub) TransportVNTHostReady(context.Context, Actor, TransportScopeRequest, string, int, string) (TransportRoomProjection, error) {
	return s.projection.Room, nil
}

func transportRequest(t *testing.T, method, path string) *http.Request {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(`{}`))
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("attempt_id", "attempt-1")
	return request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, routeContext))
}

func TestTransportScopeFromRequestRequiresBothPositiveHeaders(t *testing.T) {
	request := transportRequest(t, http.MethodGet, "/v1/match-attempts/attempt-1/transport")
	if _, err := transportScopeFromRequest(request); errorCode(err) != "INVALID_REQUEST" {
		t.Fatalf("missing scope returned %v", err)
	}
	request.Header.Set(transportRosterRevisionHeader, "7")
	if _, err := transportScopeFromRequest(request); errorCode(err) != "INVALID_REQUEST" {
		t.Fatalf("missing route generation returned %v", err)
	}
	request.Header.Set(transportRouteGenerationHeader, "2")
	scope, err := transportScopeFromRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if scope.AttemptID != "attempt-1" || scope.RosterRevision != 7 || scope.RouteGeneration != 2 {
		t.Fatalf("scope = %#v", scope)
	}
}

func TestTransportHTTPIsScopedAndNeverCacheable(t *testing.T) {
	projection := TransportProjection{
		AttemptID: "attempt-1", LobbyID: "lobby-1", RosterRevision: 7, RouteGeneration: 2,
		Room: TransportRoomProjection{RoomID: "room-1", HostPlayerID: "player-host", State: "LOBBY", CreatedAt: time.Now().UTC()},
	}
	handler := NewHTTPHandler(transportHTTPStub{projection: projection}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	request := transportRequest(t, http.MethodGet, "/v1/match-attempts/attempt-1/transport")
	request.Header.Set(transportRosterRevisionHeader, "7")
	request.Header.Set(transportRouteGenerationHeader, "2")
	recorder := httptest.NewRecorder()
	handler.Transport(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Cache-Control") != "no-store" || recorder.Header().Get("Pragma") != "no-cache" {
		t.Fatalf("cache headers = %v", recorder.Header())
	}
	body := recorder.Body.String()
	for _, expected := range []string{`"attempt_id":"attempt-1"`, `"lobby_id":"lobby-1"`, `"roster_revision":7`, `"route_generation":2`, `"room_id":"room-1"`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("response omitted %q: %s", expected, body)
		}
	}
}

func TestEveryAuthoritativeTransportHandlerSetsNoStore(t *testing.T) {
	projection := TransportProjection{
		AttemptID: "attempt-1", LobbyID: "lobby-1", RosterRevision: 7, RouteGeneration: 2,
		Room: TransportRoomProjection{RoomID: "room-1", State: "LOBBY"},
	}
	handler := NewHTTPHandler(transportHTTPStub{projection: projection}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	handlers := map[string]func(http.ResponseWriter, *http.Request){
		"get":        handler.Transport,
		"bootstrap":  handler.TransportVNTBootstrap,
		"presence":   handler.TransportVNTPresence,
		"host-ready": handler.TransportVNTHostReady,
	}
	for name, transportHandler := range handlers {
		t.Run(name, func(t *testing.T) {
			request := transportRequest(t, http.MethodPut, "/v1/match-attempts/attempt-1/transport")
			request.Header.Set(transportRosterRevisionHeader, "7")
			request.Header.Set(transportRouteGenerationHeader, "2")
			request.Header.Set(transportHostTokenHeader, "opaque-host-token")
			recorder := httptest.NewRecorder()
			transportHandler(recorder, request)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
			}
			if recorder.Header().Get("Cache-Control") != "no-store" || recorder.Header().Get("Pragma") != "no-cache" {
				t.Fatalf("cache headers = %v", recorder.Header())
			}
		})
	}
}

func TestTransportRoomProjectionDoesNotExposeRoomCredentials(t *testing.T) {
	room := p2proom.Room{
		ID: "room-1", HostPlayerID: "player-host", HostTokenHash: []byte("hash-secret"),
		HostTokenCiphertext: []byte("cipher-secret"), HostTokenNonce: []byte("nonce-secret"),
		HostTokenKeyID: "key-secret", ManagedLobbyID: "lobby-1",
		TransportKind: p2proom.TransportVNT, State: p2proom.StateConnecting,
		CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Hour),
	}
	encoded, err := json.Marshal(projectTransportRoom(room))
	if err != nil {
		t.Fatal(err)
	}
	payload := string(encoded)
	for _, forbidden := range []string{"hash-secret", "cipher-secret", "nonce-secret", "key-secret", "lobby-1"} {
		if strings.Contains(payload, forbidden) {
			t.Fatalf("transport projection leaked %q: %s", forbidden, payload)
		}
	}
}

func TestTransportDependencyErrorsKeepTheirSafeHTTPCode(t *testing.T) {
	err := mapTransportDependencyError(&p2proom.ServiceError{
		Status: http.StatusConflict, Code: "VNT_GENERATION_STALE", Message: "stale generation",
	})
	status, code, message, _ := errorDetails(err)
	if status != http.StatusConflict || code != "VNT_GENERATION_STALE" || message != "stale generation" {
		t.Fatalf("mapped dependency error = %d %q %q", status, code, message)
	}
}
