package loadbot

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/relayclient"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

type virtualClient struct {
	mu           sync.RWMutex
	playerID     string
	accessToken  string
	refreshToken string
}

func (c *virtualClient) withAccessToken(request func(string) error) error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return request(c.accessToken)
}

func (c *virtualClient) rotateTokens(
	refresh func(string) (accessToken string, refreshToken string, err error),
) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	accessToken, refreshToken, err := refresh(c.refreshToken)
	if err != nil {
		return err
	}
	c.accessToken, c.refreshToken = accessToken, refreshToken
	return nil
}

type roomFixture struct {
	id        string
	hostToken string
	host      *virtualClient
	peer      *virtualClient
}

type relayPair struct {
	mu           sync.RWMutex
	connectionID string
	host         *relayclient.Client
	peer         *relayclient.Client
	retired      []*relayclient.Client
	sockets      [2]*websocket.Conn
	clients      [2]*virtualClient
}

type eventEnvelope struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type relayAllocationPayload struct {
	AllocationID string `json:"allocation_id"`
	MigrationID  string `json:"migration_id"`
	RelayToken   string `json:"relay_token"`
	Relay        struct {
		Host string `json:"host"`
		Port int    `json:"port"`
	} `json:"relay"`
}

func (r *Runner) runEndToEnd(ctx context.Context) {
	clients := r.bindClients(ctx)
	if len(clients) < r.cfg.Rooms*2 {
		r.recordFailure("insufficient_authenticated_clients")
		return
	}
	rooms := r.createRooms(ctx, clients)
	if len(rooms) == 0 {
		return
	}
	var pairs []*relayPair
	if r.cfg.Scenario == "relay" || r.cfg.Scenario == "full" || r.cfg.Scenario == "soak" {
		for index := 0; index < r.cfg.RelayConnections && index < len(rooms); index++ {
			pair, err := r.createRelayPair(ctx, rooms[index], index)
			if err != nil {
				r.mu.Lock()
				r.report.RelayBindFailures++
				r.mu.Unlock()
				r.recordFailure("relay_setup")
				continue
			}
			pairs = append(pairs, pair)
		}
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); r.runRoomHeartbeats(ctx, rooms) }()
	for _, pair := range pairs {
		wg.Add(1)
		go func(pair *relayPair) { defer wg.Done(); r.runRelayTraffic(ctx, pair) }(pair)
		for role := 0; role < 2; role++ {
			wg.Add(1)
			go func(pair *relayPair, role int) { defer wg.Done(); r.watchRelayEvents(ctx, pair, role) }(pair, role)
		}
	}
	if r.cfg.FailureInjection.DisconnectPercent > 0 && len(pairs) > 0 {
		wg.Add(1)
		go func() { defer wg.Done(); r.runDisconnectInjection(ctx, pairs) }()
	}
	wg.Add(1)
	go func() { defer wg.Done(); r.runTokenRefresh(ctx, clients) }()
	<-ctx.Done()
	wg.Wait()

	cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, pair := range pairs {
		if err := r.requestJSONAs(cleanupCtx, pair.clients[0], http.MethodDelete,
			"/v1/connections/"+pair.connectionID, nil, nil, nil); err != nil {
			r.recordFailure("relay_cleanup")
		} else {
			r.mu.Lock()
			r.report.RelayAllocationsClosed++
			r.mu.Unlock()
		}
		pair.close()
	}
	for _, room := range rooms {
		if err := r.requestJSONAs(cleanupCtx, room.host, http.MethodDelete,
			"/v1/p2p-rooms/"+room.id,
			map[string]string{"X-Room-Host-Token": room.hostToken}, nil, nil); err != nil {
			r.recordFailure("room_cleanup")
		}
	}
}

