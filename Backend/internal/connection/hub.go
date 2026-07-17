package connection

import (
	"context"
	"sync"
)

type Subscription struct {
	events   chan Event
	hub      *Hub
	playerID string
	once     sync.Once
}

func (s *Subscription) Events() <-chan Event { return s.events }

func (s *Subscription) Close() {
	s.once.Do(func() { s.hub.unsubscribe(s) })
}

type Hub struct {
	mu          sync.RWMutex
	queueSize   int
	subscribers map[string]map[*Subscription]struct{}
	closed      chan struct{}
	closeOnce   sync.Once
}

func NewHub(queueSize int) *Hub {
	return &Hub{queueSize: queueSize, subscribers: make(map[string]map[*Subscription]struct{}), closed: make(chan struct{})}
}

func (h *Hub) Done() <-chan struct{} { return h.closed }

func (h *Hub) Run(ctx context.Context) {
	<-ctx.Done()
	h.closeOnce.Do(func() { close(h.closed) })
}

func (h *Hub) Subscribe(playerID string) *Subscription {
	subscription := &Subscription{events: make(chan Event, h.queueSize), hub: h, playerID: playerID}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.subscribers[playerID] == nil {
		h.subscribers[playerID] = make(map[*Subscription]struct{})
	}
	h.subscribers[playerID][subscription] = struct{}{}
	return subscription
}

func (h *Hub) Publish(playerIDs []string, event Event) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	seen := make(map[string]struct{}, len(playerIDs))
	for _, playerID := range playerIDs {
		if _, duplicate := seen[playerID]; duplicate {
			continue
		}
		seen[playerID] = struct{}{}
		for subscription := range h.subscribers[playerID] {
			select {
			case subscription.events <- event:
			default:
				// Durable state remains available from GET /v1/connections/{id};
				// a slow realtime client must never block coordination writes.
			}
		}
	}
}

func (h *Hub) unsubscribe(subscription *Subscription) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.subscribers[subscription.playerID], subscription)
	if len(h.subscribers[subscription.playerID]) == 0 {
		delete(h.subscribers, subscription.playerID)
	}
}
