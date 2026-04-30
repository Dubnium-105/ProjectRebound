package store

import (
	"net"
	"sync"
	"time"
)

type RelayClient struct {
	PlayerID  string
	Secret    string
	Endpoint  *net.UDPAddr
	ExpiresAt time.Time
}

type RelaySession struct {
	RoomID       string
	HostPlayerID string
	HostSecret   string
	HostEndpoint *net.UDPAddr
	Clients      map[string]*RelayClient
	ExpiresAt    time.Time
	Compression  string
	QoS          *SessionQoS
}

type RelayRoute struct {
	SessionID   string
	Role        string
	Secret      string
	Compression string
}

type SessionQoS struct {
	PingSent    int64
	PingLost    int64
	AvgRTT      int64
}

type RelayAllocation struct {
	SessionID string
	Role      string
	Secret    string
	ExpiresAt time.Time
}

type RelayRegistration struct {
	SessionID string
	Role      string
	Endpoint  *net.UDPAddr
}

type RelayStore struct {
	mu       sync.RWMutex
	sessions map[string]*RelaySession
	routes   map[string]*RelayRoute
}

func NewRelayStore() *RelayStore {
	return &RelayStore{
		sessions: make(map[string]*RelaySession),
		routes:   make(map[string]*RelayRoute),
	}
}

func (s *RelayStore) CreateHostAllocation(roomID, hostPlayerID string, ttl time.Duration) *RelayAllocation {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	expiresAt := now.Add(ttl)

	session, ok := s.sessions[roomID]
	if !ok {
		session = &RelaySession{
			RoomID:       roomID,
			HostPlayerID: hostPlayerID,
			Clients:      make(map[string]*RelayClient),
			QoS:          &SessionQoS{},
		}
		s.sessions[roomID] = session
	}

	session.HostPlayerID = hostPlayerID
	session.HostSecret = NewToken(24)
	if session.ExpiresAt.Before(expiresAt) {
		session.ExpiresAt = expiresAt
	}

	return &RelayAllocation{
		SessionID: roomID,
		Role:      "host",
		Secret:    session.HostSecret,
		ExpiresAt: session.ExpiresAt,
	}
}

func (s *RelayStore) CreateClientAllocation(roomID, hostPlayerID, clientPlayerID string, ttl time.Duration) *RelayAllocation {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	expiresAt := now.Add(ttl)

	session, ok := s.sessions[roomID]
	if !ok {
		session = &RelaySession{
			RoomID:       roomID,
			HostPlayerID: hostPlayerID,
			HostSecret:   NewToken(24),
			Clients:      make(map[string]*RelayClient),
			QoS:          &SessionQoS{},
		}
		s.sessions[roomID] = session
	}

	session.HostPlayerID = hostPlayerID
	if session.ExpiresAt.Before(expiresAt) {
		session.ExpiresAt = expiresAt
	}

	secret := NewToken(24)
	session.Clients[secret] = &RelayClient{
		PlayerID:  clientPlayerID,
		Secret:    secret,
		ExpiresAt: expiresAt,
	}

	return &RelayAllocation{
		SessionID: roomID,
		Role:      "client",
		Secret:    secret,
		ExpiresAt: expiresAt,
	}
}

func (s *RelayStore) ObserveRegistration(sessionID, role, secret string, addr *net.UDPAddr) *RelayRegistration {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.cleanupExpiredLocked()

	session, ok := s.sessions[sessionID]
	if !ok || session.ExpiresAt.Before(time.Now()) {
		return nil
	}

	role = normalize(role, 24)

	if role == "host" {
		if !FixedTimeEquals(session.HostSecret, secret) {
			return nil
		}
		if session.HostEndpoint != nil {
			delete(s.routes, endpointKey(session.HostEndpoint))
		}
		session.HostEndpoint = addr
		s.routes[endpointKey(addr)] = &RelayRoute{
			SessionID:   sessionID,
			Role:        "host",
			Secret:      secret,
			Compression: session.Compression,
		}
		return &RelayRegistration{SessionID: sessionID, Role: "host", Endpoint: addr}
	}

	if role == "client" {
		client, ok := session.Clients[secret]
		if !ok || client.ExpiresAt.Before(time.Now()) {
			return nil
		}
		if client.Endpoint != nil {
			delete(s.routes, endpointKey(client.Endpoint))
		}
		client.Endpoint = addr
		session.Clients[secret] = client
		s.routes[endpointKey(addr)] = &RelayRoute{
			SessionID:   sessionID,
			Role:        "client",
			Secret:      secret,
			Compression: session.Compression,
		}
		return &RelayRegistration{SessionID: sessionID, Role: "client", Endpoint: addr}
	}

	return nil
}

func (s *RelayStore) GetForwardTargets(source *net.UDPAddr) (targets []*net.UDPAddr, compression string) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	route, ok := s.routes[endpointKey(source)]
	if !ok {
		return nil, ""
	}

	session, ok := s.sessions[route.SessionID]
	if !ok {
		return nil, ""
	}

	if route.Role == "host" {
		for _, client := range session.Clients {
			if client.Endpoint != nil && client.ExpiresAt.After(time.Now()) {
				targets = append(targets, client.Endpoint)
			}
		}
		return targets, route.Compression
	}

	if session.HostEndpoint != nil {
		return []*net.UDPAddr{session.HostEndpoint}, route.Compression
	}
	return nil, ""
}

func (s *RelayStore) CleanupExpired() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupExpiredLocked()
}

func (s *RelayStore) cleanupExpiredLocked() {
	now := time.Now()
	for id, session := range s.sessions {
		if session.ExpiresAt.Before(now) {
			s.removeSessionRoutesLocked(id)
			delete(s.sessions, id)
			continue
		}
		for secret, client := range session.Clients {
			if client.ExpiresAt.Before(now) {
				if client.Endpoint != nil {
					delete(s.routes, endpointKey(client.Endpoint))
				}
				delete(session.Clients, secret)
			}
		}
	}
}

func (s *RelayStore) removeSessionRoutesLocked(sessionID string) {
	for key, route := range s.routes {
		if route.SessionID == sessionID {
			delete(s.routes, key)
		}
	}
}

func (s *RelayStore) UpdateCompression(sessionID, algo string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if session, ok := s.sessions[sessionID]; ok {
		session.Compression = algo
		for _, route := range s.routes {
			if route.SessionID == sessionID {
				route.Compression = algo
			}
		}
	}
}

func endpointKey(addr *net.UDPAddr) string {
	return addr.String()
}
