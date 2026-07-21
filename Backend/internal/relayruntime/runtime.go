package relayruntime

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"net"
	"net/netip"
	"sync"
	"time"
)

type RuntimeEvent struct {
	Type         string
	AllocationID string
}

type OutboundDatagram struct {
	Address netip.AddrPort
	Packet  []byte
}

type boundEndpoint struct {
	address      netip.AddrPort
	tokenID      string
	version      byte
	mtu          uint16
	key          []byte
	expiresAt    time.Time
	packetBucket tokenBucket
	byteBucket   tokenBucket
	replay       replayWindow
}

type relayAllocation struct {
	id              string
	connectionID    string
	roomID          string
	handle          uint64
	host            *boundEndpoint
	peer            *boundEndpoint
	maxTotalBytes   int64
	totalBytes      int64
	expiresAt       time.Time
	lastActivity    time.Time
	opened          bool
	protocolVersion byte
	mtu             uint16
}

type replayBinding struct {
	allocationID string
	role         EndpointRole
	address      netip.AddrPort
	expiresAt    time.Time
}

type Runtime struct {
	mu             sync.Mutex
	nodeID         string
	config         Config
	verifier       *TokenVerifier
	cookies        *CookieManager
	metrics        *Metrics
	allocations    map[string]*relayAllocation
	byHandle       map[uint64]*relayAllocation
	usedTokens     map[string]replayBinding
	ipBuckets      map[netip.Addr]*tokenBucket
	nodeByteBucket tokenBucket
	intervalBytes  int64
	draining       bool
	now            func() time.Time
	events         chan RuntimeEvent
	shutdown       chan struct{}
	shutdownOnce   sync.Once
}

func NewRuntime(nodeID string, cfg Config, verifier *TokenVerifier, metrics *Metrics) (*Runtime, error) {
	if nodeID == "" || verifier == nil {
		return nil, errors.New("relay node identity and token verifier are required")
	}
	cookies, err := NewCookieManager(cfg.CookieTTL())
	if err != nil {
		return nil, err
	}
	if metrics == nil {
		metrics = NewMetrics()
	}
	now := time.Now().UTC()
	return &Runtime{
		nodeID: nodeID, config: cfg, verifier: verifier, cookies: cookies, metrics: metrics,
		allocations: make(map[string]*relayAllocation), byHandle: make(map[uint64]*relayAllocation),
		usedTokens: make(map[string]replayBinding), ipBuckets: make(map[netip.Addr]*tokenBucket),
		nodeByteBucket: newTokenBucket(float64(cfg.MaxEgressBPS)/8, float64(cfg.MaxEgressBPS)/8, now),
		now:            time.Now, events: make(chan RuntimeEvent, 256), shutdown: make(chan struct{}),
	}, nil
}

func (r *Runtime) Events() <-chan RuntimeEvent      { return r.events }
func (r *Runtime) Metrics() *Metrics                { return r.metrics }
func (r *Runtime) UpdateKeyset(keyset Keyset) error { return r.verifier.Update(keyset) }

func (r *Runtime) SetDraining(value bool) {
	r.mu.Lock()
	r.draining = value
	if value && len(r.allocations) == 0 {
		r.emit(RuntimeEvent{Type: "DrainCompleted"})
	}
	r.mu.Unlock()
}

func (r *Runtime) Snapshot() (active int, egressBPS int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	egressBPS = r.intervalBytes * 8 / int64(r.config.HeartbeatSeconds)
	r.intervalBytes = 0
	return len(r.allocations), egressBPS
}

func (r *Runtime) RequestShutdown()                   { r.shutdownOnce.Do(func() { close(r.shutdown) }) }
func (r *Runtime) ShutdownRequested() <-chan struct{} { return r.shutdown }

