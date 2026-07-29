package metaserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/config"
	"google.golang.org/protobuf/encoding/protowire"
)

const (
	rpcUnknownError        int32 = 1
	maxMalformedFrames           = 3
	maxRateStates                = 100_000
	rateStateRetention           = 2 * time.Minute
	rateStatePruneInterval       = time.Minute
)

var ErrWriteQueueFull = errors.New("meta protocol write queue is full")

type connectionRate struct {
	active      int
	windowStart time.Time
	lastSeen    time.Time
	started     int
}

type serialFrameWriter struct {
	connection   net.Conn
	maximumFrame int
	maximumQueue int64
	timeout      time.Duration
	queue        chan []byte
	done         chan error
	pendingBytes atomic.Int64
}

func newSerialFrameWriter(
	connection net.Conn,
	maximumFrame, maximumQueue int,
	timeout time.Duration,
) *serialFrameWriter {
	return &serialFrameWriter{
		connection: connection, maximumFrame: maximumFrame,
		maximumQueue: int64(maximumQueue), timeout: timeout,
		queue: make(chan []byte, 128), done: make(chan error, 1),
	}
}

func (w *serialFrameWriter) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			w.finish(ctx.Err())
			return
		case payload := <-w.queue:
			size := int64(len(payload) + 4)
			if err := w.connection.SetWriteDeadline(time.Now().Add(w.timeout)); err != nil {
				w.pendingBytes.Add(-size)
				w.finish(err)
				return
			}
			if err := WriteFrame(w.connection, payload, w.maximumFrame); err != nil {
				w.pendingBytes.Add(-size)
				w.finish(err)
				return
			}
			w.pendingBytes.Add(-size)
		}
	}
}

func (w *serialFrameWriter) enqueue(payload []byte) error {
	size := int64(len(payload) + 4)
	if size > w.maximumQueue {
		return ErrWriteQueueFull
	}
	if pending := w.pendingBytes.Add(size); pending > w.maximumQueue {
		w.pendingBytes.Add(-size)
		return ErrWriteQueueFull
	}
	copyOfPayload := append([]byte(nil), payload...)
	select {
	case w.queue <- copyOfPayload:
		return nil
	case err := <-w.done:
		w.pendingBytes.Add(-size)
		return fmt.Errorf("meta protocol writer stopped: %w", err)
	default:
		w.pendingBytes.Add(-size)
		return ErrWriteQueueFull
	}
}

func (w *serialFrameWriter) finish(err error) {
	select {
	case w.done <- err:
	default:
	}
	_ = w.connection.Close()
}

type TCPServer struct {
	config  config.MetaServerConfig
	service *Service
	gates   *GateStore
	metrics *MetaMetrics
	logger  *slog.Logger

	mu        sync.Mutex
	byIP      map[string]*connectionRate
	rpcRate   map[string]*connectionRate
	lastPrune time.Time
}

func NewTCPServer(
	cfg config.MetaServerConfig,
	service *Service,
	gates *GateStore,
	metrics *MetaMetrics,
	logger *slog.Logger,
) *TCPServer {
	return &TCPServer{
		config: cfg, service: service, gates: gates, metrics: metrics, logger: logger,
		byIP: make(map[string]*connectionRate), rpcRate: make(map[string]*connectionRate),
	}
}

func (s *TCPServer) Run(ctx context.Context) error {
	listener, err := net.Listen("tcp", s.config.LogicAddr)
	if err != nil {
		return err
	}
	defer listener.Close()
	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()
	for {
		connection, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if s.config.LogicProxyProtocol {
			rawAddress := remoteIP(connection.RemoteAddr())
			proxiedConnection, proxyErr := acceptProxyProtocolV1(
				connection,
				time.Duration(s.config.HandshakeTimeoutSeconds)*time.Second,
			)
			if proxyErr != nil {
				s.malformed(rawAddress, proxyErr)
				_ = connection.Close()
				continue
			}
			connection = proxiedConnection
		}
		ip := remoteIP(connection.RemoteAddr())
		if !s.admit(ip) {
			s.metrics.rateLimitedTotal.Add(1)
			_ = connection.Close()
			continue
		}
		s.metrics.connectionsActive.Add(1)
		s.metrics.connectionsTotal.Add(1)
		go func() {
			defer s.release(ip)
			s.serveConnection(ctx, connection, ip)
		}()
	}
}

