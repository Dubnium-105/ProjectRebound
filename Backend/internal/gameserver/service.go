package gameserver

import (
	"context"
	"errors"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/config"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/gameserverregistration"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var labelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
var instancePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

type Service struct {
	repository         *Repository
	registrationTokens *gameserverregistration.Repository
	authority          *Authority
	proofVerifier      *ProofVerifier
	config             config.GameServerConfig
	now                func() time.Time
}

func NewService(
	repository *Repository,
	registrationTokens *gameserverregistration.Repository,
	cfg config.GameServerConfig,
	authorities ...*Authority,
) *Service {
	service := &Service{
		repository: repository, registrationTokens: registrationTokens,
		config: cfg, now: time.Now, proofVerifier: NewProofVerifier(repository, cfg),
	}
	if len(authorities) > 0 {
		service.authority = authorities[0]
	}
	return service
}

func (s *Service) IssueRegistrationCredential(
	ctx context.Context,
	input RegistrationCredentialInput,
) (RegistrationCredentialResult, error) {
	input.InstanceID = strings.TrimSpace(input.InstanceID)
	input.PlayerID = strings.TrimSpace(input.PlayerID)
	if !ValidInstanceID(input.InstanceID) {
		return RegistrationCredentialResult{}, invalid(
			"Invalid game server instance ID.",
			map[string]any{"instance_id": "contains unsupported characters or has invalid length"},
		)
	}
	if input.PlayerID == "" {
		return RegistrationCredentialResult{}, unauthorized()
	}
	tx, err := s.repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return RegistrationCredentialResult{}, internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	now := s.now().UTC()
	banned, err := s.repository.IsInstanceBanned(ctx, tx, input.InstanceID)
	if err != nil {
		return RegistrationCredentialResult{}, internal(err)
	}
	if banned {
		return RegistrationCredentialResult{}, gameServerBanned()
	}
	inviteUseID, err := s.registrationTokens.FindPlayerInviteGrant(ctx, tx, input.PlayerID)
	if errors.Is(err, gameserverregistration.ErrInvalidInviteGrant) {
		inviteUseID, err = s.registrationTokens.RedeemPlayerInviteGrant(
			ctx, tx, input.InviteCode, input.PlayerID, input.SteamID, input.IPAddress, now,
		)
		if errors.Is(err, gameserverregistration.ErrInvalidInviteGrant) {
			return RegistrationCredentialResult{}, &ServiceError{
				Status:  http.StatusForbidden,
				Code:    "GAME_SERVER_INVITE_REQUIRED",
				Message: "A valid Dedicated Server invitation is required.",
			}
		}
	}
	if err != nil {
		return RegistrationCredentialResult{}, internal(err)
	}
	plaintext, tokenHash, err := gameserverregistration.GenerateToken()
	if err != nil {
		return RegistrationCredentialResult{}, internal(err)
	}
	credential := gameserverregistration.Credential{
		ID: newID("gsrt_"), InstanceID: input.InstanceID,
		IssuedToPlayerID: input.PlayerID, SourceInviteUseID: inviteUseID,
		ExpiresAt: now.Add(s.config.RegistrationTokenTTL()), CreatedAt: now,
	}
	if _, err := s.registrationTokens.RevokeActiveForInstance(ctx, tx, input.InstanceID, now); err != nil {
		return RegistrationCredentialResult{}, internal(err)
	}
	if err := s.registrationTokens.Insert(ctx, tx, credential, tokenHash); err != nil {
		return RegistrationCredentialResult{}, internal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return RegistrationCredentialResult{}, internal(err)
	}
	return RegistrationCredentialResult{Credential: credential, Plaintext: plaintext}, nil
}

func (s *Service) Register(ctx context.Context, input RegistrationInput, registrationToken string) (RegistrationResult, error) {
	if err := validateRegistration(input); err != nil {
		return RegistrationResult{}, err
	}
	if !gameserverregistration.HasValidShape(registrationToken) {
		return RegistrationResult{}, unauthorized()
	}
	if s.authority == nil {
		return RegistrationResult{}, internal(errors.New("game server certificate authority is unavailable"))
	}
	tx, err := s.repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return RegistrationResult{}, internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	now := s.now().UTC()
	banned, err := s.repository.IsInstanceBanned(ctx, tx, strings.TrimSpace(input.InstanceID))
	if err != nil {
		return RegistrationResult{}, internal(err)
	}
	if banned {
		return RegistrationResult{}, gameServerBanned()
	}
	credential, err := s.registrationTokens.LockActive(
		ctx, tx, gameserverregistration.HashToken(registrationToken), now,
	)
	if errors.Is(err, gameserverregistration.ErrInvalidToken) {
		return RegistrationResult{}, unauthorized()
	}
	if err != nil {
		return RegistrationResult{}, internal(err)
	}
	if credential.InstanceID != strings.TrimSpace(input.InstanceID) {
		return RegistrationResult{}, unauthorized()
	}
	token, tokenHash, err := newServerToken()
	if err != nil {
		return RegistrationResult{}, internal(err)
	}
	item, err := s.repository.Register(ctx, tx, Server{
		ID:                 newID("gs_"),
		InstanceID:         strings.TrimSpace(input.InstanceID),
		OwnerPlayerID:      credential.IssuedToPlayerID,
		DisplayName:        truncate(strings.TrimSpace(input.DisplayName), 128),
		Region:             strings.TrimSpace(input.Region),
		Mode:               strings.TrimSpace(input.Mode),
		Version:            strings.TrimSpace(input.Version),
		PublicHost:         strings.TrimSpace(input.PublicHost),
		PublicPort:         input.PublicPort,
		MaxPlayers:         input.MaxPlayers,
		State:              StateStarting,
		ServerTokenHash:    tokenHash,
		RegistrationIssuer: credential.ID,
		TokenExpiresAt:     now.Add(s.config.ServerTokenTTL()),
		LastHeartbeatAt:    now,
		CreatedAt:          now,
		UpdatedAt:          now,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return RegistrationResult{}, &ServiceError{
				Status: http.StatusConflict, Code: "GAME_SERVER_INSTANCE_OWNED",
				Message: "The game server instance belongs to another registrant.",
			}
		}
		return RegistrationResult{}, internal(err)
	}
	certificate, err := s.authority.IssueClientCertificate(item.ID, input.CSRPEM, s.config.CertificateTTL())
	if err != nil {
		return RegistrationResult{}, err
	}
	item, err = s.repository.BindCertificate(ctx, tx, item.ID, certificate, now)
	if err != nil {
		return RegistrationResult{}, internal(err)
	}
	if err := s.registrationTokens.MarkConsumed(ctx, tx, credential.ID, item.ID, now); err != nil {
		if errors.Is(err, gameserverregistration.ErrInvalidToken) {
			return RegistrationResult{}, unauthorized()
		}
		return RegistrationResult{}, internal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return RegistrationResult{}, internal(err)
	}
	return RegistrationResult{
		Server:            item,
		ServerToken:       token,
		HeartbeatInterval: s.config.HeartbeatIntervalSeconds,
		CertificatePEM:    certificate.PEM,
		CACertificatePEM:  s.authority.CACertificatePEM(),
	}, nil
}

func (s *Service) VerifySignedRequest(ctx context.Context, input SignedRequestInput) (SignedRequestPrincipal, error) {
	return s.proofVerifier.Verify(ctx, input)
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
	assignment, err := s.repository.ActiveMatchAssignment(ctx, tx, serverID)
	if err != nil {
		return Server{}, internal(err)
	}
	reportedState := input.State
	if assignment != nil {
		// Meta owns reservation/running state for an assigned strict-roster
		// attempt. A Payload heartbeat that still reports READY during
		// provisioning must never make the node allocatable a second time.
		reportedState = StateReserved
		if assignment.State == "RUNNING" {
			reportedState = StateRunning
		}
	}
	if !validTransition(current.State, reportedState) {
		return Server{}, &ServiceError{Status: http.StatusConflict, Code: "INVALID_STATE_TRANSITION", Message: "Invalid game server state transition."}
	}
	updated, err := s.repository.UpdateHeartbeat(ctx, tx, serverID, reportedState, input.PlayerCount, now)
	if err != nil {
		return Server{}, internal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Server{}, internal(err)
	}
	updated.ActiveMatch = assignment
	return updated, nil
}

func (s *Service) RotateCredential(
	ctx context.Context,
	serverID, serverToken, csrPEM string,
) (CredentialRotationResult, error) {
	if !strings.HasPrefix(serverToken, "gst_") || len(serverToken) < 64 {
		return CredentialRotationResult{}, unauthorized()
	}
	if s.authority == nil {
		return CredentialRotationResult{}, internal(errors.New("game server certificate authority is unavailable"))
	}
	tx, err := s.repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return CredentialRotationResult{}, internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	now := s.now().UTC()
	current, err := s.repository.GetCurrentForManagement(ctx, tx, serverID, hashServerToken(serverToken), now)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return CredentialRotationResult{}, unauthorized()
		}
		return CredentialRotationResult{}, internal(err)
	}
	plaintext, tokenHash, err := newServerToken()
	if err != nil {
		return CredentialRotationResult{}, internal(err)
	}
	previousValidUntil := now.Add(s.config.ServerTokenRotationGrace())
	if current.TokenExpiresAt.Before(previousValidUntil) {
		previousValidUntil = current.TokenExpiresAt
	}
	newExpiresAt := now.Add(s.config.ServerTokenTTL())
	certificate, err := s.authority.IssueClientCertificate(serverID, csrPEM, s.config.CertificateTTL())
	if err != nil {
		return CredentialRotationResult{}, err
	}
	updated, err := s.repository.RotateCredential(
		ctx, tx, serverID, tokenHash, certificate, newExpiresAt, previousValidUntil, now,
	)
	if err != nil {
		return CredentialRotationResult{}, internal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return CredentialRotationResult{}, internal(err)
	}
	return CredentialRotationResult{
		ServerID: serverID, ServerToken: plaintext, TokenExpiresAt: updated.TokenExpiresAt,
		PreviousValidUntil:     previousValidUntil,
		CredentialGeneration:   updated.CredentialGeneration,
		CertificatePEM:         certificate.PEM,
		CACertificatePEM:       s.authority.CACertificatePEM(),
		CertificateFingerprint: certificate.Fingerprint,
		CertificateExpiresAt:   certificate.ExpiresAt,
	}, nil
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
	if _, err := s.repository.GetCurrentForManagement(ctx, tx, serverID, hashServerToken(serverToken), now); err != nil {
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

func validateRegistration(input RegistrationInput) error {
	details := make(map[string]any)
	if !ValidInstanceID(input.InstanceID) {
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
	if strings.TrimSpace(input.CSRPEM) == "" || len(input.CSRPEM) > 16*1024 {
		details["csr_pem"] = "must contain an Ed25519 certificate request no larger than 16384 bytes"
	}
	if len(details) > 0 {
		return invalid("Invalid game server registration.", details)
	}
	return nil
}

func ValidInstanceID(value string) bool {
	return instancePattern.MatchString(strings.TrimSpace(value))
}

func gameServerBanned() *ServiceError {
	return &ServiceError{
		Status:  http.StatusForbidden,
		Code:    "GAME_SERVER_BANNED",
		Message: "This game server instance is banned.",
	}
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