func (r *Runtime) Process(packet []byte, address netip.AddrPort) []OutboundDatagram {
	r.metrics.packetsReceived.Add(1)
	if len(packet) < 6 || len(packet) > r.config.MaxDatagramBytes || string(packet[:4]) != Magic || !r.acceptsVersion(packet[4]) {
		r.drop(false)
		return nil
	}
	now := r.now().UTC()
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.allowIP(address.Addr(), now) {
		r.drop(true)
		return nil
	}
	switch packet[5] {
	case messageBind:
		var response []byte
		if packet[4] == ProtocolVersionV1 {
			_, token, err := decodeV1TokenPacket(packet, messageBind, 0)
			if err != nil {
				r.bindFailure(false)
				return nil
			}
			response = encodeChallengeV1(r.cookies.Issue(address, token, now))
		} else {
			init, err := decodeBindInitV2(packet)
			if err != nil || init.RequestedMTU < 1000 || int(init.RequestedMTU) > r.config.MaxPayloadBytes {
				r.bindFailure(false)
				return nil
			}
			serverNonce := make([]byte, NonceSize)
			if _, err := rand.Read(serverNonce); err != nil {
				r.bindFailure(false)
				return nil
			}
			cookie := r.cookies.IssueV2(address, init.ClientNonce, serverNonce, init.Token, init.RequestedMTU, now)
			response = encodeChallengeV2(serverNonce, cookie, r.cookies.TTL())
		}
		if len(response) > len(packet) {
			r.bindFailure(false)
			return nil
		}
		return []OutboundDatagram{{Address: address, Packet: response}}
	case messageBindProof:
		if packet[4] == ProtocolVersionV1 {
			cookie, tokenBytes, err := decodeV1TokenPacket(packet, messageBindProof, sha256Size)
			if err != nil || !r.cookies.Verify(cookie, address, tokenBytes, now) {
				r.bindFailure(false)
				return nil
			}
			return r.bind(string(tokenBytes), address, now, ProtocolVersionV1, uint16(r.config.MaxPayloadBytes))
		}
		proof, err := decodeBindProofV2(packet)
		if err != nil || proof.RequestedMTU < 1000 || int(proof.RequestedMTU) > r.config.MaxPayloadBytes ||
			!r.cookies.VerifyV2(proof.Cookie, address, proof.ClientNonce, proof.ServerNonce, proof.Token, proof.RequestedMTU, now) {
			r.bindFailure(false)
			return nil
		}
		return r.bind(string(proof.Token), address, now, ProtocolVersion, proof.RequestedMTU)
	case messageData:
		return r.forward(packet, address, now)
	default:
		r.drop(false)
		return nil
	}
}

const sha256Size = 32

