package loadbot

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
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

type matchFixture struct {
	lobbyID          string
	p2pRoomID        string
	hostToken        string
	host             *virtualClient
	peer             *virtualClient
	attemptID        string
	authoritySession string
	rosterRevision   int64
	routeGeneration  int
	started          bool
}

type matchLobbySnapshot struct {
	LobbyID        string            `json:"lobby_id"`
	OwnerPlayerID  string            `json:"owner_player_id"`
	P2PRoomID      string            `json:"p2p_room_id"`
	State          string            `json:"state"`
	RosterRevision int64             `json:"roster_revision"`
	Attempt        *matchAttemptView `json:"attempt"`
}

type matchAttemptView struct {
	AttemptID        string `json:"attempt_id"`
	State            string `json:"state"`
	RosterRevision   int64  `json:"roster_revision"`
	RouteGeneration  int    `json:"route_generation"`
	PayloadInstalled bool   `json:"payload_installed"`
	CleanupState     string `json:"cleanup_state"`
	WorldInstanceID  string `json:"world_instance_id"`
}

type allocationClaims struct {
	Issuer             string                   `json:"iss"`
	Audience           string                   `json:"aud"`
	KeyID              string                   `json:"kid"`
	TokenID            string                   `json:"jti"`
	AttemptID          string                   `json:"attempt_id"`
	LobbyID            string                   `json:"lobby_id"`
	HostingKind        string                   `json:"hosting_kind"`
	AuthorityID        string                   `json:"authority_id"`
	AuthoritySessionID string                   `json:"authority_session_id"`
	RosterRevision     int64                    `json:"roster_revision"`
	RouteGeneration    int                      `json:"route_generation"`
	ConnectionWindow   int                      `json:"initial_connection_window_seconds"`
	Roster             []allocationRosterMember `json:"roster"`
	NotBefore          int64                    `json:"nbf"`
	ExpiresAt          int64                    `json:"exp"`
}

type allocationRosterMember struct {
	PlayerID             string `json:"player_id"`
	RoomRole             string `json:"room_role"`
	ConnectionGeneration int    `json:"connection_generation"`
}

type allocationHeader struct {
	Algorithm string `json:"alg"`
	Type      string `json:"typ"`
	KeyID     string `json:"kid"`
}