func (s *TCPServer) serveConnection(ctx context.Context, connection net.Conn, ip string) {
	defer func() {
		if recovered := recover(); recovered != nil {
			s.logger.Error("MetaServer TCP panic isolated", "remote_ip", ip)
		}
		_ = connection.Close()
	}()
	handshakeTimeout := time.Duration(s.config.HandshakeTimeoutSeconds) * time.Second
	frameTimeout := time.Duration(s.config.FrameTimeoutSeconds) * time.Second
	idleTimeout := time.Duration(s.config.IdleTimeoutSeconds) * time.Second
	first, err := ReadFrameWithDeadlines(
		connection, s.config.MaxFrameBytes, handshakeTimeout, handshakeTimeout,
	)
	if err != nil {
		if !errors.Is(err, io.EOF) {
			s.malformed(ip, err)
		}
		return
	}
	request, err := DecodeRequestWrapper(first)
	if err != nil {
		s.malformed(ip, err)
		return
	}
	gate, err := s.gates.Consume(ctx, request.RPCPath)
	if err != nil {
		s.logger.Warn("MetaServer gate ticket rejected", "remote_ip", ip)
		return
	}
	active, err := s.service.repository.IsAuthSessionActive(
		ctx, gate.PlayerID, gate.AuthSessionID,
	)
	if err != nil || !active {
		s.logger.Warn(
			"MetaServer gate session is no longer active",
			"remote_ip", ip, "player_id", gate.PlayerID,
		)
		return
	}
	// Boundary's Gate handshake expects its authenticated request echoed.
	_ = connection.SetWriteDeadline(time.Now().Add(frameTimeout))
	if err := WriteFrame(connection, first, s.config.MaxFrameBytes); err != nil {
		return
	}

	writerCtx, stopWriter := context.WithCancel(ctx)
	defer stopWriter()
	writer := newSerialFrameWriter(
		connection, s.config.MaxFrameBytes, s.config.MaxWriteQueueBytes, frameTimeout,
	)
	go writer.run(writerCtx)

	malformed := 0
	for {
		payload, err := ReadFrameWithDeadlines(
			connection, s.config.MaxFrameBytes, idleTimeout, frameTimeout,
		)
		if err != nil {
			if !errors.Is(err, io.EOF) && !isNetTimeout(err) {
				s.malformed(ip, err)
			}
			return
		}
		if isKeepaliveFrame(payload) {
			if err := writer.enqueue(payload); err != nil {
				s.writeQueueRejected(ip, err)
				return
			}
			continue
		}
		request, err := DecodeRequestWrapper(payload)
		if err != nil {
			malformed++
			s.malformed(ip, err)
			if malformed >= maxMalformedFrames {
				return
			}
			continue
		}
		malformed = 0
		if !s.allowPlayerRPC(gate.PlayerID) {
			s.metrics.rateLimitedTotal.Add(1)
			return
		}
		if request.RPCPath == "/matchmaking.Matchmaking/StartUnityMatchmaking" {
			if err := nativeIdentityMatches(request.Message, gate.PlayerID); err != nil {
				s.metrics.malformedTotal.Add(1)
				s.logger.Warn(
					"MetaServer native identity mismatch rejected",
					"remote_ip", ip, "player_id", gate.PlayerID,
				)
				return
			}
		}
		response := s.dispatch(ctx, gate, request)
		encoded := EncodeResponseWrapper(response)
		if err := writer.enqueue(encoded); err != nil {
			s.writeQueueRejected(ip, err)
			return
		}
	}
}

func isKeepaliveFrame(payload []byte) bool {
	return bytes.Equal(payload, []byte("//"))
}

