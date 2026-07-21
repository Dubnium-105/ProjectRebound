package connection

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/projectrebound/matchserver/internal/config"
)

type RealtimeService interface {
	HandleRealtime(context.Context, Actor, IncomingEvent) error
}

type RealtimeHandler struct {
	service        RealtimeService
	hub            *Hub
	originPatterns []string
	maxMessageSize int64
	logger         *slog.Logger
	metrics        interface{ WebSocketConnected(string) func() }
}

func (h *RealtimeHandler) SetMetrics(metrics interface{ WebSocketConnected(string) func() }) {
	h.metrics = metrics
}

func NewRealtimeHandler(service RealtimeService, hub *Hub, cors config.CORSConfig, maxMessageSize int, logger *slog.Logger) *RealtimeHandler {
	return &RealtimeHandler{
		service: service, hub: hub, originPatterns: websocketOriginPatterns(cors.AllowedOrigins),
		maxMessageSize: int64(maxMessageSize), logger: logger,
	}
}

func (h *RealtimeHandler) Connect(w http.ResponseWriter, r *http.Request) {
	actor := actorFromRequest(r)
	socket, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns:  h.originPatterns,
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		h.logger.WarnContext(r.Context(), "realtime websocket handshake rejected", "error", err)
		return
	}
	defer socket.CloseNow()
	if h.metrics != nil {
		disconnected := h.metrics.WebSocketConnected(actor.PlayerID)
		defer disconnected()
	}
	socket.SetReadLimit(h.maxMessageSize)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-h.hub.Done():
			cancel()
		case <-ctx.Done():
		}
	}()
	subscription := h.hub.Subscribe(actor.PlayerID)
	defer subscription.Close()
	localEvents := make(chan Event, 8)
	go h.writeEvents(ctx, cancel, socket, subscription, localEvents)

	for {
		var incoming IncomingEvent
		if err := wsjson.Read(ctx, socket, &incoming); err != nil {
			break
		}
		if err := h.service.HandleRealtime(ctx, actor, incoming); err != nil {
			status, code, message, details := errorDetails(err)
			if status >= 500 {
				h.logger.Error("realtime event failed", "player_id", actor.PlayerID, "code", code, "error", err)
			}
			select {
			case localEvents <- Event{Type: "error", Payload: map[string]any{
				"code": code, "message": message, "details": details,
			}, CreatedAt: time.Now().UTC()}:
			case <-ctx.Done():
				break
			}
		}
	}
	_ = socket.Close(websocket.StatusNormalClosure, "")
}

func (h *RealtimeHandler) writeEvents(
	ctx context.Context,
	cancel context.CancelFunc,
	socket *websocket.Conn,
	subscription *Subscription,
	localEvents <-chan Event,
) {
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case event := <-subscription.Events():
			if err := wsjson.Write(ctx, socket, event); err != nil {
				return
			}
		case event := <-localEvents:
			if err := wsjson.Write(ctx, socket, event); err != nil {
				return
			}
		}
	}
}

func websocketOriginPatterns(origins []string) []string {
	patterns := make([]string, 0, len(origins))
	for _, origin := range origins {
		origin = strings.TrimSpace(origin)
		if origin == "*" {
			patterns = append(patterns, "*")
			continue
		}
		parsed, err := url.Parse(origin)
		if err == nil && parsed.Host != "" {
			patterns = append(patterns, parsed.Host)
		}
	}
	return patterns
}
