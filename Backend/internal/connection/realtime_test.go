package connection

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/projectrebound/matchserver/internal/auth"
	"github.com/projectrebound/matchserver/internal/config"
	appmiddleware "github.com/projectrebound/matchserver/internal/middleware"
	"github.com/projectrebound/matchserver/internal/player"
)

type realtimeServiceStub struct {
	incoming chan IncomingEvent
}

func (s *realtimeServiceStub) HandleRealtime(_ context.Context, _ Actor, event IncomingEvent) error {
	s.incoming <- event
	return nil
}

type accessAuthenticatorStub struct{}

func (accessAuthenticatorStub) AuthenticateAccess(context.Context, string) (auth.Principal, error) {
	return auth.Principal{Player: player.Player{ID: "player_realtime", AccountStatus: player.AccountStatusActive}}, nil
}

func TestRealtimeWebSocketAuthenticatesAndExchangesEvents(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	hub := NewHub(8)
	service := &realtimeServiceStub{incoming: make(chan IncomingEvent, 1)}
	realtime := NewRealtimeHandler(service, hub, config.Defaults.CORS, 16*1024, logger)
	handler := appmiddleware.AccessLog(logger, auth.RequireAccess(accessAuthenticatorStub{}, logger)(auth.RequireActive(http.HandlerFunc(realtime.Connect))))
	server := httptest.NewServer(handler)
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	header := http.Header{"Authorization": []string{"Bearer access-test"}}
	socket, _, err := websocket.Dial(ctx, "ws"+server.URL[len("http"):], &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		t.Fatal(err)
	}
	defer socket.CloseNow()
	outgoing := IncomingEvent{Type: "connection.candidate", Payload: []byte(`{"connection_id":"conn_test"}`)}
	if err := wsjson.Write(ctx, socket, outgoing); err != nil {
		t.Fatal(err)
	}
	select {
	case received := <-service.incoming:
		if received.Type != outgoing.Type {
			t.Fatalf("incoming event = %#v", received)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	hub.Publish([]string{"player_realtime"}, Event{Type: "connection.path_selected", Payload: map[string]any{"connection_id": "conn_test"}})
	var received Event
	if err := wsjson.Read(ctx, socket, &received); err != nil {
		t.Fatal(err)
	}
	if received.Type != "connection.path_selected" {
		t.Fatalf("published event = %#v", received)
	}
}

func TestRealtimeWebSocketRejectsMissingAccessToken(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	hub := NewHub(1)
	realtime := NewRealtimeHandler(&realtimeServiceStub{incoming: make(chan IncomingEvent, 1)}, hub, config.Defaults.CORS, 1024, logger)
	server := httptest.NewServer(auth.RequireAccess(accessAuthenticatorStub{}, logger)(http.HandlerFunc(realtime.Connect)))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	socket, response, err := websocket.Dial(ctx, "ws"+server.URL[len("http"):], nil)
	if socket != nil {
		_ = socket.CloseNow()
	}
	if err == nil || response == nil || response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("dial error = %v, response = %#v", err, response)
	}
}