func (r *Runner) bindClients(ctx context.Context) []*virtualClient {
	clients := make([]*virtualClient, r.cfg.Clients)
	semaphore := make(chan struct{}, r.cfg.SetupConcurrency)
	var wg sync.WaitGroup
	for id := 0; id < r.cfg.Clients; id++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			select {
			case semaphore <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-semaphore }()
			var response struct {
				Data struct {
					PlayerID string `json:"player_id"`
					Session  struct {
						AccessToken  string `json:"access_token"`
						RefreshToken string `json:"refresh_token"`
					} `json:"session"`
				} `json:"data"`
			}
			steamID := fmt.Sprintf("7656119%010d", id)
			bindRequest := map[string]any{
				"steam_id": steamID, "persona_name": fmt.Sprintf("loadbot-%d", id),
				"device_id": fmt.Sprintf("loadbot-device-%d", id), "invite_code": r.cfg.Auth.InviteCode,
			}
			if r.cfg.Auth.UnsafeTestTicketFixture {
				bindRequest["encrypted_ticket"] = fixtureEncryptedTicket(steamID)
			}
			err := r.requestJSON(ctx, http.MethodPost, "/v1/auth/bind", "", nil, bindRequest, &response)
			if err != nil || response.Data.PlayerID == "" || response.Data.Session.AccessToken == "" {
				r.recordFailure("auth_bind")
				return
			}
			clients[id] = &virtualClient{playerID: response.Data.PlayerID, accessToken: response.Data.Session.AccessToken, refreshToken: response.Data.Session.RefreshToken}
		}(id)
	}
	wg.Wait()
	result := make([]*virtualClient, 0, len(clients))
	for _, client := range clients {
		if client != nil {
			result = append(result, client)
		}
	}
	return result
}

func (r *Runner) createRooms(ctx context.Context, clients []*virtualClient) []roomFixture {
	rooms := make([]roomFixture, 0, r.cfg.Rooms)
	for index := 0; index < r.cfg.Rooms; index++ {
		host, peer := clients[index*2], clients[index*2+1]
		var created struct {
			Data struct {
				Room struct {
					RoomID string `json:"room_id"`
				} `json:"room"`
				HostToken string `json:"host_token"`
			} `json:"data"`
		}
		err := r.requestJSONAs(ctx, host, http.MethodPost, "/v1/p2p-rooms", nil, map[string]any{
			"display_name": fmt.Sprintf("loadbot-room-%d", index), "region": r.cfg.Room.Region,
			"mode": r.cfg.Room.Mode, "version": r.cfg.Room.Version, "max_players": 2,
		}, &created)
		if err != nil || created.Data.Room.RoomID == "" {
			r.recordFailure("room_create")
			continue
		}
		if err := r.requestJSONAs(ctx, peer, http.MethodPost,
			"/v1/p2p-rooms/"+created.Data.Room.RoomID+"/join",
			nil, map[string]string{"version": r.cfg.Room.Version}, nil); err != nil {
			r.recordFailure("room_join")
			continue
		}
		rooms = append(rooms, roomFixture{id: created.Data.Room.RoomID, hostToken: created.Data.HostToken, host: host, peer: peer})
		r.mu.Lock()
		r.report.RoomsCreated++
		r.mu.Unlock()
	}
	return rooms
}