func (s *TCPServer) dispatch(
	ctx context.Context,
	session GateSession,
	request RequestWrapper,
) ResponseWrapper {
	started := time.Now()
	defer func() {
		s.metrics.RPC(request.RPCPath, time.Since(started))
	}()
	response := ResponseWrapper{MessageID: request.MessageID, RPCPath: request.RPCPath}
	switch request.RPCPath {
	case "/assets.Assets/GetPlayerArchiveV2":
		message, err := s.getPlayerArchive(ctx, session, request.Message)
		if err != nil {
			response.ErrorCode = rpcUnknownError
			return response
		}
		response.Message = message
	case "/assets.Assets/UpdateRoleArchiveV2":
		message, err := s.updateRoleArchive(ctx, session, request.Message)
		if err != nil {
			response.ErrorCode = rpcUnknownError
			return response
		}
		response.Message = message
	case "/assets.Assets/UpdateWeaponArchiveV2":
		message, err := s.updateWeaponArchive(ctx, session, request.Message)
		if err != nil {
			response.ErrorCode = rpcUnknownError
			return response
		}
		response.Message = message
	case "/assets.Assets/QueryAssets":
		message, err := s.queryAssets()
		if err != nil {
			response.ErrorCode = rpcUnknownError
			return response
		}
		response.Message = message
	case "/notification.Notification/QueryNotification":
		message, err := s.queryNotifications(ctx, request.Message)
		if err != nil {
			response.ErrorCode = rpcUnknownError
			return response
		}
		response.Message = message
	case "/party.party/Create":
		party, err := s.service.CreateParty(ctx, session.PlayerID, "default", "auto", session.ClientVersion)
		if err != nil {
			response.ErrorCode = rpcUnknownError
			return response
		}
		response.Message = encodeCreatePartyResponse(party)
	case "/party.party/Ready":
		partyID, _ := consumeStringField(request.Message, 1)
		if _, err := s.service.SetReady(ctx, partyID, session.PlayerID, true); err != nil {
			response.ErrorCode = rpcUnknownError
			return response
		}
		response.Message = EncodeStatusMessage(0)
	case "/party.party/SetPresence":
		presence, _ := consumeStringField(request.Message, 1)
		if _, err := s.service.SetPresence(
			ctx, activePartyID(ctx, s.service.repository, session.PlayerID),
			session.PlayerID, normalizedNativePresence(presence),
		); err != nil {
			response.ErrorCode = rpcUnknownError
			return response
		}
		response.Message = EncodeStatusMessage(0)
	case "/party.party/QueryPresence":
		message, err := s.queryPartyPresence(ctx, session)
		if err != nil {
			response.ErrorCode = rpcUnknownError
			return response
		}
		response.Message = message
	case "/matchmaking.Matchmaking/QueryUnityMatchmakingRegion":
		regions, err := s.service.Regions(ctx)
		if err != nil {
			response.ErrorCode = rpcUnknownError
			return response
		}
		response.Message = encodeRegions(regions)
	case "/matchmaking.Matchmaking/QueryPlayList":
		playlists, err := s.service.Playlists(ctx)
		if err != nil {
			response.ErrorCode = rpcUnknownError
			return response
		}
		raw, _ := json.Marshal(playlists)
		response.Message = appendStringField(EncodeStatusMessage(0), 2, string(raw))
	case "/matchmaking.Matchmaking/StartUnityMatchmaking":
		mode, _ := consumeStringField(request.Message, 2)
		if _, err := s.service.CreateMatchTicket(
			ctx, session.PlayerID, activePartyID(ctx, s.service.repository, session.PlayerID),
			mode, "auto", session.ClientVersion,
		); err != nil {
			response.ErrorCode = rpcUnknownError
			return response
		}
		response.Message = EncodeStatusMessage(0)
	case "/matchmaking.Matchmaking/QueryUnityMatchmaking",
		"/matchmaking.Matchmaking/StopUnityMatchmaking":
		// Upstream explicitly marks these message fields tentative. Until a
		// sanitized capture confirms them, return the known empty response and
		// keep state changes on the authenticated HTTP API.
		response.Message = []byte{}
	case "/profile.Profile/QueryCurrency", "/assets.Assets/QueryCurrency":
		message, err := s.queryCurrency(ctx, session)
		if err != nil {
			response.ErrorCode = rpcUnknownError
			return response
		}
		response.Message = message
	case "/playerdata.PlayerDataClient/GetDataStatisticsInfo":
		response.Message = EncodeStatusMessage(0)
	default:
		response.ErrorCode = rpcUnknownError
	}
	return response
}

func activePartyID(ctx context.Context, repository *Repository, playerID string) string {
	var partyID string
	_ = repository.pool.QueryRow(ctx, `
		SELECT party_id FROM meta_party_members
		WHERE player_id = $1 AND left_at IS NULL
		LIMIT 1
	`, playerID).Scan(&partyID)
	return partyID
}

func encodeCreatePartyResponse(party Party) []byte {
	output := EncodeStatusMessage(0)
	output = appendStringField(output, 2, party.ID)
	for _, member := range party.Members {
		output = appendStringField(output, 3, member.PlayerID)
	}
	return output
}