func (r *Runtime) bind(token string, address netip.AddrPort, now time.Time, version byte, requestedMTU uint16) []OutboundDatagram {
	claims, err := r.verifier.Verify(token, r.nodeID, now)
	if err != nil {
		r.bindFailure(true)
		return nil
	}
	role, _ := parseRole(claims.EndpointRole)
	if previous, exists := r.usedTokens[claims.TokenID]; exists {
		item := r.allocations[previous.allocationID]
		if item != nil && item.protocolVersion == version && previous.allocationID == claims.AllocationID && previous.role == role && previous.address == address {
			r.metrics.bindSuccess.Add(1)
			return []OutboundDatagram{{Address: address, Packet: encodeBindOKVersion(version, item.handle, role, item.mtu)}}
		}
		r.bindFailure(true)
		return nil
	}
	allocation := r.allocations[claims.AllocationID]
	if allocation == nil {
		if r.draining || len(r.allocations) >= r.config.MaxAllocations {
			r.bindFailure(false)
			return nil
		}
		handle, err := r.newHandle()
		if err != nil {
			r.bindFailure(false)
			return nil
		}
		allocation = &relayAllocation{
			id: claims.AllocationID, connectionID: claims.ConnectionID, roomID: claims.RoomID,
			handle: handle, maxTotalBytes: claims.MaxTotalBytes,
			expiresAt: time.Unix(claims.AllocationExpiresAt, 0).UTC(), lastActivity: now,
			protocolVersion: version, mtu: requestedMTU,
		}
		r.allocations[allocation.id] = allocation
		r.byHandle[handle] = allocation
		r.metrics.activeAllocations.Add(1)
	} else if allocation.connectionID != claims.ConnectionID || allocation.roomID != claims.RoomID ||
		allocation.maxTotalBytes != claims.MaxTotalBytes || allocation.protocolVersion != version {
		r.bindFailure(true)
		return nil
	}
	claimExpiry := time.Unix(claims.AllocationExpiresAt, 0).UTC()
	if claimExpiry.Before(allocation.expiresAt) {
		allocation.expiresAt = claimExpiry
	}
	endpoint := &boundEndpoint{
		address: address, tokenID: claims.TokenID, version: version, mtu: requestedMTU,
		key:          deriveDataKeyVersion(token, version),
		expiresAt:    time.Unix(claims.AllocationExpiresAt, 0).UTC(),
		packetBucket: newTokenBucket(float64(claims.MaxPPS), float64(claims.MaxPPS), now),
		byteBucket:   newTokenBucket(float64(claims.MaxBPS)/8, float64(claims.MaxBPS)/8, now),
	}
	if requestedMTU < allocation.mtu {
		allocation.mtu = requestedMTU
	}
	if role == RoleHost {
		if allocation.host != nil {
			r.bindFailure(true)
			return nil
		}
		allocation.host = endpoint
	} else {
		if allocation.peer != nil {
			r.bindFailure(true)
			return nil
		}
		allocation.peer = endpoint
	}
	r.usedTokens[claims.TokenID] = replayBinding{
		allocationID: allocation.id, role: role, address: address, expiresAt: endpoint.expiresAt,
	}
	allocation.lastActivity = now
	if allocation.host != nil && allocation.peer != nil && !allocation.opened {
		allocation.opened = true
		r.emit(RuntimeEvent{Type: "AllocationOpened", AllocationID: allocation.id})
	}
	r.metrics.bindSuccess.Add(1)
	return []OutboundDatagram{{Address: address, Packet: encodeBindOKVersion(version, allocation.handle, role, allocation.mtu)}}
}

func (r *Runtime) forward(packet []byte, source netip.AddrPort, now time.Time) []OutboundDatagram {
	decoded, err := decodeDataPacket(packet)
	if err != nil {
		r.drop(false)
		return nil
	}
	allocation := r.byHandle[decoded.Handle]
	if allocation == nil || decoded.Version != allocation.protocolVersion || len(decoded.Payload) > int(allocation.mtu) ||
		!now.Before(allocation.expiresAt) || now.Sub(allocation.lastActivity) > r.config.AllocationIdleTTL() {
		r.drop(false)
		return nil
	}
	var sender, recipient *boundEndpoint
	if decoded.Role == RoleHost {
		sender, recipient = allocation.host, allocation.peer
	} else {
		sender, recipient = allocation.peer, allocation.host
	}
	if sender == nil || recipient == nil || sender.address != source || !now.Before(sender.expiresAt) || !now.Before(recipient.expiresAt) ||
		subtle.ConstantTimeCompare(dataAuthenticationTag(sender.key, packet), decoded.Tag) != 1 || !sender.replay.Accept(decoded.Sequence) {
		r.drop(false)
		return nil
	}
	cost := float64(len(decoded.Payload))
	if !sender.packetBucket.Allow(1, now) || !sender.byteBucket.Allow(cost, now) ||
		!r.nodeByteBucket.Allow(cost, now) || allocation.totalBytes+int64(len(decoded.Payload)) > allocation.maxTotalBytes {
		r.drop(true)
		return nil
	}
	allocation.totalBytes += int64(len(decoded.Payload))
	r.intervalBytes += int64(len(decoded.Payload))
	allocation.lastActivity = now
	forwarded := encodeDataPacketVersion(recipient.version, allocation.handle, decoded.Role, decoded.Sequence, recipient.key, decoded.Payload)
	r.metrics.packetsForwarded.Add(1)
	r.metrics.bytesForwarded.Add(uint64(len(decoded.Payload)))
	return []OutboundDatagram{{Address: recipient.address, Packet: forwarded}}
}