func (r *Runner) createRelayPair(ctx context.Context, room roomFixture, index int) (*relayPair, error) {
	hostSocket, err := r.openWebSocket(ctx, room.host)
	if err != nil {
		return nil, err
	}
	peerSocket, err := r.openWebSocket(ctx, room.peer)
	if err != nil {
		_ = hostSocket.CloseNow()
		return nil, err
	}
	var connectionResponse struct {
		Data struct {
			ConnectionID string `json:"connection_id"`
		} `json:"data"`
	}
	if err := r.requestJSONAs(ctx, room.host, http.MethodPost, "/v1/connections", nil,
		map[string]string{"room_id": room.id, "peer_player_id": room.peer.playerID},
		&connectionResponse); err != nil {
		_ = hostSocket.CloseNow()
		_ = peerSocket.CloseNow()
		return nil, err
	}
	connectionID := connectionResponse.Data.ConnectionID
	for roleIndex, item := range []struct {
		socket  *websocket.Conn
		address string
	}{{hostSocket, fmt.Sprintf("10.0.0.%d", 2+index%200)}, {peerSocket, fmt.Sprintf("10.1.0.%d", 2+index%200)}} {
		event := map[string]any{"type": "connection.candidate", "payload": map[string]any{
			"connection_id": connectionID, "foundation": fmt.Sprintf("loadbot-%d-%d", index, roleIndex),
			"candidate_type": "LAN", "protocol": "UDP", "address": item.address, "port": 30000 + index, "priority": 100,
		}}
		if err := wsjson.Write(ctx, item.socket, event); err != nil {
			return nil, err
		}
	}
	ready := false
	for attempt := 0; attempt < 50; attempt++ {
		var current struct {
			Data struct {
				State string `json:"state"`
			} `json:"data"`
		}
		if err := r.requestJSONAs(ctx, room.host, http.MethodGet,
			"/v1/connections/"+connectionID, nil, nil, &current); err == nil &&
			current.Data.State == "CHECKING_DIRECT" {
			ready = true
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	if !ready {
		return nil, fmt.Errorf("connection candidates did not become ready")
	}
	check := map[string]any{"type": "connection.check_result", "payload": map[string]any{
		"connection_id": connectionID, "success": false, "path": "LAN", "latency_ms": 1, "reason": "loadbot relay scenario",
	}}
	if err := wsjson.Write(ctx, hostSocket, check); err != nil {
		return nil, err
	}
	hostAllocation, err := waitRelayAllocation(ctx, hostSocket)
	if err != nil {
		return nil, err
	}
	peerAllocation, err := waitRelayAllocation(ctx, peerSocket)
	if err != nil {
		return nil, err
	}
	if hostAllocation.AllocationID != peerAllocation.AllocationID {
		return nil, fmt.Errorf("participants received different allocations")
	}
	endpoint := net.JoinHostPort(hostAllocation.Relay.Host, fmt.Sprint(hostAllocation.Relay.Port))
	hostRelay, err := relayclient.Dial(ctx, endpoint, hostAllocation.RelayToken, 1200)
	if err != nil {
		return nil, err
	}
	peerRelay, err := relayclient.Dial(ctx, endpoint, peerAllocation.RelayToken, 1200)
	if err != nil {
		_ = hostRelay.Close()
		return nil, err
	}
	pair := &relayPair{
		connectionID: connectionID,
		host:         hostRelay,
		peer:         peerRelay,
		sockets:      [2]*websocket.Conn{hostSocket, peerSocket},
		clients:      [2]*virtualClient{room.host, room.peer},
	}
	r.mu.Lock()
	r.report.RelayAllocations++
	r.report.RelayBindSuccess += 2
	r.mu.Unlock()
	return pair, nil
}

func (r *Runner) openWebSocket(ctx context.Context, client *virtualClient) (*websocket.Conn, error) {
	url := r.cfg.RealtimeURL
	if url == "" {
		url = strings.Replace(r.cfg.ControlPlaneURL, "http://", "ws://", 1)
		url = strings.Replace(url, "https://", "wss://", 1) + "/v1/realtime/connect"
	}
	var socket *websocket.Conn
	err := client.withAccessToken(func(accessToken string) error {
		header := http.Header{"Authorization": []string{"Bearer " + accessToken}}
		var dialErr error
		socket, _, dialErr = websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: header})
		return dialErr
	})
	return socket, err
}

