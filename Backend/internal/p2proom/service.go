package p2proom

import (
	"context"
	"crypto/subtle"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/projectrebound/matchserver/internal/config"
	"github.com/projectrebound/matchserver/internal/player"
)

var labelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

type Service struct {
	repository        *Repository
	config            config.P2PRoomConfig
	connectionCreator ConnectionCreator
	now               func() time.Time
}

type ConnectionCreator interface {
	EnsureForRoomPeer(context.Context, string, string, string) error
	CloseForRoom(context.Context, string, string) error
}

func NewService(repository *Repository, cfg config.P2PRoomConfig) *Service {
	return &Service{repository: repository, config: cfg, now: time.Now}
}

func (s *Service) SetConnectionCreator(creator ConnectionCreator) {
	s.connectionCreator = creator
}

func (s *Service) Create(ctx context.Context, actor Actor, input CreateInput) (CreateResult, error) {
	if err := requireActive(actor); err != nil {
		return CreateResult{}, err
	}
	if err := s.validateCreate(input); err != nil {
		return CreateResult{}, err
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
	}
	tx, err := s.repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return CreateResult{}, internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := s.repository.Create(ctx, tx, room); err != nil {
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
	now := s.now().UTC()
	if err := s.repository.ActivateMember(ctx, tx, roomID, actor.PlayerID, now); err != nil {
		return Room{}, internal(err)
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
	_, err := s.repository.MarkRunning(ctx, roomID, s.now().UTC())
	if err == nil {
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
		return nil
	}
	return conflict("INVALID_ROOM_STATE", "Room cannot enter RUNNING from its current state.")
}

func (s *Service) ensureConnection(ctx context.Context, room Room, peerPlayerID string) error {
	if s.connectionCreator == nil || peerPlayerID == room.HostPlayerID {
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
		return s.repository.Heartbeat(ctx, tx, room.ID, now)
	})
}

func (s *Service) Start(ctx context.Context, actor Actor, roomID, hostToken string) (Room, error) {
	return s.hostOperation(ctx, actor, roomID, hostToken, func(ctx context.Context, tx pgx.Tx, room Room, now time.Time) (Room, error) {
		if room.State == StateConnecting || room.State == StateRunning {
			return room, nil
		}
		if room.State != StateLobby {
			return Room{}, conflict("INVALID_ROOM_STATE", "Room cannot start from its current state.")
		}
		return s.repository.Start(ctx, tx, room.ID, now)
	})
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
	if s.connectionCreator != nil {
		if err := s.connectionCreator.CloseForRoom(ctx, room.ID, "ROOM_CLOSED"); err != nil {
			return Room{}, internal(err)
		}
	}
	return room, nil
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