type allocationResult struct {
	AttemptID          string    `json:"attempt_id"`
	Allocation         string    `json:"allocation"`
	AdmissionKeyID     string    `json:"admission_key_id"`
	AdmissionPublicKey string    `json:"admission_public_key_base64"`
	ExpiresAt          time.Time `json:"expires_at"`
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
	matches := r.createMatchLobbies(ctx, clients)
	if len(matches) == 0 {
		return
	}
	var pairs []*relayPair
	if r.cfg.Scenario == "relay" || r.cfg.Scenario == "websocket" || r.cfg.Scenario == "full" || r.cfg.Scenario == "soak" {
		for index := 0; index < r.cfg.RelayConnections && index < len(matches); index++ {
			pair, err := r.createRelayPair(ctx, matches[index], index)
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
	go func() { defer wg.Done(); r.runMatchLobbyHeartbeats(ctx, matches) }()
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
	for _, match := range matches {
		r.completeMatchAttempt(cleanupCtx, match)
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

func (r *Runner) createMatchLobbies(ctx context.Context, clients []*virtualClient) []matchFixture {
	matches := make([]matchFixture, 0, r.cfg.Rooms)
	for index := 0; index < r.cfg.Rooms; index++ {
		host, peer := clients[index*2], clients[index*2+1]
		var created struct {
			Data struct {
				Lobby              matchLobbySnapshot `json:"lobby"`
				TransportHostToken string             `json:"transport_host_token"`
			} `json:"data"`
		}
		runID := r.runID
		if runID == "" {
			runID = fmt.Sprintf("%d", time.Now().UnixNano())
		}
		idempotencyKey := fmt.Sprintf("loadbot-%s-%d", runID, index)
		err := r.requestJSONAsWithHeaders(ctx, host, http.MethodPost, "/v1/match-lobbies",
			map[string]string{"Idempotency-Key": idempotencyKey}, map[string]any{
				"display_name":     fmt.Sprintf("loadbot-lobby-%d", index),
				"hosting_kind":     "P2P",
				"transport_kind":   "LEGACY_RELAY",
				"mode":             r.cfg.Room.Mode,
				"region":           r.cfg.Room.Region,
				"client_version":   r.cfg.Room.Version,
				"protocol_version": 1,
				"team_capacities":  map[string]int{"team_1": 1, "team_2": 1},
				"team_id":          1,
			}, &created)
		if err != nil || created.Data.Lobby.LobbyID == "" || created.Data.Lobby.OwnerPlayerID != host.playerID ||
			created.Data.Lobby.P2PRoomID == "" || created.Data.TransportHostToken == "" || created.Data.Lobby.RosterRevision < 1 {
			r.recordFailure("match_lobby_create")
			continue
		}
		match := matchFixture{
			lobbyID: created.Data.Lobby.LobbyID, p2pRoomID: created.Data.Lobby.P2PRoomID,
			hostToken: created.Data.TransportHostToken, host: host, peer: peer,
			rosterRevision: created.Data.Lobby.RosterRevision,
		}
		var joined struct {
			Data matchLobbySnapshot `json:"data"`
		}
		if err := r.requestJSONAs(ctx, peer, http.MethodPost,
			"/v1/match-lobbies/"+match.lobbyID+"/join", nil,
			map[string]any{"team_id": 2, "expected_revision": match.rosterRevision}, &joined); err != nil ||
			joined.Data.LobbyID != match.lobbyID || joined.Data.RosterRevision <= match.rosterRevision {
			r.recordFailure("match_lobby_join")
			r.cleanupOpenMatchLobby(ctx, match)
			continue
		}
		match.rosterRevision = joined.Data.RosterRevision
		readyFailed := false
		for _, ready := range []struct {
			client *virtualClient
			label  string
		}{
			{peer, "peer"},
			{host, "host"},
		} {
			var response struct {
				Data matchLobbySnapshot `json:"data"`
			}
			if err := r.requestJSONAs(ctx, ready.client, http.MethodPut,
				"/v1/match-lobbies/"+match.lobbyID+"/members/me/ready", nil,
				map[string]any{"ready": true, "expected_revision": match.rosterRevision}, &response); err != nil ||
				response.Data.LobbyID != match.lobbyID || response.Data.RosterRevision != match.rosterRevision {
				r.recordFailure("match_lobby_ready_" + ready.label)
				readyFailed = true
				break
			}
			match.rosterRevision = response.Data.RosterRevision
		}
		if readyFailed {
			r.cleanupOpenMatchLobby(ctx, match)
			continue
		}
		var started struct {
			Data matchLobbySnapshot `json:"data"`
		}
		if err := r.requestJSONAs(ctx, host, http.MethodPost,
			"/v1/match-lobbies/"+match.lobbyID+"/start", nil,
			map[string]any{"expected_revision": match.rosterRevision}, &started); err != nil ||
			started.Data.LobbyID != match.lobbyID || started.Data.P2PRoomID != match.p2pRoomID ||
			started.Data.Attempt == nil || started.Data.Attempt.AttemptID == "" ||
			started.Data.Attempt.RosterRevision != match.rosterRevision || started.Data.Attempt.RouteGeneration < 1 {
			r.recordFailure("match_lobby_start")
			r.cleanupOpenMatchLobby(ctx, match)
			continue
		}
		match.started = true
		match.attemptID = started.Data.Attempt.AttemptID
		match.rosterRevision = started.Data.Attempt.RosterRevision
		match.routeGeneration = started.Data.Attempt.RouteGeneration
		match.p2pRoomID = started.Data.P2PRoomID
		_, claims, err := r.fetchAndVerifyAllocation(ctx, match)
		if err != nil {
			r.recordFailure("match_allocation")
			// Retain the started fixture so cleanup reports the missing verified
			// authority session rather than claiming that the attempt was cleared.
		} else {
			match.authoritySession = claims.AuthoritySessionID
		}
		matches = append(matches, match)
		r.mu.Lock()
		r.report.MatchLobbiesCreated++
		r.report.RoomsCreated++
		r.report.MatchAttemptsStarted++
		r.mu.Unlock()
	}
	return matches
}

func (r *Runner) createRelayPair(ctx context.Context, match matchFixture, index int) (*relayPair, error) {
	hostSocket, err := r.openWebSocket(ctx, match.host)
	if err != nil {
		return nil, err
	}
	peerSocket, err := r.openWebSocket(ctx, match.peer)
	if err != nil {
		_ = hostSocket.CloseNow()
		return nil, err
	}
	var connectionResponse struct {
		Data struct {
			ConnectionID string `json:"connection_id"`
		} `json:"data"`
	}
	if match.p2pRoomID == "" {
		_ = hostSocket.CloseNow()
		_ = peerSocket.CloseNow()
		return nil, fmt.Errorf("match lobby omitted managed P2P room")
	}
	if err := r.requestJSONAs(ctx, match.host, http.MethodPost, "/v1/connections", nil,
		map[string]string{"room_id": match.p2pRoomID, "peer_player_id": match.peer.playerID},
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
		if err := r.requestJSONAs(ctx, match.host, http.MethodGet,
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
		clients:      [2]*virtualClient{match.host, match.peer},
	}
	r.mu.Lock()
	r.report.RelayAllocations++
	r.report.RelayBindSuccess += 2
	r.mu.Unlock()
	return pair, nil
}

func (r *Runner) fetchAndVerifyAllocation(ctx context.Context, match matchFixture) (allocationResult, allocationClaims, error) {
	if match.attemptID == "" || match.lobbyID == "" {
		return allocationResult{}, allocationClaims{}, fmt.Errorf("match attempt scope is incomplete")
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		var response struct {
			Data allocationResult `json:"data"`
		}
		err := r.requestJSONAs(ctx, match.host, http.MethodGet,
			"/v1/match-attempts/"+match.attemptID+"/host/allocation", nil, nil, &response)
		if err == nil {
			claims, verifyErr := verifyAllocation(response.Data, match)
			if verifyErr == nil {
				return response.Data, claims, nil
			}
			lastErr = verifyErr
		} else {
			lastErr = err
		}
		if attempt < 2 {
			select {
			case <-ctx.Done():
				return allocationResult{}, allocationClaims{}, ctx.Err()
			case <-time.After(200 * time.Millisecond):
			}
		}
	}
	return allocationResult{}, allocationClaims{}, lastErr
}

func verifyAllocation(allocation allocationResult, match matchFixture) (allocationClaims, error) {
	parts := strings.Split(allocation.Allocation, ".")
	if len(parts) != 3 {
		return allocationClaims{}, fmt.Errorf("allocation is not a compact JWT")
	}
	decodeURL := func(value string) ([]byte, error) {
		return base64.RawURLEncoding.DecodeString(value)
	}
	var header allocationHeader
	headerBytes, err := decodeURL(parts[0])
	if err != nil {
		return allocationClaims{}, fmt.Errorf("decode allocation header: %w", err)
	}
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return allocationClaims{}, fmt.Errorf("decode allocation header JSON: %w", err)
	}
	if header.Algorithm != "EdDSA" || header.Type != "match-allocation+jwt" || header.KeyID != allocation.AdmissionKeyID {
		return allocationClaims{}, fmt.Errorf("allocation header is not the expected strict authority token")
	}
	claimsBytes, err := decodeURL(parts[1])
	if err != nil {
		return allocationClaims{}, fmt.Errorf("decode allocation claims: %w", err)
	}
	var claims allocationClaims
	if err := json.Unmarshal(claimsBytes, &claims); err != nil {
		return allocationClaims{}, fmt.Errorf("decode allocation claims JSON: %w", err)
	}
	publicKeyBytes, err := base64.StdEncoding.DecodeString(allocation.AdmissionPublicKey)
	if err != nil {
		publicKeyBytes, err = base64.RawStdEncoding.DecodeString(allocation.AdmissionPublicKey)
	}
	if err != nil || len(publicKeyBytes) != ed25519.PublicKeySize {
		return allocationClaims{}, fmt.Errorf("allocation admission public key is invalid")
	}
	signature, err := decodeURL(parts[2])
	if err != nil || len(signature) != ed25519.SignatureSize ||
		!ed25519.Verify(ed25519.PublicKey(publicKeyBytes), []byte(parts[0]+"."+parts[1]), signature) {
		return allocationClaims{}, fmt.Errorf("allocation signature verification failed")
	}
	now := time.Now().Unix()
	if claims.KeyID != header.KeyID || claims.Issuer != "game-control-plane" || claims.Audience != "project-rebound-match-authority" ||
		claims.TokenID == "" || claims.NotBefore > now+5 || claims.ExpiresAt <= now ||
		allocation.ExpiresAt.IsZero() || claims.ExpiresAt != allocation.ExpiresAt.Unix() || claims.ConnectionWindow <= 0 {
		return allocationClaims{}, fmt.Errorf("allocation temporal or issuer claims are invalid")
	}
	if claims.AttemptID != match.attemptID || claims.LobbyID != match.lobbyID ||
		allocation.AttemptID != match.attemptID ||
		claims.HostingKind != "P2P" || claims.AuthorityID != match.host.playerID ||
		claims.RosterRevision != match.rosterRevision || claims.RouteGeneration != match.routeGeneration || claims.RouteGeneration < 1 ||
		claims.AuthoritySessionID == "" || len(claims.Roster) != 2 {
		return allocationClaims{}, fmt.Errorf("allocation claims do not match the frozen MatchLobby attempt")
	}
	hostRoster, peerRoster := false, false
	for _, member := range claims.Roster {
		if member.ConnectionGeneration < 1 {
			return allocationClaims{}, fmt.Errorf("allocation roster contains an invalid connection generation")
		}
		switch {
		case member.PlayerID == match.host.playerID && member.RoomRole == "HOST":
			hostRoster = true
		case member.PlayerID == match.peer.playerID && member.RoomRole == "MEMBER":
			peerRoster = true
		}
	}
	if !hostRoster || !peerRoster {
		return allocationClaims{}, fmt.Errorf("allocation roster does not contain the frozen P2P HOST and MEMBER seats")
	}
	return claims, nil
}

func (r *Runner) cleanupOpenMatchLobby(ctx context.Context, match matchFixture) {
	if match.lobbyID == "" || match.host == nil {
		return
	}
	var current struct {
		Data matchLobbySnapshot `json:"data"`
	}
	if err := r.requestJSONAs(ctx, match.host, http.MethodGet,
		"/v1/match-lobbies/"+match.lobbyID, nil, nil, &current); err != nil {
		r.recordFailure("match_lobby_cleanup")
		return
	}
	if current.Data.State != "OPEN" {
		return
	}
	if err := r.requestJSONAsWithHeaders(ctx, match.host, http.MethodPost,
		"/v1/match-lobbies/"+match.lobbyID+"/leave",
		map[string]string{"X-Match-Transport-Host-Token": match.hostToken},
		map[string]any{"expected_revision": current.Data.RosterRevision}, nil); err != nil {
		r.recordFailure("match_lobby_cleanup")
	}
}

func (r *Runner) completeMatchAttempt(ctx context.Context, match matchFixture) {
	if !match.started {
		r.cleanupOpenMatchLobby(ctx, match)
		return
	}
	if match.attemptID == "" || match.authoritySession == "" {
		r.recordFailure("match_cleanup_pending")
		r.mu.Lock()
		r.report.MatchCleanupPending++
		r.mu.Unlock()
		return
	}
	var completed struct {
		Data matchLobbySnapshot `json:"data"`
	}
	err := r.requestJSONAsWithHeaders(ctx, match.host, http.MethodPost,
		"/v1/match-attempts/"+match.attemptID+"/host/complete",
		map[string]string{"X-Match-Authority-Session": match.authoritySession},
		map[string]any{"success": false, "failure_code": "LOADBOT_TRANSPORT_STOPPED"}, &completed)
	if err != nil {
		r.recordFailure("match_cleanup")
		r.mu.Lock()
		r.report.MatchCleanupPending++
		r.mu.Unlock()
		return
	}
	r.mu.Lock()
	r.report.MatchAttemptsAborted++
	r.mu.Unlock()
	if err := r.acknowledgeNativeProcessNotStarted(ctx, match, completed.Data); err != nil {
		r.recordFailure("match_native_cleanup")
		r.mu.Lock()
		r.report.MatchCleanupPending++
		r.mu.Unlock()
		return
	}
	r.mu.Lock()
	r.report.MatchCleanupCleared++
	r.mu.Unlock()
}

func nativeProcessNotStartedCleanupBody(match matchFixture) (map[string]any, error) {
	if match.attemptID == "" || match.authoritySession == "" || match.rosterRevision < 1 || match.routeGeneration < 1 {
		return nil, fmt.Errorf("native not-started cleanup scope is incomplete")
	}
	return map[string]any{
		"world_instance_id":         "",
		"roster_revision":           match.rosterRevision,
		"route_generation":          match.routeGeneration,
		"evidence_kind":             "native_process_not_started",
		"owned_process_id":          uint32(0),
		"process_start_fingerprint": "",
	}, nil
}

func (r *Runner) acknowledgeNativeProcessNotStarted(ctx context.Context, match matchFixture, completed matchLobbySnapshot) error {
	if completed.LobbyID != match.lobbyID || completed.Attempt == nil ||
		completed.Attempt.AttemptID != match.attemptID ||
		completed.Attempt.RosterRevision != match.rosterRevision ||
		completed.Attempt.RouteGeneration != match.routeGeneration ||
		completed.Attempt.PayloadInstalled || completed.Attempt.WorldInstanceID != "" {
		return fmt.Errorf("backend complete response does not prove a pre-native cleanup scope")
	}
	body, err := nativeProcessNotStartedCleanupBody(match)
	if err != nil {
		return err
	}
	var cleared struct {
		Data matchLobbySnapshot `json:"data"`
	}
	if err := r.requestJSONAsWithHeaders(ctx, match.host, http.MethodPost,
		"/v1/match-attempts/"+match.attemptID+"/host/native-cleared",
		map[string]string{"X-Match-Authority-Session": match.authoritySession}, body, &cleared); err != nil {
		return err
	}
	if cleared.Data.LobbyID != match.lobbyID || cleared.Data.Attempt == nil ||
		cleared.Data.Attempt.AttemptID != match.attemptID ||
		cleared.Data.Attempt.RosterRevision != match.rosterRevision ||
		cleared.Data.Attempt.RouteGeneration != match.routeGeneration ||
		cleared.Data.Attempt.WorldInstanceID != "" || cleared.Data.Attempt.CleanupState != "CLEARED" {
		return fmt.Errorf("native cleanup acknowledgement did not return CLEARED for the exact attempt scope")
	}
	return nil
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

func (r *Runner) runMatchLobbyHeartbeats(ctx context.Context, matches []matchFixture) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, match := range matches {
				if err := r.requestJSONAsWithHeaders(ctx, match.host, http.MethodPost,
					"/v1/match-lobbies/"+match.lobbyID+"/presence",
					map[string]string{"X-Match-Transport-Host-Token": match.hostToken},
					map[string]bool{"online": true}, nil); err != nil && ctx.Err() == nil {
					r.recordFailure("match_lobby_presence")
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

func (r *Runner) requestJSONAsWithHeaders(
	ctx context.Context,
	client *virtualClient,
	method string,
	path string,
	headers map[string]string,
	body any,
	result any,
) error {
	return r.requestJSONAs(ctx, client, method, path, headers, body, result)
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