func (r *Runtime) acceptsVersion(version byte) bool {
	return version == ProtocolVersion || version == ProtocolVersionV1 && r.config.AcceptProtocolV1
}

func (r *Runtime) RevokeAllocation(allocationID string) { r.closeAllocation(allocationID, true) }

func (r *Runtime) Sweep() int {
	now := r.now().UTC()
	r.mu.Lock()
	defer r.mu.Unlock()
	closed := 0
	for id, allocation := range r.allocations {
		if !now.Before(allocation.expiresAt) || now.Sub(allocation.lastActivity) > r.config.AllocationIdleTTL() {
			r.closeAllocationLocked(id, true)
			closed++
		}
	}
	for tokenID, binding := range r.usedTokens {
		if !now.Before(binding.expiresAt) {
			delete(r.usedTokens, tokenID)
		}
	}
	for address, bucket := range r.ipBuckets {
		if now.Sub(bucket.lastRefill) > 5*time.Minute {
			delete(r.ipBuckets, address)
		}
	}
	return closed
}

func (r *Runtime) closeAllocation(allocationID string, notify bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closeAllocationLocked(allocationID, notify)
}

func (r *Runtime) closeAllocationLocked(allocationID string, notify bool) {
	allocation := r.allocations[allocationID]
	if allocation == nil {
		return
	}
	delete(r.allocations, allocationID)
	delete(r.byHandle, allocation.handle)
	r.metrics.activeAllocations.Add(-1)
	if notify && allocation.opened {
		r.emit(RuntimeEvent{Type: "AllocationClosed", AllocationID: allocation.id})
	}
	if r.draining && len(r.allocations) == 0 {
		r.emit(RuntimeEvent{Type: "DrainCompleted"})
	}
}

func (r *Runtime) allowIP(address netip.Addr, now time.Time) bool {
	bucket := r.ipBuckets[address]
	if bucket == nil {
		created := newTokenBucket(float64(r.config.IPPacketsPerSecond), float64(r.config.IPPacketsPerSecond), now)
		bucket = &created
		r.ipBuckets[address] = bucket
	}
	return bucket.Allow(1, now)
}

func (r *Runtime) newHandle() (uint64, error) {
	for attempts := 0; attempts < 8; attempts++ {
		var encoded [8]byte
		if _, err := rand.Read(encoded[:]); err != nil {
			return 0, err
		}
		handle := binary.BigEndian.Uint64(encoded[:])
		if handle != 0 && r.byHandle[handle] == nil {
			return handle, nil
		}
	}
	return 0, errors.New("failed to allocate a unique relay handle")
}

func (r *Runtime) emit(event RuntimeEvent) {
	select {
	case r.events <- event:
	default:
	}
}

func (r *Runtime) bindFailure(invalidToken bool) {
	r.metrics.bindFailed.Add(1)
	if invalidToken {
		r.metrics.tokenInvalid.Add(1)
	}
}

func (r *Runtime) drop(rateLimited bool) {
	r.metrics.packetsDropped.Add(1)
	if rateLimited {
		r.metrics.rateLimitDrops.Add(1)
	}
}

func (r *Runtime) RunUDP(ctx context.Context) error {
	address, err := net.ResolveUDPAddr("udp", r.config.ListenAddr)
	if err != nil {
		return err
	}
	connection, err := net.ListenUDP("udp", address)
	if err != nil {
		return err
	}
	defer connection.Close()
	return r.ServeUDP(ctx, connection)
}

func (r *Runtime) ServeUDP(ctx context.Context, connection *net.UDPConn) error {
	go func() {
		<-ctx.Done()
		_ = connection.Close()
	}()
	buffer := make([]byte, r.config.MaxDatagramBytes+1)
	for {
		length, remote, err := connection.ReadFromUDPAddrPort(buffer)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		packet := append([]byte(nil), buffer[:length]...)
		for _, output := range r.Process(packet, remote) {
			_, _ = connection.WriteToUDPAddrPort(output.Packet, output.Address)
		}
	}
}
