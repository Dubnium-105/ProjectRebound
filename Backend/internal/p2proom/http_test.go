package p2proom

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type stubHTTPService struct {
	room   Room
	vntErr error
}

func (s *stubHTTPService) Create(context.Context, Actor, CreateInput) (CreateResult, error) {
	return CreateResult{Room: s.room, HostToken: "p2h_secret", HeartbeatInterval: 15}, nil
}
func (s *stubHTTPService) Get(context.Context, string) (Room, error) { return s.room, nil }
func (s *stubHTTPService) List(context.Context, ListFilter) (ListResult, error) {
	return ListResult{Items: []Room{s.room}}, nil
}
func (s *stubHTTPService) Join(context.Context, Actor, string, string) (Room, error) {
	return s.room, nil
}
func (s *stubHTTPService) Leave(context.Context, Actor, string) (Room, error) {
	return s.room, nil
}
func (s *stubHTTPService) Heartbeat(context.Context, Actor, string, string) (Room, error) {
	return s.room, nil
}
func (s *stubHTTPService) Start(context.Context, Actor, string, string) (Room, error) {
	return s.room, nil
}
func (s *stubHTTPService) Delete(context.Context, Actor, string, string) (Room, error) {
	return s.room, nil
}
func (s *stubHTTPService) HeartbeatInterval() int { return 15 }
func (s *stubHTTPService) VNTBootstrap(context.Context, Actor, string) (VNTBootstrap, error) {
	return VNTBootstrap{}, s.vntErr
}
func (s *stubHTTPService) UpdateVNTPresence(context.Context, Actor, string, VNTPresenceInput) (Room, error) {
	return s.room, s.vntErr
}
func (s *stubHTTPService) VNTHostReady(context.Context, Actor, string, string, int, string) (Room, error) {
	return s.room, s.vntErr
}
func (s *stubHTTPService) VNTRebind(context.Context, Actor, string, string, string) (Room, error) {
	return s.room, s.vntErr
}

func TestPublicRoomResponseDoesNotLeakCredentialsOrNetworkMetadata(t *testing.T) {
	service := &stubHTTPService{room: Room{
		ID: "room_test", HostPlayerID: "player_host", HostTokenHash: []byte("secret-host-token-hash"),
		DisplayName: "Public", Region: "hk", Mode: "coop", Version: "1.0.0",
		MaxPlayers: 4, PlayerCount: 1, State: StateLobby, LastHeartbeatAt: time.Now(), CreatedAt: time.Now(),
	}}
	handler := NewHTTPHandler(service, slog.New(slog.NewTextHandler(io.Discard, nil)))
	recorder := httptest.NewRecorder()
	handler.Get(recorder, httptest.NewRequest("GET", "/v1/p2p-rooms/room_test", nil))
	body := recorder.Body.String()
	for _, forbidden := range []string{"secret-host-token-hash", "host_token", "relay_token", "candidate", "public_ip", "nat_type"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("public response leaked %q: %s", forbidden, body)
		}
	}
}

func TestListRejectsInvalidHasSlots(t *testing.T) {
	handler := NewHTTPHandler(&stubHTTPService{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	recorder := httptest.NewRecorder()
	handler.List(recorder, httptest.NewRequest("GET", "/v1/p2p-rooms?has_slots=sometimes", nil))
	if recorder.Code != 400 || !strings.Contains(recorder.Body.String(), "INVALID_REQUEST") {
		t.Fatalf("response = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestVNTBootstrapRateLimitSetsRetryAfter(t *testing.T) {
	service := &stubHTTPService{vntErr: &ServiceError{
		Status: 429, Code: "VNT_RATE_LIMITED", Message: "Too many VNT requests.",
		Details: map[string]any{"retry_after_seconds": 7},
	}}
	handler := NewHTTPHandler(service, slog.New(slog.NewTextHandler(io.Discard, nil)))
	recorder := httptest.NewRecorder()
	handler.VNTBootstrap(recorder, httptest.NewRequest("POST", "/v1/p2p-rooms/room_test/vnt/bootstrap", nil))
	if recorder.Code != 429 || recorder.Header().Get("Retry-After") != "7" {
		t.Fatalf("response = %d, Retry-After = %q", recorder.Code, recorder.Header().Get("Retry-After"))
	}
}
