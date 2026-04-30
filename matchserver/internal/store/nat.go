package store

import (
	"net"
	"sync"
	"time"

	"github.com/projectrebound/matchserver/internal/models"
)

type NatBinding struct {
	BindingToken string
	PlayerID     string
	LocalPort    int
	Role         string
	RoomID       string
	PublicIP     string
	PublicPort   int
	ExpiresAt    time.Time
}

type PunchTicket struct {
	TicketID           string
	RoomID             string
	HostPlayerID       string
	ClientPlayerID     string
	HostEndpoint       string
	HostLocalEndpoint  string
	ClientEndpoint     string
	ClientLocalEndpoint string
	Nonce              string
	State              models.PunchTicketState
	ExpiresAt          time.Time
}

type NatTraversalStore struct {
	mu           sync.RWMutex
	bindings     map[string]*NatBinding
	punchTickets map[string]*PunchTicket
}

func NewNatTraversalStore() *NatTraversalStore {
	return &NatTraversalStore{
		bindings:     make(map[string]*NatBinding),
		punchTickets: make(map[string]*PunchTicket),
	}
}

func (s *NatTraversalStore) CreateBinding(playerID string, localPort int, role, roomID string, ttl time.Duration) *NatBinding {
	s.mu.Lock()
	defer s.mu.Unlock()

	binding := &NatBinding{
		BindingToken: NewToken(24),
		PlayerID:     playerID,
		LocalPort:    localPort,
		Role:         normalize(role, 24),
		RoomID:       roomID,
		ExpiresAt:    time.Now().Add(ttl),
	}
	s.bindings[binding.BindingToken] = binding
	return binding
}

func (s *NatTraversalStore) ObserveBinding(token string, addr *net.UDPAddr) *NatBinding {
	s.mu.Lock()
	defer s.mu.Unlock()

	binding, ok := s.bindings[token]
	if !ok || binding.ExpiresAt.Before(time.Now()) {
		return nil
	}

	binding.PublicIP = addr.IP.String()
	binding.PublicPort = addr.Port
	s.bindings[token] = binding
	return binding
}

func (s *NatTraversalStore) GetBinding(token, playerID string) *NatBinding {
	s.mu.RLock()
	defer s.mu.RUnlock()

	binding, ok := s.bindings[token]
	if !ok || binding.ExpiresAt.Before(time.Now()) {
		return nil
	}
	if playerID != "" && binding.PlayerID != playerID {
		return nil
	}
	if binding.PublicIP == "" || binding.PublicPort == 0 {
		return nil
	}
	return binding
}

func (s *NatTraversalStore) CreatePunchTicket(
	roomID, hostPlayerID, clientPlayerID string,
	hostEndpoint, hostLocalEndpoint string,
	clientEndpoint, clientLocalEndpoint string,
	ttl time.Duration,
) *PunchTicket {
	s.mu.Lock()
	defer s.mu.Unlock()

	ticket := &PunchTicket{
		TicketID:            NewToken(16),
		RoomID:              roomID,
		HostPlayerID:        hostPlayerID,
		ClientPlayerID:      clientPlayerID,
		HostEndpoint:        hostEndpoint,
		HostLocalEndpoint:   hostLocalEndpoint,
		ClientEndpoint:      clientEndpoint,
		ClientLocalEndpoint: clientLocalEndpoint,
		Nonce:               NewToken(18),
		State:               models.PunchTicketPending,
		ExpiresAt:           time.Now().Add(ttl),
	}
	s.punchTickets[ticket.TicketID] = ticket
	return ticket
}

func (s *NatTraversalStore) GetHostTickets(roomID, hostPlayerID string) []*PunchTicket {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	var result []*PunchTicket
	for _, ticket := range s.punchTickets {
		if ticket.ExpiresAt.Before(now) {
			ticket.State = models.PunchTicketExpired
			continue
		}
		if ticket.RoomID == roomID && ticket.HostPlayerID == hostPlayerID &&
			(ticket.State == models.PunchTicketPending || ticket.State == models.PunchTicketActive) {
			if ticket.State == models.PunchTicketPending {
				ticket.State = models.PunchTicketActive
			}
			result = append(result, ticket)
		}
	}
	return result
}

func (s *NatTraversalStore) CompletePunchTicket(ticketID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if ticket, ok := s.punchTickets[ticketID]; ok {
		ticket.State = models.PunchTicketCompleted
	}
}

func (s *NatTraversalStore) CleanupExpired() {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	for k, v := range s.bindings {
		if v.ExpiresAt.Before(now) {
			delete(s.bindings, k)
		}
	}
	for k, v := range s.punchTickets {
		if v.ExpiresAt.Before(now) {
			delete(s.punchTickets, k)
		}
	}
}

func normalize(s string, maxLen int) string {
	if len(s) > maxLen {
		return s[:maxLen]
	}
	return s
}
