package gameserver

import (
	"context"
	"errors"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/projectrebound/matchserver/internal/config"
)

var labelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
var instancePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

type Service struct {
	repository *Repository
	config     config.GameServerConfig
	now        func() time.Time
}

func NewService(repository *Repository, cfg config.GameServerConfig) *Service {
	return &Service{repository: repository, config: cfg, now: time.Now}
}

func (s *Service) Register(ctx context.Context, input RegistrationInput, issuer string) (RegistrationResult, error) {
	if err := validateRegistration(input, issuer); err != nil {
		return RegistrationResult{}, err
	}
	token, tokenHash, err := newServerToken()
	if err != nil {
		return RegistrationResult{}, internal(err)
	}
	now := s.now().UTC()
	item, err := s.repository.Register(ctx, Server{
		ID:                 newID("gs_"),
		InstanceID:         strings.TrimSpace(input.InstanceID),
		DisplayName:        truncate(strings.TrimSpace(input.DisplayName), 128),
		Region:             strings.TrimSpace(input.Region),
		Mode:               strings.TrimSpace(input.Mode),
		Version:            strings.TrimSpace(input.Version),
		PublicHost:         strings.TrimSpace(input.PublicHost),
		PublicPort:         input.PublicPort,
		MaxPlayers:         input.MaxPlayers,
		State:              StateStarting,
		ServerTokenHash:    tokenHash,
		RegistrationIssuer: issuer,
		TokenExpiresAt:     now.Add(s.config.ServerTokenTTL()),
		LastHeartbeatAt:    now,
		CreatedAt:          now,
		UpdatedAt:          now,
	})
	if err != nil {
		return RegistrationResult{}, internal(err)
	}
	return RegistrationResult{
		Server:            item,
		ServerToken:       token,
		HeartbeatInterval: s.config.HeartbeatIntervalSeconds,
	}, nil
}

func (s *Service) Heartbeat(ctx context.Context, serverID, serverToken string, input HeartbeatInput) (Server, error) {
	if !isReportedState(input.State) {
		return Server{}, invalid("Invalid game server state.", map[string]any{"state": "must be STARTING, READY, RESERVED, RUNNING, or DRAINING"})
	}
	if !strings.HasPrefix(serverToken, "gst_") || len(serverToken) < 64 {
		return Server{}, unauthorized()
	}
	tx, err := s.repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Server{}, internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	now := s.now().UTC()
	current, err := s.repository.GetForManagement(ctx, tx, serverID, hashServerToken(serverToken), now)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Server{}, unauthorized()
		}
		return Server{}, internal(err)
	}
	if input.PlayerCount < 0 || input.PlayerCount > current.MaxPlayers {
		return Server{}, invalid("Invalid player count.", map[string]any{"player_count": "must be within server capacity"})
	}
	if !validTransition(current.State, input.State) {
		return Server{}, &ServiceError{Status: http.StatusConflict, Code: "INVALID_STATE_TRANSITION", Message: "Invalid game server state transition."}
	}
	updated, err := s.repository.UpdateHeartbeat(ctx, tx, serverID, input.State, input.PlayerCount, now)
	if err != nil {
		return Server{}, internal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Server{}, internal(err)
	}
	return updated, nil
}

func (s *Service) Deregister(ctx context.Context, serverID, serverToken string) error {
	if !strings.HasPrefix(serverToken, "gst_") || len(serverToken) < 64 {
		return unauthorized()
	}
	tx, err := s.repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	now := s.now().UTC()
	if _, err := s.repository.GetForManagement(ctx, tx, serverID, hashServerToken(serverToken), now); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return unauthorized()
		}
		return internal(err)
	}
	if err := s.repository.Deregister(ctx, tx, serverID, now); err != nil {
		return internal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return internal(err)
	}
	return nil
}

func (s *Service) Get(ctx context.Context, serverID string) (Server, error) {
	item, err := s.repository.Get(ctx, serverID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Server{}, &ServiceError{Status: 404, Code: "GAME_SERVER_NOT_FOUND", Message: "Game server not found."}
		}
		return Server{}, internal(err)
	}
	return item, nil
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
	filter.Limit++
	items, err := s.repository.List(ctx, filter)
	if err != nil {
		return ListResult{}, internal(err)
	}
	nextCursor := ""
	limit := filter.Limit - 1
	if len(items) > limit {
		nextCursor = items[limit-1].ID
		items = items[:limit]
	}
	return ListResult{Items: items, NextCursor: nextCursor}, nil
}

func validateRegistration(input RegistrationInput, issuer string) error {
	details := make(map[string]any)
	if !instancePattern.MatchString(strings.TrimSpace(input.InstanceID)) {
		details["instance_id"] = "contains unsupported characters or has invalid length"
	}
	if strings.TrimSpace(input.DisplayName) == "" {
		details["display_name"] = "is required"
	}
	for name, value := range map[string]string{"region": input.Region, "mode": input.Mode, "version": input.Version} {
		if !labelPattern.MatchString(strings.TrimSpace(value)) {
			details[name] = "contains unsupported characters or has invalid length"
		}
	}
	ip := net.ParseIP(strings.TrimSpace(input.PublicHost))
	if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		details["public_host"] = "must be a public unicast IP address"
	}
	if input.PublicPort < 1 || input.PublicPort > 65535 {
		details["public_port"] = "must be between 1 and 65535"
	}
	if input.MaxPlayers < 1 || input.MaxPlayers > 256 {
		details["max_players"] = "must be between 1 and 256"
	}
	if strings.TrimSpace(issuer) == "" {
		return &ServiceError{Status: 401, Code: "REGISTRATION_UNAUTHORIZED", Message: "Invalid registration token."}
	}
	if len(details) > 0 {
		return invalid("Invalid game server registration.", details)
	}
	return nil
}

func validTransition(current, next State) bool {
	if current == StateOffline {
		return false
	}
	if current == StateDraining {
		return next == StateDraining
	}
	if current == StateStarting {
		return next == StateStarting || next == StateReady || next == StateDraining
	}
	if current == StateUnhealthy {
		return isReportedState(next)
	}
	return next == StateReady || next == StateReserved || next == StateRunning || next == StateDraining
}

func isReportedState(state State) bool {
	switch state {
	case StateStarting, StateReady, StateReserved, StateRunning, StateDraining:
		return true
	default:
		return false
	}
}

func isState(state State) bool {
	return isReportedState(state) || state == StateUnhealthy || state == StateOffline
}

func truncate(value string, maximum int) string {
	runes := []rune(value)
	if len(runes) <= maximum {
		return value
	}
	return string(runes[:maximum])
}

func newID(prefix string) string {
	return prefix + strings.ReplaceAll(uuid.NewString(), "-", "")
}

func (s *Service) SweepStale(ctx context.Context) (int64, error) {
	return s.repository.SweepStale(
		ctx,
		s.now().UTC(),
		time.Duration(s.config.UnhealthyAfterSeconds)*time.Second,
		time.Duration(s.config.OfflineAfterSeconds)*time.Second,
	)
}

func (s *Service) HeartbeatInterval() int { return s.config.HeartbeatIntervalSeconds }
