package p2proom

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/config"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/entitlement"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/player"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/vnt"
	"github.com/jackc/pgx/v5"
)

var labelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
var idempotencyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{7,127}$`)

type Service struct {
	repository        *Repository
	config            config.P2PRoomConfig
	connectionCreator ConnectionCreator
	matchLifecycle    MatchLifecycle
	entitlements      interface {
		Has(context.Context, string, string) (bool, error)
	}
	vntNodes         *vnt.Repository
	vntSecrets       *SecretBox
	vntEnabled       bool
	vntVersionPolicy vnt.VersionPolicy
	vntLimiter       interface {
		Check(context.Context, vnt.LimitOperation, string) vnt.LimitDecision
	}
	now func() time.Time
}

type ConnectionCreator interface {
	EnsureForRoomPeer(context.Context, string, string, string) error
	CloseForRoom(context.Context, string, string) error
	RenewForRoom(context.Context, pgx.Tx, string, time.Time) error
}

type MatchStartRoom struct {
	ID           string
	HostPlayerID string
	Mode         string
}

type MatchLifecycle interface {
	EnsureForRoomStart(context.Context, pgx.Tx, MatchStartRoom, time.Time) error
	MarkRoomRunning(context.Context, string, time.Time) error
}

func NewService(repository *Repository, cfg config.P2PRoomConfig) *Service {
	return &Service{repository: repository, config: cfg, now: time.Now}
}

func (s *Service) SetConnectionCreator(creator ConnectionCreator) {
	s.connectionCreator = creator
}

func (s *Service) SetMatchLifecycle(lifecycle MatchLifecycle) {
	s.matchLifecycle = lifecycle
}

func (s *Service) SetEntitlementChecker(checker interface {
	Has(context.Context, string, string) (bool, error)
}) {
	s.entitlements = checker
}

func (s *Service) SetVNT(repository *vnt.Repository, secrets *SecretBox) {
	s.vntNodes = repository
	s.vntSecrets = secrets
}

func (s *Service) SetVNTEnabled(enabled bool) {
	s.vntEnabled = enabled
}

func (s *Service) SetVNTVersionPolicy(policy vnt.VersionPolicy) {
	s.vntVersionPolicy = policy
}

func (s *Service) SetVNTLimiter(limiter interface {
	Check(context.Context, vnt.LimitOperation, string) vnt.LimitDecision
}) {
	s.vntLimiter = limiter
}

func (s *Service) Create(ctx context.Context, actor Actor, input CreateInput) (CreateResult, error) {
	if err := requireActive(actor); err != nil {
		return CreateResult{}, err
	}
	if s.entitlements != nil {
		allowed, err := s.entitlements.Has(ctx, actor.PlayerID, entitlement.P2PRoomRegistration)
		if err != nil {
			return CreateResult{}, internal(err)
		}
		if !allowed {
			return CreateResult{}, forbidden(
				"P2P_ROOM_REGISTRATION_NOT_ALLOWED",
				"This player is not allowed to register P2P rooms.",
			)
		}
	}
	if input.TransportKind == "" {
		input.TransportKind = TransportLegacy
	}
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	if err := s.validateCreate(input); err != nil {
		return CreateResult{}, err
	}
	if input.TransportKind == TransportVNT && !s.vntEnabled {
		return CreateResult{}, conflict("VNT_FEATURE_DISABLED", "VNT rooms are not available.")
	}
	hostToken, hostTokenHash, err := newHostToken()
	if err != nil {
		return CreateResult{}, internal(err)
	}
	now := s.now().UTC()
	room := Room{
		ID: newID("room_"), HostPlayerID: actor.PlayerID, HostTokenHash: hostTokenHash,
		DisplayName: truncate(strings.TrimSpace(input.DisplayName), 128),
		Region:      strings.TrimSpace(input.Region), Mode: strings.TrimSpace(input.Mode),
		Version: strings.TrimSpace(input.Version), MaxPlayers: input.MaxPlayers,
		PlayerCount: 1, State: StateLobby, LastHeartbeatAt: now,
		CreatedAt: now, UpdatedAt: now,
		TransportKind: input.TransportKind, ExpiresAt: now.Add(8 * time.Hour),
	}
	requestHash := createRequestHash(room, input.VNTNodeID)
	tx, err := s.repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return CreateResult{}, internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if input.IdempotencyKey != "" {
		if s.vntSecrets == nil {
			return CreateResult{}, internal(errors.New("VNT secret box is not configured"))
		}
		if err := s.repository.LockIdempotency(ctx, tx, actor.PlayerID, input.IdempotencyKey); err != nil {
			return CreateResult{}, internal(err)
		}
		existingRoomID, existingHash, ciphertext, nonce, secretKeyID, findErr := s.repository.FindIdempotent(ctx, tx, actor.PlayerID, input.IdempotencyKey)
		if findErr == nil {
			if subtle.ConstantTimeCompare(existingHash, requestHash) != 1 {
				return CreateResult{}, conflict("IDEMPOTENCY_KEY_CONFLICT", "The idempotency key was already used for a different room request.")
			}
			existingRoom, err := s.repository.GetForUpdate(ctx, tx, existingRoomID)
			if err != nil {
				return CreateResult{}, internal(err)
			}
			plaintext, err := s.vntSecrets.Open(ciphertext, nonce, roomHostTokenAAD(existingRoomID, actor.PlayerID, input.IdempotencyKey), secretKeyID)
			if err != nil {
				return CreateResult{}, internal(err)
			}
			if err := tx.Commit(ctx); err != nil {
				return CreateResult{}, internal(err)
			}
			return CreateResult{Room: existingRoom, HostToken: string(plaintext), HeartbeatInterval: s.config.HeartbeatIntervalSeconds}, nil
		}
		if !errors.Is(findErr, pgx.ErrNoRows) {
			return CreateResult{}, internal(findErr)
		}
		ciphertext, nonce, keyID, err := s.vntSecrets.Seal([]byte(hostToken), roomHostTokenAAD(room.ID, actor.PlayerID, input.IdempotencyKey))
		if err != nil {
			return CreateResult{}, internal(err)
		}
		room.IdempotencyKey = input.IdempotencyKey
		room.IdempotencyRequestHash = requestHash
		room.HostTokenCiphertext = ciphertext
		room.HostTokenNonce = nonce
		room.HostTokenKeyID = keyID
	}
	var vntSession *VNTSession
	var hostSession *VNTMemberSession
	if input.TransportKind == TransportVNT {
		if s.vntNodes == nil || s.vntSecrets == nil {
			return CreateResult{}, conflict("VNT_FEATURE_DISABLED", "VNT rooms are not available.")
		}
		node, err := s.vntNodes.GetForAllocation(ctx, tx, strings.TrimSpace(input.VNTNodeID), now)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return CreateResult{}, conflict("VNT_NODE_UNAVAILABLE", "The selected VNT node is unavailable.")
			}
			return CreateResult{}, internal(err)
		}
		if node.State != vnt.StateOnline || node.ActiveRooms >= node.MaxRooms ||
			!s.vntVersionPolicy.Compatible(node) {
			return CreateResult{}, conflict("VNT_NODE_UNAVAILABLE", "The selected VNT node is unavailable.")
		}
		networkToken, err := newVNTSecret("vntk_")
		if err != nil {
			return CreateResult{}, internal(err)
		}
		e2ePassword, err := newVNTSecret("vntw_")
		if err != nil {
			return CreateResult{}, internal(err)
		}
		generation := 1
		networkCipher, networkNonce, keyID, err := s.vntSecrets.Seal(
			[]byte(networkToken), vntSecretAAD(room.ID, generation, node.ID, "network_token"),
		)
		if err != nil {
			return CreateResult{}, internal(err)
		}
		passwordCipher, passwordNonce, _, err := s.vntSecrets.Seal(
			[]byte(e2ePassword), vntSecretAAD(room.ID, generation, node.ID, "e2e_password"),
		)
		if err != nil {
			return CreateResult{}, internal(err)
		}
		vntSession = &VNTSession{
			RoomID: room.ID, NodeID: node.ID, Generation: generation, State: "SELECTED",
			NodeHost: node.AdvertisedHost, NodePort: node.Port, NodeRegion: node.Region,
			NodeLocation: node.Location, NodeFingerprint: node.ServerKeyFingerprint,
			NodeTransports: node.SupportedTransports, NetworkTokenCiphertext: networkCipher,
			NetworkTokenNonce: networkNonce, E2EPasswordCiphertext: passwordCipher,
			E2EPasswordNonce: passwordNonce, SecretKeyID: keyID, CreatedAt: now, UpdatedAt: now,
		}
		hostSession = &VNTMemberSession{
			RoomID: room.ID, Generation: generation, PlayerID: actor.PlayerID,
			DeviceID: newID("vnd_"), VirtualIP: "10.26.0.2", State: "ISSUED", CreatedAt: now,
		}
		room.VNTNodeID = node.ID
		room.VNTHost = node.AdvertisedHost
		room.VNTPort = node.Port
		room.VNTRegion = node.Region
		room.VNTLocation = node.Location
		room.VNTState = "SELECTED"
		room.VNTGeneration = generation
	}
	if err := s.repository.Create(ctx, tx, room, vntSession, hostSession); err != nil {
		return CreateResult{}, internal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return CreateResult{}, internal(err)
	}
	return CreateResult{Room: room, HostToken: hostToken, HeartbeatInterval: s.config.HeartbeatIntervalSeconds}, nil
}

func (s *Service) Get(ctx context.Context, roomID string) (Room, error) {
	room, err := s.repository.Get(ctx, roomID)
	if err != nil {
		return Room{}, mapRoomError(err)
	}
	return room, nil
}

func (s *Service) List(ctx context.Context, filter ListFilter) (ListResult, error) {
	if filter.Limit == 0 {
		filter.Limit = 50
	}
	if filter.Limit < 1 || filter.Limit > 100 {
		return ListResult{}, invalid("Invalid limit.", map[string]any{"limit": "must be between 1 and 100"})
	}
	for name, value := range map[string]string{"region": filter.Region, "mode": filter.Mode, "version": filter.Version} {
		if value != "" && !labelPattern.MatchString(value) {
			return ListResult{}, invalid("Invalid filter.", map[string]any{name: "contains unsupported characters"})
		}
	}
	if filter.State != "" && !isState(filter.State) {
		return ListResult{}, invalid("Invalid state filter.", nil)
	}
	hasSlots := -1
	if filter.HasSlots != nil {
		if *filter.HasSlots {
			hasSlots = 1
		} else {
			hasSlots = 0
		}
	}
	limit := filter.Limit
	filter.Limit++
	items, err := s.repository.List(ctx, filter, hasSlots)
	if err != nil {
		return ListResult{}, internal(err)
	}
	nextCursor := ""
	if len(items) > limit {
		nextCursor = items[limit-1].ID
		items = items[:limit]
	}
	return ListResult{Items: items, NextCursor: nextCursor}, nil
}

func (s *Service) Join(ctx context.Context, actor Actor, roomID, version string) (Room, error) {
	if err := requireActive(actor); err != nil {
		return Room{}, err
	}
	tx, err := s.repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Room{}, internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	room, err := s.repository.GetForUpdate(ctx, tx, roomID)
	if err != nil {
		return Room{}, mapRoomError(err)
	}
	now := s.now().UTC()
	if !room.ExpiresAt.After(now) {
		return Room{}, conflict("ROOM_EXPIRED", "Room has expired.")
	}
	if room.State != StateLobby {
		return Room{}, conflict("ROOM_NOT_JOINABLE", "Room is not accepting new members.")
	}
	if strings.TrimSpace(version) != room.Version {
		return Room{}, conflict("VERSION_MISMATCH", "Client and room versions do not match.")
	}
	member, err := s.repository.GetMemberForUpdate(ctx, tx, roomID, actor.PlayerID)
	if err == nil && member.Status == "ACTIVE" {
		if err := tx.Commit(ctx); err != nil {
			return Room{}, internal(err)
		}
		if err := s.ensureConnection(ctx, room, actor.PlayerID); err != nil {
			return Room{}, err
		}
		return room, nil
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Room{}, internal(err)
	}
	if room.PlayerCount >= room.MaxPlayers {
		return Room{}, conflict("ROOM_FULL", "Room is full.")
	}
	if err := s.repository.ActivateMember(ctx, tx, roomID, actor.PlayerID, now); err != nil {
		return Room{}, internal(err)
	}
	if room.TransportKind == TransportVNT {
		virtualIP, err := s.repository.NextVNTVirtualIP(ctx, tx, room.ID, room.VNTGeneration)
		if err != nil {
			return Room{}, internal(err)
		}
		if err := s.repository.ActivateVNTMember(ctx, tx, VNTMemberSession{
			RoomID: room.ID, Generation: room.VNTGeneration, PlayerID: actor.PlayerID,
			DeviceID: newID("vnd_"), VirtualIP: virtualIP,
			State: "ISSUED", CreatedAt: now,
		}); err != nil {
			return Room{}, internal(err)
		}
	}
	room, err = s.repository.UpdatePlayerCount(ctx, tx, roomID, 1, now)
	if err != nil {
		return Room{}, internal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Room{}, internal(err)
	}
	if err := s.ensureConnection(ctx, room, actor.PlayerID); err != nil {
		return Room{}, err
	}
	return room, nil
}

func (s *Service) ResolveConnectionParticipants(ctx context.Context, roomID, actorPlayerID, requestedPeerPlayerID string) (string, string, error) {
	room, err := s.repository.Get(ctx, roomID)
	if err != nil {
		return "", "", mapRoomError(err)
	}
	if room.State == StateStale || room.State == StateClosed {
		return "", "", conflict("ROOM_NOT_CONNECTABLE", "Room is not accepting connection sessions.")
	}
	peerPlayerID := strings.TrimSpace(requestedPeerPlayerID)
	if actorPlayerID == room.HostPlayerID {
		if peerPlayerID == "" || peerPlayerID == room.HostPlayerID {
			return "", "", invalid("Host connection requests require a peer_player_id.", nil)
		}
	} else {
		if peerPlayerID != "" && peerPlayerID != actorPlayerID {
			return "", "", forbidden("CONNECTION_FORBIDDEN", "Players may only create their own room connection.")
		}
		peerPlayerID = actorPlayerID
	}
	member, err := s.repository.GetMember(ctx, roomID, peerPlayerID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", "", forbidden("CONNECTION_FORBIDDEN", "An active room membership is required.")
		}
		return "", "", internal(err)
	}
	if member.Role == "HOST" || member.Status != "ACTIVE" {
		return "", "", forbidden("CONNECTION_FORBIDDEN", "An active peer room membership is required.")
	}
	return room.HostPlayerID, peerPlayerID, nil
}

func (s *Service) MarkConnectionEstablished(ctx context.Context, roomID string) error {
	now := s.now().UTC()
	_, err := s.repository.MarkRunning(ctx, roomID, now)
	if err == nil {
		if s.matchLifecycle != nil {
			if lifecycleErr := s.matchLifecycle.MarkRoomRunning(ctx, roomID, now); lifecycleErr != nil {
				return internal(lifecycleErr)
			}
		}
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return internal(err)
	}
	room, getErr := s.repository.Get(ctx, roomID)
	if getErr != nil {
		return mapRoomError(getErr)
	}
	if room.State == StateRunning {
		if s.matchLifecycle != nil {
			if lifecycleErr := s.matchLifecycle.MarkRoomRunning(ctx, roomID, now); lifecycleErr != nil {
				return internal(lifecycleErr)
			}
		}
		return nil
	}
	return conflict("INVALID_ROOM_STATE", "Room cannot enter RUNNING from its current state.")
}

func (s *Service) RelayRegion(ctx context.Context, roomID string) (string, error) {
	room, err := s.repository.Get(ctx, roomID)
	if err != nil {
		return "", mapRoomError(err)
	}
	if room.State == StateClosed {
		return "", conflict("ROOM_CLOSED", "Room is closed.")
	}
	return room.Region, nil
}

func (s *Service) ensureConnection(ctx context.Context, room Room, peerPlayerID string) error {
	if room.TransportKind == TransportVNT || s.connectionCreator == nil || peerPlayerID == room.HostPlayerID {
		return nil
	}
	if err := s.connectionCreator.EnsureForRoomPeer(ctx, room.ID, room.HostPlayerID, peerPlayerID); err != nil {
		return internal(err)
	}
	return nil
}

func (s *Service) Leave(ctx context.Context, actor Actor, roomID string) (Room, error) {
	if err := requireActive(actor); err != nil {
		return Room{}, err
	}
	tx, err := s.repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Room{}, internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	room, err := s.repository.GetForUpdate(ctx, tx, roomID)
	if err != nil {
		return Room{}, mapRoomError(err)
	}
	member, err := s.repository.GetMemberForUpdate(ctx, tx, roomID, actor.PlayerID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Room{}, &ServiceError{Status: 404, Code: "ROOM_MEMBERSHIP_NOT_FOUND", Message: "Room membership not found."}
		}
		return Room{}, internal(err)
	}
	if member.Role == "HOST" {
		return Room{}, conflict("HOST_CANNOT_LEAVE", "The host must close the room.")
	}
	if member.Status == "LEFT" {
		if err := tx.Commit(ctx); err != nil {
			return Room{}, internal(err)
		}
		return room, nil
	}
	now := s.now().UTC()
	if err := s.repository.MarkMemberLeft(ctx, tx, roomID, actor.PlayerID, now); err != nil {
		return Room{}, internal(err)
	}
	if room.TransportKind == TransportVNT {
		if err := s.repository.StopVNTMember(ctx, tx, roomID, actor.PlayerID, now); err != nil {
			return Room{}, internal(err)
		}
	}
	room, err = s.repository.UpdatePlayerCount(ctx, tx, roomID, -1, now)
	if err != nil {
		return Room{}, internal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Room{}, internal(err)
	}
	return room, nil
}

func (s *Service) Heartbeat(ctx context.Context, actor Actor, roomID, hostToken string) (Room, error) {
	return s.hostOperation(ctx, actor, roomID, hostToken, func(ctx context.Context, tx pgx.Tx, room Room, now time.Time) (Room, error) {
		if room.State == StateClosed {
			return Room{}, conflict("ROOM_CLOSED", "Room is closed.")
		}
		if now.After(room.ExpiresAt) {
			return Room{}, conflict("ROOM_EXPIRED", "Room has expired.")
		}
		updated, err := s.repository.Heartbeat(ctx, tx, room.ID, now)
		if err != nil {
			return Room{}, err
		}
		if room.TransportKind != TransportVNT && s.connectionCreator != nil {
			if err := s.connectionCreator.RenewForRoom(ctx, tx, room.ID, now); err != nil {
				return Room{}, internal(err)
			}
		}
		return updated, nil
	})
}

func (s *Service) Start(ctx context.Context, actor Actor, roomID, hostToken string) (Room, error) {
	room, err := s.hostOperation(ctx, actor, roomID, hostToken, func(ctx context.Context, tx pgx.Tx, room Room, now time.Time) (Room, error) {
		if !room.ExpiresAt.After(now) {
			return Room{}, conflict("ROOM_EXPIRED", "Room has expired.")
		}
		if room.State == StateConnecting || room.State == StateRunning {
			if s.matchLifecycle != nil {
				if err := s.matchLifecycle.EnsureForRoomStart(ctx, tx, MatchStartRoom{
					ID: room.ID, HostPlayerID: room.HostPlayerID, Mode: room.Mode,
				}, now); err != nil {
					return Room{}, internal(err)
				}
			}
			return room, nil
		}
		if room.State != StateLobby {
			return Room{}, conflict("INVALID_ROOM_STATE", "Room cannot start from its current state.")
		}
		if room.TransportKind == TransportVNT && room.VNTState != "HOST_READY" && room.VNTState != "READY" {
			return Room{}, conflict("VNT_HOST_NOT_READY", "The VNT host is not ready.")
		}
		updated, err := s.repository.Start(ctx, tx, room.ID, now)
		if err != nil {
			return Room{}, err
		}
		if s.matchLifecycle != nil {
			if err := s.matchLifecycle.EnsureForRoomStart(ctx, tx, MatchStartRoom{
				ID: room.ID, HostPlayerID: room.HostPlayerID, Mode: room.Mode,
			}, now); err != nil {
				return Room{}, internal(err)
			}
		}
		return updated, nil
	})
	if err != nil {
		return Room{}, err
	}
	if room.TransportKind == TransportVNT && s.matchLifecycle != nil {
		if err := s.matchLifecycle.MarkRoomRunning(ctx, room.ID, s.now().UTC()); err != nil {
			return Room{}, internal(err)
		}
	}
	return room, nil
}

func (s *Service) Delete(ctx context.Context, actor Actor, roomID, hostToken string) (Room, error) {
	room, err := s.hostOperation(ctx, actor, roomID, hostToken, func(ctx context.Context, tx pgx.Tx, room Room, now time.Time) (Room, error) {
		if room.State == StateClosed {
			return room, nil
		}
		return s.repository.Close(ctx, tx, room.ID, now)
	})
	if err != nil {
		return Room{}, err
	}
	if room.TransportKind != TransportVNT && s.connectionCreator != nil {
		if err := s.connectionCreator.CloseForRoom(ctx, room.ID, "ROOM_CLOSED"); err != nil {
			return Room{}, internal(err)
		}
	}
	return room, nil
}

func (s *Service) VNTBootstrap(ctx context.Context, actor Actor, roomID string) (VNTBootstrap, error) {
	if err := requireActive(actor); err != nil {
		return VNTBootstrap{}, err
	}
	if err := s.checkVNTLimit(ctx, vnt.LimitBootstrap, actor.PlayerID); err != nil {
		return VNTBootstrap{}, err
	}
	if s.vntSecrets == nil {
		return VNTBootstrap{}, conflict("VNT_FEATURE_DISABLED", "VNT rooms are not available.")
	}
	tx, err := s.repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return VNTBootstrap{}, internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	room, err := s.repository.GetForUpdate(ctx, tx, roomID)
	if err != nil {
		return VNTBootstrap{}, mapRoomError(err)
	}
	if room.TransportKind != TransportVNT {
		return VNTBootstrap{}, conflict("VNT_ROOM_REQUIRED", "This is not a VNT room.")
	}
	now := s.now().UTC()
	if room.State == StateClosed {
		return VNTBootstrap{}, conflict("ROOM_CLOSED", "Room is closed.")
	}
	if !room.ExpiresAt.After(now) {
		return VNTBootstrap{}, conflict("ROOM_EXPIRED", "Room has expired.")
	}
	member, err := s.repository.GetMemberForUpdate(ctx, tx, roomID, actor.PlayerID)
	if err != nil || member.Status != "ACTIVE" {
		return VNTBootstrap{}, forbidden("VNT_BOOTSTRAP_FORBIDDEN", "An active room membership is required.")
	}
	session, err := s.repository.GetVNTSession(ctx, tx, roomID)
	if err != nil {
		return VNTBootstrap{}, internal(err)
	}
	memberSession, err := s.repository.GetVNTMember(ctx, tx, roomID, session.Generation, actor.PlayerID)
	if err != nil {
		return VNTBootstrap{}, internal(err)
	}
	if session.State == "FAILED" || session.State == "CLOSED" {
		return VNTBootstrap{}, conflict("VNT_SESSION_UNAVAILABLE", "The VNT session is no longer available.")
	}
	if member.Role != "HOST" && session.State != "HOST_READY" && session.State != "READY" && session.State != "ACTIVE" {
		return VNTBootstrap{}, conflict("VNT_HOST_NOT_READY", "The VNT host is not ready.")
	}
	networkToken, err := s.vntSecrets.Open(session.NetworkTokenCiphertext, session.NetworkTokenNonce,
		vntSecretAAD(room.ID, session.Generation, session.NodeID, "network_token"), session.SecretKeyID)
	if err != nil {
		s.recordVNTSecurityFailure(ctx, "VNT_ROOM_SECRET_DECRYPTION_FAILED", actor.PlayerID, session.NodeID, room.ID,
			"NETWORK_TOKEN_DECRYPTION_FAILED", map[string]any{
				"secret_kind": "network_token", "secret_key_id": session.SecretKeyID,
				"generation": session.Generation,
			})
		return VNTBootstrap{}, internal(err)
	}
	e2ePassword, err := s.vntSecrets.Open(session.E2EPasswordCiphertext, session.E2EPasswordNonce,
		vntSecretAAD(room.ID, session.Generation, session.NodeID, "e2e_password"), session.SecretKeyID)
	if err != nil {
		s.recordVNTSecurityFailure(ctx, "VNT_ROOM_SECRET_DECRYPTION_FAILED", actor.PlayerID, session.NodeID, room.ID,
			"E2E_PASSWORD_DECRYPTION_FAILED", map[string]any{
				"secret_kind": "e2e_password", "secret_key_id": session.SecretKeyID,
				"generation": session.Generation,
			})
		return VNTBootstrap{}, internal(err)
	}
	var hostVirtualIP *string
	if member.Role != "HOST" && session.HostVirtualIP != "" {
		value := session.HostVirtualIP
		hostVirtualIP = &value
	}
	deviceName := "room-member"
	if member.Role == "HOST" {
		deviceName = "room-host"
	}
	if err := tx.Commit(ctx); err != nil {
		return VNTBootstrap{}, internal(err)
	}
	return VNTBootstrap{
		RoomID: room.ID, Generation: session.Generation, ExpiresAt: room.ExpiresAt,
		Server: VNTServerEndpoint{
			Address:              net.JoinHostPort(session.NodeHost, fmt.Sprintf("%d", session.NodePort)),
			ServerKeyFingerprint: session.NodeFingerprint,
			SupportedTransports:  session.NodeTransports,
		},
		NetworkToken: string(networkToken), E2EPassword: string(e2ePassword),
		CipherModel: "chacha20_poly1305", ServerEncrypt: true,
		DeviceID: memberSession.DeviceID, DeviceName: deviceName,
		VirtualIP: memberSession.VirtualIP, HostVirtualIP: hostVirtualIP, MTU: 1410,
	}, nil
}

func (s *Service) UpdateVNTPresence(ctx context.Context, actor Actor, roomID string, input VNTPresenceInput) (Room, error) {
	if err := requireActive(actor); err != nil {
		return Room{}, err
	}
	input.State = strings.ToUpper(strings.TrimSpace(input.State))
	input.ObservedPath = strings.ToUpper(strings.TrimSpace(input.ObservedPath))
	if !containsString([]string{"ISSUED", "CONNECTING", "CONNECTED", "FAILED", "STOPPED"}, input.State) ||
		(input.ObservedPath != "" && !containsString([]string{"P2P", "RELAY", "UNKNOWN"}, input.ObservedPath)) ||
		net.ParseIP(strings.TrimSpace(input.VirtualIP)) == nil || len(input.ReasonCode) > 64 {
		return Room{}, invalid("Invalid VNT presence.", nil)
	}
	tx, err := s.repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Room{}, internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	room, err := s.repository.GetForUpdate(ctx, tx, roomID)
	if err != nil {
		return Room{}, mapRoomError(err)
	}
	if room.TransportKind != TransportVNT {
		return Room{}, conflict("VNT_ROOM_REQUIRED", "This is not a VNT room.")
	}
	now := s.now().UTC()
	if !room.ExpiresAt.After(now) {
		return Room{}, conflict("ROOM_EXPIRED", "Room has expired.")
	}
	if input.Generation != room.VNTGeneration {
		return Room{}, conflict("VNT_GENERATION_STALE", "The VNT generation has changed.")
	}
	member, err := s.repository.GetMemberForUpdate(ctx, tx, roomID, actor.PlayerID)
	if err != nil || member.Status != "ACTIVE" {
		return Room{}, forbidden("VNT_PRESENCE_FORBIDDEN", "An active room membership is required.")
	}
	if err := s.repository.UpdateVNTPresence(ctx, tx, roomID, input, actor.PlayerID, now); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Room{}, conflict("VNT_PRESENCE_MISMATCH", "VNT presence does not match the issued slot.")
		}
		return Room{}, internal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Room{}, internal(err)
	}
	return s.repository.Get(ctx, roomID)
}

func (s *Service) VNTHostReady(ctx context.Context, actor Actor, roomID, hostToken string, generation int, virtualIP string) (Room, error) {
	virtualIP = strings.TrimSpace(virtualIP)
	if virtualIP != "10.26.0.2" {
		return Room{}, invalid("Invalid VNT host virtual IP.", nil)
	}
	return s.hostOperation(ctx, actor, roomID, hostToken, func(ctx context.Context, tx pgx.Tx, room Room, now time.Time) (Room, error) {
		if room.TransportKind != TransportVNT {
			return Room{}, conflict("VNT_ROOM_REQUIRED", "This is not a VNT room.")
		}
		if generation != room.VNTGeneration {
			return Room{}, conflict("VNT_GENERATION_STALE", "The VNT generation has changed.")
		}
		if !room.ExpiresAt.After(now) {
			return Room{}, conflict("ROOM_EXPIRED", "Room has expired.")
		}
		if err := s.repository.MarkVNTHostReady(ctx, tx, room.ID, generation, virtualIP, now); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return Room{}, conflict("VNT_HOST_READY_CONFLICT", "VNT host readiness conflicts with current state.")
			}
			return Room{}, internal(err)
		}
		room.VNTState = "HOST_READY"
		return room, nil
	})
}

func (s *Service) VNTRebind(ctx context.Context, actor Actor, roomID, hostToken, nodeID string) (Room, error) {
	if !s.vntEnabled || s.vntNodes == nil || s.vntSecrets == nil {
		return Room{}, conflict("VNT_FEATURE_DISABLED", "VNT rooms are not available.")
	}
	requestedNodeID := strings.TrimSpace(nodeID)
	result, err := s.hostOperation(ctx, actor, roomID, hostToken, func(ctx context.Context, tx pgx.Tx, room Room, now time.Time) (Room, error) {
		if room.TransportKind != TransportVNT {
			return Room{}, conflict("VNT_ROOM_REQUIRED", "This is not a VNT room.")
		}
		if room.State != StateLobby {
			return Room{}, conflict("VNT_REBIND_NOT_ALLOWED", "VNT nodes can only be changed before the room starts.")
		}
		if !room.ExpiresAt.After(now) {
			return Room{}, conflict("ROOM_EXPIRED", "Room has expired.")
		}
		node, err := s.vntNodes.GetForAllocation(ctx, tx, requestedNodeID, now)
		if err != nil || node.State != vnt.StateOnline || node.ActiveRooms >= node.MaxRooms ||
			!s.vntVersionPolicy.Compatible(node) {
			return Room{}, conflict("VNT_NODE_UNAVAILABLE", "The selected VNT node is unavailable.")
		}
		previousNodeID := room.VNTNodeID
		previousGeneration := room.VNTGeneration
		generation := previousGeneration + 1
		networkToken, err := newVNTSecret("vntk_")
		if err != nil {
			return Room{}, internal(err)
		}
		e2ePassword, err := newVNTSecret("vntw_")
		if err != nil {
			return Room{}, internal(err)
		}
		networkCipher, networkNonce, keyID, err := s.vntSecrets.Seal([]byte(networkToken), vntSecretAAD(room.ID, generation, node.ID, "network_token"))
		if err != nil {
			return Room{}, internal(err)
		}
		passwordCipher, passwordNonce, _, err := s.vntSecrets.Seal([]byte(e2ePassword), vntSecretAAD(room.ID, generation, node.ID, "e2e_password"))
		if err != nil {
			return Room{}, internal(err)
		}
		session := VNTSession{
			RoomID: room.ID, NodeID: node.ID, Generation: generation, State: "SELECTED",
			NodeHost: node.AdvertisedHost, NodePort: node.Port, NodeRegion: node.Region,
			NodeLocation: node.Location, NodeFingerprint: node.ServerKeyFingerprint,
			NodeTransports: node.SupportedTransports, NetworkTokenCiphertext: networkCipher,
			NetworkTokenNonce: networkNonce, E2EPasswordCiphertext: passwordCipher,
			E2EPasswordNonce: passwordNonce, SecretKeyID: keyID,
		}
		if err := s.repository.RebindVNT(ctx, tx, session, now); err != nil {
			return Room{}, internal(err)
		}
		meta := vnt.RequestMetaFromContext(ctx)
		if err := s.vntNodes.InsertSecurityAudit(ctx, tx, vnt.SecurityAudit{
			ID: vnt.NewSecurityAuditID(), EventType: "VNT_ROOM_REBOUND", Result: vnt.AuditSucceeded,
			ActorType: vnt.AuditActorPlayer, PlayerID: actor.PlayerID, NodeID: node.ID, RoomID: room.ID,
			RequestID: meta.RequestID, IPAddress: meta.IPAddress, UserAgent: meta.UserAgent,
			Details: map[string]any{
				"previous_node_id": previousNodeID, "new_node_id": node.ID,
				"previous_generation": previousGeneration, "new_generation": generation,
			}, CreatedAt: now,
		}); err != nil {
			return Room{}, internal(err)
		}
		room.VNTNodeID = node.ID
		room.VNTHost = node.AdvertisedHost
		room.VNTPort = node.Port
		room.VNTRegion = node.Region
		room.VNTLocation = node.Location
		room.VNTState = "SELECTED"
		room.VNTGeneration = generation
		return room, nil
	})
	if err != nil {
		s.recordVNTSecurityError(ctx, "VNT_ROOM_REBIND_REJECTED", actor.PlayerID, requestedNodeID, roomID, err,
			map[string]any{"requested_node_id": requestedNodeID})
		return Room{}, err
	}
	return result, nil
}

func (s *Service) recordVNTSecurityFailure(
	ctx context.Context,
	eventType, playerID, nodeID, roomID, reasonCode string,
	details map[string]any,
) {
	s.recordVNTSecurityAudit(ctx, eventType, vnt.AuditFailed, playerID, nodeID, roomID, reasonCode, details)
}

func (s *Service) checkVNTLimit(ctx context.Context, operation vnt.LimitOperation, identity string) error {
	if s.vntLimiter == nil {
		return nil
	}
	decision := s.vntLimiter.Check(ctx, operation, identity)
	if decision.Allowed {
		return nil
	}
	retryAfterSeconds := max(1, int((decision.RetryAfter+time.Second-1)/time.Second))
	return &ServiceError{
		Status: http.StatusTooManyRequests, Code: "VNT_RATE_LIMITED", Message: "Too many VNT requests.",
		Details: map[string]any{"operation": operation, "retry_after_seconds": retryAfterSeconds},
	}
}

func (s *Service) recordVNTSecurityError(
	ctx context.Context,
	eventType, playerID, nodeID, roomID string,
	err error,
	details map[string]any,
) {
	status, reasonCode, _, _ := errorDetails(err)
	result := vnt.AuditDenied
	if status >= http.StatusInternalServerError {
		result = vnt.AuditFailed
	}
	s.recordVNTSecurityAudit(ctx, eventType, result, playerID, nodeID, roomID, reasonCode, details)
}

func (s *Service) recordVNTSecurityAudit(
	ctx context.Context,
	eventType, result, playerID, nodeID, roomID, reasonCode string,
	details map[string]any,
) {
	if s.vntNodes == nil {
		return
	}
	meta := vnt.RequestMetaFromContext(ctx)
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	_ = s.vntNodes.RecordSecurityAudit(auditCtx, vnt.SecurityAudit{
		ID: vnt.NewSecurityAuditID(), EventType: eventType, Result: result,
		ActorType: vnt.AuditActorPlayer, PlayerID: playerID, NodeID: nodeID, RoomID: roomID,
		RequestID: meta.RequestID, IPAddress: meta.IPAddress, UserAgent: meta.UserAgent,
		ReasonCode: reasonCode, Details: details, CreatedAt: s.now().UTC(),
	})
}

func vntSecretAAD(roomID string, generation int, nodeID, kind string) []byte {
	return []byte(fmt.Sprintf("%s:%d:%s:%s", roomID, generation, nodeID, kind))
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func createRequestHash(room Room, vntNodeID string) []byte {
	canonical := fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%d\x00%s\x00%s",
		room.DisplayName, room.Region, room.Mode, room.Version, room.MaxPlayers,
		room.TransportKind, strings.TrimSpace(vntNodeID))
	hash := sha256.Sum256([]byte(canonical))
	return hash[:]
}

func roomHostTokenAAD(roomID, playerID, key string) []byte {
	return []byte("p2p-host-token:" + roomID + ":" + playerID + ":" + key)
}

func (s *Service) hostOperation(
	ctx context.Context,
	actor Actor,
	roomID string,
	hostToken string,
	operation func(context.Context, pgx.Tx, Room, time.Time) (Room, error),
) (Room, error) {
	if err := requireActive(actor); err != nil {
		return Room{}, err
	}
	if !strings.HasPrefix(hostToken, "p2h_") || len(hostToken) < 64 {
		return Room{}, forbidden("HOST_UNAUTHORIZED", "Valid room host credentials are required.")
	}
	tx, err := s.repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Room{}, internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	room, err := s.repository.GetForUpdate(ctx, tx, roomID)
	if err != nil {
		return Room{}, mapRoomError(err)
	}
	if room.HostPlayerID != actor.PlayerID || subtle.ConstantTimeCompare(room.HostTokenHash, hashHostToken(hostToken)) != 1 {
		return Room{}, forbidden("HOST_UNAUTHORIZED", "Valid room host credentials are required.")
	}
	updated, err := operation(ctx, tx, room, s.now().UTC())
	if err != nil {
		return Room{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Room{}, internal(err)
	}
	return updated, nil
}

func (s *Service) SweepStale(ctx context.Context) (int64, error) {
	count, closedRoomIDs, err := s.repository.SweepStale(
		ctx,
		s.now().UTC(),
		time.Duration(s.config.StaleAfterSeconds)*time.Second,
		time.Duration(s.config.ClosedAfterSeconds)*time.Second,
	)
	if err != nil {
		return 0, err
	}
	if s.connectionCreator != nil {
		for _, roomID := range closedRoomIDs {
			if err := s.connectionCreator.CloseForRoom(ctx, roomID, "ROOM_HEARTBEAT_EXPIRED"); err != nil {
				return count, err
			}
		}
	}
	return count, nil
}

func (s *Service) HeartbeatInterval() int { return s.config.HeartbeatIntervalSeconds }

func (s *Service) validateCreate(input CreateInput) error {
	details := make(map[string]any)
	if strings.TrimSpace(input.DisplayName) == "" {
		details["display_name"] = "is required"
	}
	for name, value := range map[string]string{"region": input.Region, "mode": input.Mode, "version": input.Version} {
		if !labelPattern.MatchString(strings.TrimSpace(value)) {
			details[name] = "contains unsupported characters or has invalid length"
		}
	}
	if input.MaxPlayers < 2 || input.MaxPlayers > s.config.MaximumPlayers {
		details["max_players"] = "is outside the configured capacity"
	}
	if input.TransportKind != TransportLegacy && input.TransportKind != TransportVNT {
		details["transport_kind"] = "must be LEGACY_RELAY or VNT"
	}
	if input.TransportKind == TransportVNT && strings.TrimSpace(input.VNTNodeID) == "" {
		details["vnt_node_id"] = "is required for VNT rooms"
	}
	if input.IdempotencyKey != "" && !idempotencyPattern.MatchString(strings.TrimSpace(input.IdempotencyKey)) {
		details["idempotency_key"] = "must contain 8 to 128 safe characters"
	}
	if len(details) > 0 {
		return invalid("Invalid P2P room.", details)
	}
	return nil
}

func requireActive(actor Actor) error {
	if actor.PlayerID == "" {
		return &ServiceError{Status: 401, Code: "UNAUTHORIZED", Message: "Authentication is required."}
	}
	if actor.AccountStatus != player.AccountStatusActive {
		return forbidden("ACCOUNT_NOT_ACTIVE", "An active account is required.")
	}
	return nil
}

func mapRoomError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return &ServiceError{Status: 404, Code: "ROOM_NOT_FOUND", Message: "Room not found."}
	}
	return internal(err)
}

func isState(state State) bool {
	switch state {
	case StateLobby, StateConnecting, StateRunning, StateStale, StateClosed:
		return true
	default:
		return false
	}
}

func truncate(value string, maximum int) string {
	runes := []rune(value)
	if len(runes) <= maximum {
		return value
	}
	return string(runes[:maximum])
}