func encodeRegions(regions []Region) []byte {
	output := EncodeStatusMessage(0)
	for _, region := range regions {
		nested := appendStringField(nil, 1, region.ID)
		nested = appendStringField(nested, 2, region.Name)
		output = protowire.AppendTag(output, 2, protowire.BytesType)
		output = protowire.AppendBytes(output, nested)
	}
	return output
}

func appendStringField(output []byte, number protowire.Number, value string) []byte {
	output = protowire.AppendTag(output, number, protowire.BytesType)
	return protowire.AppendString(output, value)
}

func consumeStringField(data []byte, wanted protowire.Number) (string, bool) {
	for len(data) > 0 {
		number, typ, tagLength := protowire.ConsumeTag(data)
		if tagLength < 0 {
			return "", false
		}
		data = data[tagLength:]
		if typ == protowire.BytesType {
			value, n := protowire.ConsumeBytes(data)
			if n < 0 {
				return "", false
			}
			if number == wanted {
				return string(value), true
			}
			data = data[n:]
			continue
		}
		n := protowire.ConsumeFieldValue(number, typ, data)
		if n < 0 {
			return "", false
		}
		data = data[n:]
	}
	return "", false
}

func (s *TCPServer) admit(ip string) bool {
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneRateStatesLocked(now)
	state := s.byIP[ip]
	if state == nil {
		if len(s.byIP) >= maxRateStates {
			return false
		}
		state = &connectionRate{windowStart: now, lastSeen: now}
		s.byIP[ip] = state
	}
	state.lastSeen = now
	if now.Sub(state.windowStart) >= time.Minute {
		state.windowStart = now
		state.started = 0
	}
	if state.active >= s.config.MaxConnectionsPerIP ||
		state.started >= s.config.ConnectionsPerIPPerMinute {
		return false
	}
	state.active++
	state.started++
	return true
}

func (s *TCPServer) release(ip string) {
	s.metrics.connectionsActive.Add(-1)
	s.mu.Lock()
	if state := s.byIP[ip]; state != nil && state.active > 0 {
		state.active--
		state.lastSeen = time.Now().UTC()
	}
	s.mu.Unlock()
}

func (s *TCPServer) allowPlayerRPC(playerID string) bool {
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneRateStatesLocked(now)
	state := s.rpcRate[playerID]
	if state == nil || now.Sub(state.windowStart) >= time.Minute {
		if state == nil && len(s.rpcRate) >= maxRateStates {
			return false
		}
		s.rpcRate[playerID] = &connectionRate{
			windowStart: now, lastSeen: now, started: 1,
		}
		return true
	}
	state.lastSeen = now
	if state.started >= s.config.RPCCallsPerPlayerPerMinute {
		return false
	}
	state.started++
	return true
}

func (s *TCPServer) pruneRateStatesLocked(now time.Time) {
	if !s.lastPrune.IsZero() && now.Sub(s.lastPrune) < rateStatePruneInterval &&
		len(s.byIP) < maxRateStates && len(s.rpcRate) < maxRateStates {
		return
	}
	cutoff := now.Add(-rateStateRetention)
	for key, state := range s.byIP {
		if state.active == 0 && state.lastSeen.Before(cutoff) {
			delete(s.byIP, key)
		}
	}
	for key, state := range s.rpcRate {
		if state.lastSeen.Before(cutoff) {
			delete(s.rpcRate, key)
		}
	}
	s.lastPrune = now
}

func (s *TCPServer) malformed(ip string, err error) {
	s.metrics.malformedTotal.Add(1)
	s.logger.Warn("MetaServer malformed frame rejected", "remote_ip", ip, "error_type", errorClass(err))
}

func (s *TCPServer) writeQueueRejected(ip string, err error) {
	s.metrics.rateLimitedTotal.Add(1)
	s.logger.Warn(
		"MetaServer write queue rejected response",
		"remote_ip", ip, "error_type", errorClass(err),
	)
}

func remoteIP(address net.Addr) string {
	host, _, err := net.SplitHostPort(address.String())
	if err != nil {
		return address.String()
	}
	return host
}

func errorClass(err error) string {
	switch {
	case errors.Is(err, ErrFrameTooLarge):
		return "frame_too_large"
	case errors.Is(err, ErrEmptyFrame):
		return "empty_frame"
	case errors.Is(err, ErrWriteQueueFull):
		return "write_queue_full"
	default:
		return "malformed"
	}
}

func isNetTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}