func waitRelayAllocation(ctx context.Context, socket *websocket.Conn) (relayAllocationPayload, error) {
	for {
		var event eventEnvelope
		if err := wsjson.Read(ctx, socket, &event); err != nil {
			return relayAllocationPayload{}, err
		}
		if event.Type == "connection.relay_failed" {
			return relayAllocationPayload{}, fmt.Errorf("Relay allocation failed")
		}
		if event.Type != "connection.relay_allocated" {
			continue
		}
		var allocation relayAllocationPayload
		if err := json.Unmarshal(event.Payload, &allocation); err != nil {
			return relayAllocationPayload{}, err
		}
		if allocation.RelayToken == "" || allocation.Relay.Host == "" || allocation.Relay.Port < 1 {
			return relayAllocationPayload{}, fmt.Errorf("invalid Relay allocation event")
		}
		return allocation, nil
	}
}

func (r *Runner) runRoomHeartbeats(ctx context.Context, rooms []roomFixture) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, room := range rooms {
				if err := r.requestJSONAs(ctx, room.host, http.MethodPost,
					"/v1/p2p-rooms/"+room.id+"/heartbeat",
					map[string]string{"X-Room-Host-Token": room.hostToken},
					nil, nil); err != nil && ctx.Err() == nil {
					r.recordFailure("room_heartbeat")
				}
			}
		}
	}
}

func (r *Runner) runRelayTraffic(ctx context.Context, pair *relayPair) {
	pps := r.cfg.Traffic.PacketsPerSecond
	if pps <= 0 {
		<-ctx.Done()
		return
	}
	interval := time.Second / time.Duration(pps)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	payload := make([]byte, r.cfg.Traffic.PayloadBytes)
	for index := range payload {
		payload[index] = byte(index)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if r.cfg.Traffic.JitterMS > 0 {
				time.Sleep(time.Duration(rand.IntN(r.cfg.Traffic.JitterMS+1)) * time.Millisecond)
			}
			pair.mu.RLock()
			host, peer := pair.host, pair.peer
			pair.mu.RUnlock()
			opCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			r.mu.Lock()
			r.report.PacketsSent++
			r.mu.Unlock()
			err := host.Send(opCtx, payload)
			if err == nil {
				var received []byte
				received, err = peer.Receive(opCtx)
				if err == nil && len(received) != len(payload) {
					err = fmt.Errorf("Relay payload length mismatch")
				}
			}
			cancel()
			if err != nil && ctx.Err() == nil {
				r.mu.Lock()
				r.report.PacketsDropped++
				r.mu.Unlock()
				r.recordFailure("relay_traffic")
				continue
			}
			r.mu.Lock()
			r.report.BytesSent += uint64(len(payload))
			r.report.BytesReceived += uint64(len(payload))
			r.report.PacketsReceived++
			r.mu.Unlock()
		}
	}
}

func (r *Runner) watchRelayEvents(ctx context.Context, pair *relayPair, role int) {
	for {
		pair.mu.RLock()
		socket := pair.sockets[role]
		client := pair.clients[role]
		pair.mu.RUnlock()
		var event eventEnvelope
		if err := wsjson.Read(ctx, socket, &event); err != nil {
			if ctx.Err() != nil {
				return
			}
			delay := time.Duration(r.cfg.FailureInjection.ReconnectDelaySeconds) * time.Second
			if delay <= 0 {
				delay = time.Second
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
			replacement, dialErr := r.openWebSocket(ctx, client)
			if dialErr != nil {
				r.recordFailure("websocket_reconnect")
				continue
			}
			pair.mu.Lock()
			pair.sockets[role] = replacement
			pair.mu.Unlock()
			r.mu.Lock()
			r.report.WebSocketReconnects++
			r.mu.Unlock()
			continue
		}
		switch event.Type {
		case "connection.relay_allocated":
			var allocation relayAllocationPayload
			if err := json.Unmarshal(event.Payload, &allocation); err != nil || allocation.RelayToken == "" {
				r.mu.Lock()
				r.report.RelayBindFailures++
				r.mu.Unlock()
				r.recordFailure("relay_migration_event")
				continue
			}
			endpoint := net.JoinHostPort(allocation.Relay.Host, fmt.Sprint(allocation.Relay.Port))
			replacement, err := relayclient.Dial(ctx, endpoint, allocation.RelayToken, 1200)
			if err != nil {
				r.mu.Lock()
				r.report.RelayBindFailures++
				r.mu.Unlock()
				r.recordFailure("relay_migration_bind")
				continue
			}
			pair.mu.Lock()
			if role == 0 {
				pair.retired = append(pair.retired, pair.host)
				pair.host = replacement
			} else {
				pair.retired = append(pair.retired, pair.peer)
				pair.peer = replacement
			}
			pair.mu.Unlock()
			r.mu.Lock()
			r.report.RelayBindSuccess++
			if role == 0 {
				r.report.RelayAllocations++
			}
			r.mu.Unlock()
		case "connection.relay_migrating":
			var payload struct {
				MigrationID string `json:"migration_id"`
			}
			if json.Unmarshal(event.Payload, &payload) == nil {
				r.recordMigrationAttempt(payload.MigrationID)
			}
		case "connection.relay_migrated":
			var payload struct {
				MigrationID string `json:"migration_id"`
			}
			if json.Unmarshal(event.Payload, &payload) == nil {
				r.recordMigration(payload.MigrationID)
			}
		case "connection.relay_failed":
			r.recordFailure("relay_migration_failed")
		}
	}
}

func (r *Runner) runDisconnectInjection(ctx context.Context, pairs []*relayPair) {
	interval := time.Duration(r.cfg.FailureInjection.ReconnectDelaySeconds) * time.Second
	if interval < 30*time.Second {
		interval = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, pair := range pairs {
				for role := 0; role < 2; role++ {
					if rand.IntN(100) >= r.cfg.FailureInjection.DisconnectPercent {
						continue
					}
					pair.mu.RLock()
					socket := pair.sockets[role]
					pair.mu.RUnlock()
					_ = socket.CloseNow()
				}
			}
		}
	}
}

func (r *Runner) runTokenRefresh(ctx context.Context, clients []*virtualClient) {
	interval, _ := time.ParseDuration(r.cfg.Auth.RefreshInterval)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, client := range clients {
				if err := client.rotateTokens(func(refreshToken string) (string, string, error) {
					var response struct {
						Data struct {
							Session struct {
								AccessToken  string `json:"access_token"`
								RefreshToken string `json:"refresh_token"`
							} `json:"session"`
						} `json:"data"`
					}
					err := r.requestJSON(
						ctx,
						http.MethodPost,
						"/v1/auth/refresh",
						"",
						nil,
						map[string]string{"refresh_token": refreshToken},
						&response,
					)
					if err != nil {
						return "", "", err
					}
					if response.Data.Session.AccessToken == "" ||
						response.Data.Session.RefreshToken == "" {
						return "", "", fmt.Errorf("refresh response did not contain both tokens")
					}
					return response.Data.Session.AccessToken, response.Data.Session.RefreshToken, nil
				}); err != nil {
					r.mu.Lock()
					r.report.TokenRefreshFailures++
					r.mu.Unlock()
					r.recordFailure("token_refresh")
				}
			}
		}
	}
}

func (r *Runner) requestJSONAs(
	ctx context.Context,
	client *virtualClient,
	method string,
	path string,
	headers map[string]string,
	body any,
	result any,
) error {
	return client.withAccessToken(func(accessToken string) error {
		return r.requestJSON(ctx, method, path, accessToken, headers, body, result)
	})
}

func (p *relayPair) close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.host != nil {
		_ = p.host.Close()
	}
	if p.peer != nil {
		_ = p.peer.Close()
	}
	for _, retired := range p.retired {
		_ = retired.Close()
	}
	for _, socket := range p.sockets {
		if socket != nil {
			_ = socket.CloseNow()
		}
	}
}
