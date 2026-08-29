package auth

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/config"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/entitlement"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/player"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Service struct {
	pool                *pgxpool.Pool
	repository          *Repository
	players             *player.Repository
	tokens              *TokenManager
	deviceFingerprinter *DeviceFingerprinter
	ticketVerifier      TicketVerifier
	integritySessions   IntegritySessionManager
	config              config.AuthConfig
	logger              *slog.Logger
	now                 func() time.Time
	metrics             interface {
		RefreshTokenReuse()
		BindRateLimited(string)
		InviteCodeFailure()
	}
	bindLimiter *BindLimiter
	invites     interface {
		Consume(context.Context, pgx.Tx, string, string, string, string, time.Time) (string, map[string]any, *time.Time, error)
	}
	entitlements *entitlement.Repository
}

type IntegritySessionManager interface {
	PEMFingerprint() []byte
	RegisterSession(sessionID string, ticket []byte, expiresAt time.Time) (IntegrityChallenge, error)
	RotateSession(oldSessionID, newSessionID string, expiresAt time.Time)
	RemoveSession(sessionID string)
}

func (s *Service) SetMetrics(metrics interface {
	RefreshTokenReuse()
	BindRateLimited(string)
	InviteCodeFailure()
}) {
	s.metrics = metrics
	if s.bindLimiter != nil {
		s.bindLimiter.SetMetrics(metrics)
	}
}

func (s *Service) SetInviteConsumer(consumer interface {
	Consume(context.Context, pgx.Tx, string, string, string, string, time.Time) (string, map[string]any, *time.Time, error)
}) {
	s.invites = consumer
}

func (s *Service) SetEntitlements(repository *entitlement.Repository) {
	s.entitlements = repository
}

func (s *Service) SetBindLimiter(limiter *BindLimiter) {
	s.bindLimiter = limiter
	if limiter != nil && s.metrics != nil {
		limiter.SetMetrics(s.metrics)
	}
}

func (s *Service) SetTicketVerifier(verifier TicketVerifier) {
	s.ticketVerifier = verifier
}

func (s *Service) SetIntegritySessionManager(manager IntegritySessionManager) {
	s.integritySessions = manager
}

func NewService(
	pool *pgxpool.Pool,
	repository *Repository,
	players *player.Repository,
	tokens *TokenManager,
	deviceFingerprinter *DeviceFingerprinter,
	cfg config.AuthConfig,
	logger *slog.Logger,
) *Service {
	return &Service{
		pool:                pool,
		repository:          repository,
		players:             players,
		tokens:              tokens,
		deviceFingerprinter: deviceFingerprinter,
		config:              cfg,
		logger:              logger,
		now:                 time.Now,
	}
}

func (s *Service) Bind(ctx context.Context, input BindInput, meta RequestMeta) (BindResult, error) {
	meta = sanitizeMeta(meta)
	input.SteamID = strings.TrimSpace(input.SteamID)
	deviceID := input.DeviceID
	if strings.TrimSpace(deviceID) == "" {
		deviceID = meta.DeviceID
	}
	deviceID, err := NormalizeDeviceID(deviceID)
	if err != nil {
		s.recordFailedAudit(ctx, input.SteamID, "AUTH_BIND_FAILURE", CodeInvalidRequest, meta)
		return BindResult{}, invalidRequest("Invalid device ID.", map[string]any{"device_id": err.Error()})
	}
	meta.DeviceID = deviceID
	now := s.now().UTC()
	if s.bindLimiter != nil {
		decision := s.bindLimiter.Check(ctx, BindLimitRequest{
			IPAddress: meta.IPAddress,
			SteamID:   input.SteamID,
			DeviceID:  deviceID,
		})
		if !decision.Allowed {
			retryAfterSeconds := max(1, int((decision.RetryAfter+time.Second-1)/time.Second))
			s.recordRiskEvent(ctx, RiskEvent{
				SteamID:      validSteamIDOrEmpty(input.SteamID),
				DeviceIDHash: HashDeviceID(deviceID),
				IPAddress:    meta.IPAddress,
				EventType:    "BIND_RATE_LIMITED",
				Severity:     "MEDIUM",
				Details:      map[string]any{"dimension": decision.Dimension, "retry_after_seconds": retryAfterSeconds},
			}, meta)
			s.recordFailedAudit(ctx, input.SteamID, "AUTH_BIND_FAILURE", CodeBindRateLimited, meta)
			return BindResult{}, &ServiceError{
				Status:  429,
				Code:    CodeBindRateLimited,
				Message: "Too many authentication attempts.",
				Details: map[string]any{"retry_after_seconds": retryAfterSeconds},
			}
		}
	}
	if err := ValidateSteamID(input.SteamID); err != nil {
		s.recordRiskEvent(ctx, RiskEvent{
			DeviceIDHash: HashDeviceID(deviceID),
			IPAddress:    meta.IPAddress,
			EventType:    "INVALID_STEAM_ID",
			Severity:     "LOW",
			Details:      map[string]any{"validation_error": err.Error()},
		}, meta)
		s.recordFailedAudit(ctx, input.SteamID, "AUTH_BIND_FAILURE", CodeInvalidRequest, meta)
		return BindResult{}, invalidRequest("Invalid SteamID.", map[string]any{"steam_id": err.Error()})
	}
	personaName, err := NormalizePersonaName(input.PersonaName, s.config.DefaultPersonaName)
	if err != nil {
		s.recordFailedAudit(ctx, input.SteamID, "AUTH_BIND_FAILURE", CodeInvalidRequest, meta)
		return BindResult{}, invalidRequest("Invalid persona name.", map[string]any{"persona_name": err.Error()})
	}

	authProvider := player.AuthProviderSteamClientAsserted
	authLevel := player.AuthLevelUnverified
	verifiedSteamID := input.SteamID
	var ticketHash []byte
	var ticketBytes []byte
	var ticket VerifiedTicket
	if strings.TrimSpace(input.EncryptedTicket) != "" {
		canonicalTicket, hash, normalizeErr := normalizeEncryptedTicket(
			input.EncryptedTicket,
			s.config.TicketMaximumHexBytes,
		)
		if normalizeErr != nil {
			return BindResult{}, s.rejectTicket(ctx, input.SteamID, CodeInvalidSteamTicket, "invalid_format", meta, normalizeErr)
		}
		if s.ticketVerifier == nil {
			return BindResult{}, s.rejectTicket(ctx, input.SteamID, CodeInvalidSteamTicket, "verifier_unavailable", meta, errors.New("ticket verifier is unavailable"))
		}
		ticket, err = s.ticketVerifier.Verify(ctx, canonicalTicket)
		if err != nil {
			return BindResult{}, s.rejectTicket(ctx, input.SteamID, CodeInvalidSteamTicket, "decrypt_failed", meta, err)
		}
		ticket.SteamID = strings.TrimSpace(ticket.SteamID)
		if validationErr := validateVerifiedTicket(input.SteamID, ticket); validationErr != nil {
			details := validationErr.(*ticketValidationError)
			return BindResult{}, s.rejectTicket(
				ctx, input.SteamID, details.code, details.reason, meta, details.cause,
			)
		}
		ticketHash = hash
		ticketBytes, _ = hex.DecodeString(canonicalTicket)
		verifiedSteamID = ticket.SteamID
		authProvider = player.AuthProviderSteamTicket
		authLevel = player.AuthLevelVerified
	} else if developmentTrustedSteamID(s.config.DevelopmentTrustedSteamIDs, input.SteamID) {
		// This path exists only for an isolated, development-mode two-machine
		// lab whose exact Steam IDs were allowlisted at process startup. Config
		// validation rejects the allowlist in every non-development environment.
		authLevel = player.AuthLevelTrusted
	}

	deviceFingerprintID, err := s.resolveDeviceFingerprint(ctx, s.pool, deviceID, now)
	if err != nil {
		return BindResult{}, internalError(err)
	}
	meta.DeviceFingerprintID = deviceFingerprintID
	deviceBanned, err := s.repository.IsDeviceFingerprintBanned(ctx, s.pool, deviceFingerprintID)
	if err != nil {
		return BindResult{}, internalError(err)
	}
	if deviceBanned {
		s.recordRiskEvent(ctx, RiskEvent{
			SteamID: verifiedSteamID, DeviceIDHash: HashDeviceID(deviceID),
			DeviceFingerprintID: deviceFingerprintID, IPAddress: meta.IPAddress,
			EventType: "DEVICE_MISMATCH", Severity: "HIGH",
			Details: map[string]any{"matched_factors": "at_least_two"},
		}, meta)
		s.recordFailedAudit(ctx, verifiedSteamID, "DEVICE_MISMATCH", CodeDeviceRestricted, meta)
		return BindResult{}, &ServiceError{
			Status: 403, Code: CodeDeviceRestricted, Message: "This device is restricted.",
		}
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return BindResult{}, internalError(fmt.Errorf("begin bind transaction: %w", err))
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	item, isNew, err := s.players.UpsertSteamIdentity(
		ctx, tx, NewID("p_"), verifiedSteamID, personaName, authProvider, authLevel, now,
	)
	if err != nil {
		return BindResult{}, internalError(err)
	}
	if item.AccountStatus == player.AccountStatusDeleted {
		_ = tx.Rollback(ctx)
		s.recordFailedAudit(ctx, input.SteamID, "AUTH_BIND_FAILURE", CodeAccountDeleted, meta)
		return BindResult{}, &ServiceError{
			Status:  403,
			Code:    CodeAccountDeleted,
			Message: "Account has been deleted.",
		}
	}
	inviteCode := strings.TrimSpace(input.InviteCode)
	if (isNew && s.config.InviteRequired) || inviteCode != "" {
		if s.invites == nil {
			return BindResult{}, internalError(errors.New("invite code service is not configured"))
		}
		inviteUseID, permissions, permissionExpiresAt, consumeErr := s.invites.Consume(ctx, tx, inviteCode, item.ID, item.SteamID, meta.IPAddress, now)
		if consumeErr != nil {
			var invalidInvite interface{ InvalidInviteCode() bool }
			if !errors.As(consumeErr, &invalidInvite) || !invalidInvite.InvalidInviteCode() {
				return BindResult{}, internalError(consumeErr)
			}
			_ = tx.Rollback(ctx)
			s.recordRiskEvent(ctx, RiskEvent{
				SteamID: item.SteamID, PlayerID: "", DeviceIDHash: HashDeviceID(deviceID),
				IPAddress: meta.IPAddress, EventType: "INVALID_INVITE_CODE", Severity: "LOW",
				Details: map[string]any{"invite_required": s.config.InviteRequired},
			}, meta)
			s.recordFailedAudit(ctx, item.SteamID, "AUTH_BIND_FAILURE", CodeInvalidInvite, meta)
			if s.metrics != nil {
				s.metrics.InviteCodeFailure()
			}
			return BindResult{}, &ServiceError{
				Status: 403, Code: CodeInvalidInvite, Message: "A valid invite code is required.",
			}
		}
		if isNew && s.config.InviteRequired && !entitlement.AllowsAccountCreation(permissions) {
			return BindResult{}, &ServiceError{
				Status: 403, Code: CodeInvalidInvite,
				Message: "This invite code cannot create an account.",
			}
		}
		if s.entitlements == nil {
			return BindResult{}, internalError(errors.New("entitlement repository is not configured"))
		}
		if err := s.entitlements.GrantFromInvite(ctx, tx, item.ID, inviteUseID, permissions, permissionExpiresAt, now); err != nil {
			return BindResult{}, internalError(err)
		}
	}
	capabilities := []string{}
	if s.entitlements != nil {
		capabilities, err = s.entitlements.ListWith(ctx, tx, item.ID)
		if err != nil {
			return BindResult{}, internalError(err)
		}
	}

	if len(ticketHash) > 0 && ticket.AppID > 0 && ticket.IssueTime > 0 {
		if insertErr := s.repository.InsertTicketVerification(ctx, tx, TicketVerification{
			ID: NewID("atv_"), PlayerID: item.ID, SteamID: ticket.SteamID,
			AppID: ticket.AppID, TicketHash: ticketHash,
			IssueTime: time.Unix(ticket.IssueTime, 0).UTC(), VerifiedAt: now,
		}); insertErr != nil {
			return BindResult{}, internalError(insertErr)
		}
	}

	session, rawRefreshToken, err := s.newSession(
		item.ID, NewID("fam_"), 1, authProvider, authLevel, meta, now,
	)
	if err != nil {
		return BindResult{}, internalError(err)
	}
	if s.integritySessions != nil {
		session.PEMFingerprint = s.integritySessions.PEMFingerprint()
	}
	integrityChallenge := IntegrityChallenge{}
	integrityRegistered := false
	if len(ticketBytes) > 0 && s.integritySessions != nil {
		integrityChallenge, err = s.integritySessions.RegisterSession(
			session.ID,
			ticketBytes,
			session.ExpiresAt,
		)
		if err != nil {
			return BindResult{}, internalError(fmt.Errorf("register integrity session: %w", err))
		}
		integrityRegistered = integrityChallenge.Nonce != ""
	}
	defer func() {
		if integrityRegistered && s.integritySessions != nil {
			s.integritySessions.RemoveSession(session.ID)
		}
	}()
	if err := s.repository.InsertSession(ctx, tx, session); err != nil {
		return BindResult{}, internalError(err)
	}
	issued, err := s.issueTokens(item, session, rawRefreshToken)
	if err != nil {
		return BindResult{}, internalError(fmt.Errorf("issue bind tokens: %w", err))
	}
	if err := s.repository.InsertAudit(ctx, tx, AuditEvent{
		ID:                  NewID("aal_"),
		PlayerID:            item.ID,
		SteamID:             item.SteamID,
		Event:               "AUTH_BIND_SUCCESS",
		Success:             true,
		RequestID:           meta.RequestID,
		IPAddress:           meta.IPAddress,
		UserAgent:           meta.UserAgent,
		DeviceIDHash:        session.DeviceIDHash,
		DeviceFingerprintID: session.DeviceFingerprintID,
		CreatedAt:           now,
	}); err != nil {
		return BindResult{}, internalError(err)
	}
	if len(ticketHash) > 0 {
		if err := s.repository.InsertAudit(ctx, tx, AuditEvent{
			ID: NewID("aal_"), PlayerID: item.ID, SteamID: item.SteamID,
			Event: "STEAM_TICKET_VERIFY_SUCCESS", Success: true,
			RequestID: meta.RequestID, IPAddress: meta.IPAddress, UserAgent: meta.UserAgent,
			DeviceIDHash: session.DeviceIDHash, DeviceFingerprintID: session.DeviceFingerprintID,
			CreatedAt: now,
		}); err != nil {
			return BindResult{}, internalError(err)
		}
	}
	if err := s.repository.InsertLoginEvent(ctx, tx, LoginEvent{
		ID:                  NewID("ale_"),
		PlayerID:            item.ID,
		SteamID:             item.SteamID,
		SessionID:           session.ID,
		DeviceIDHash:        session.DeviceIDHash,
		DeviceFingerprintID: session.DeviceFingerprintID,
		IPAddress:           meta.IPAddress,
		UserAgent:           meta.UserAgent,
		Result:              "SUCCESS",
		CreatedAt:           now,
	}); err != nil {
		return BindResult{}, internalError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return BindResult{}, internalError(fmt.Errorf("commit bind transaction: %w", err))
	}
	integrityRegistered = false
	return BindResult{
		Player: item, Tokens: issued, IsNewPlayer: isNew,
		AuthLevel: session.AuthLevel, SteamVerified: session.SteamVerified,
		Capabilities:       capabilities,
		IntegrityChallenge: integrityChallenge,
	}, nil
}

func developmentTrustedSteamID(allowlist []string, steamID string) bool {
	for _, allowed := range allowlist {
		if steamID == allowed {
			return true
		}
	}
	return false
}

func (s *Service) Refresh(ctx context.Context, refreshToken string, meta RequestMeta) (RefreshResult, error) {
	meta = sanitizeMeta(meta)
	deviceID, err := NormalizeDeviceID(meta.DeviceID)
	if err != nil {
		s.recordFailedAudit(ctx, "", "AUTH_REFRESH_FAILURE", CodeInvalidRequest, meta)
		return RefreshResult{}, invalidRequest("Invalid device ID.", map[string]any{"device_id": err.Error()})
	}
	meta.DeviceID = deviceID
	if !strings.HasPrefix(refreshToken, "rfr_") || len(refreshToken) < 64 {
		s.recordFailedAudit(ctx, "", "AUTH_REFRESH_FAILURE", CodeUnauthorized, meta)
		return RefreshResult{}, unauthorized("Invalid refresh token.", nil)
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return RefreshResult{}, internalError(fmt.Errorf("begin refresh transaction: %w", err))
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	now := s.now().UTC()
	current, err := s.repository.FindByRefreshTokenForUpdate(ctx, tx, HashRefreshToken(refreshToken))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			_ = tx.Rollback(ctx)
			s.recordFailedAudit(ctx, "", "AUTH_REFRESH_FAILURE", CodeUnauthorized, meta)
			return RefreshResult{}, unauthorized("Invalid refresh token.", nil)
		}
		return RefreshResult{}, internalError(fmt.Errorf("find refresh session: %w", err))
	}
	if meta.DeviceID == "" {
		meta.DeviceFingerprintID = current.DeviceFingerprintID
	} else {
		deviceFingerprintID, fingerprintErr := s.resolveDeviceFingerprint(ctx, tx, meta.DeviceID, now)
		if fingerprintErr != nil {
			return RefreshResult{}, internalError(fingerprintErr)
		}
		meta.DeviceFingerprintID = deviceFingerprintID
	}
	if current.RevokedAt != nil {
		if current.RevokedReason == "ROTATED" || current.ReplacedBySessionID != "" {
			if err := s.repository.RevokeFamilyForReuse(ctx, tx, current.TokenFamilyID, current.ID, now); err != nil {
				return RefreshResult{}, internalError(err)
			}
			if err := s.repository.InsertAudit(ctx, tx, AuditEvent{
				ID:          NewID("aal_"),
				PlayerID:    current.PlayerID,
				Event:       "REFRESH_TOKEN_REUSE",
				Success:     false,
				FailureCode: CodeRefreshTokenReused,
				RequestID:   meta.RequestID,
				IPAddress:   meta.IPAddress,
				UserAgent:   meta.UserAgent,
				CreatedAt:   now,
			}); err != nil {
				return RefreshResult{}, internalError(err)
			}
			if err := s.repository.InsertRiskEvent(ctx, tx, RiskEvent{
				ID: NewID("are_"), PlayerID: current.PlayerID,
				DeviceIDHash: HashDeviceID(meta.DeviceID), DeviceFingerprintID: meta.DeviceFingerprintID,
				IPAddress: meta.IPAddress,
				EventType: "REFRESH_TOKEN_REUSE", Severity: "HIGH",
				Details:   map[string]any{"session_id": current.ID, "token_family_id": current.TokenFamilyID},
				CreatedAt: now,
			}); err != nil {
				return RefreshResult{}, internalError(err)
			}
			if err := s.repository.InsertLoginEvent(ctx, tx, LoginEvent{
				ID: NewID("ale_"), PlayerID: current.PlayerID, SessionID: current.ID,
				DeviceIDHash: HashDeviceID(meta.DeviceID), DeviceFingerprintID: meta.DeviceFingerprintID,
				IPAddress: meta.IPAddress,
				UserAgent: meta.UserAgent, Result: "FAILURE", FailureCode: CodeRefreshTokenReused,
				CreatedAt: now,
			}); err != nil {
				return RefreshResult{}, internalError(err)
			}
			if err := tx.Commit(ctx); err != nil {
				return RefreshResult{}, internalError(fmt.Errorf("commit refresh reuse revocation: %w", err))
			}
			if s.metrics != nil {
				s.metrics.RefreshTokenReuse()
			}
			return RefreshResult{}, &ServiceError{
				Status:  401,
				Code:    CodeRefreshTokenReused,
				Message: "Refresh token reuse detected; the session family was revoked.",
			}
		}
		_ = tx.Rollback(ctx)
		s.recordRiskEvent(ctx, RiskEvent{
			PlayerID: current.PlayerID, DeviceIDHash: HashDeviceID(meta.DeviceID),
			DeviceFingerprintID: meta.DeviceFingerprintID,
			IPAddress:           meta.IPAddress, EventType: "REVOKED_SESSION_USAGE", Severity: "MEDIUM",
			Details: map[string]any{"session_id": current.ID, "revoked_reason": current.RevokedReason},
		}, meta)
		s.recordFailedAudit(ctx, "", "AUTH_REFRESH_FAILURE", CodeSessionRevoked, meta)
		return RefreshResult{}, &ServiceError{Status: 401, Code: CodeSessionRevoked, Message: "Session has been revoked."}
	}
	if !current.ExpiresAt.After(now) {
		if err := s.repository.RevokeSession(ctx, tx, current.ID, now, "EXPIRED"); err != nil {
			return RefreshResult{}, internalError(err)
		}
		if err := tx.Commit(ctx); err != nil {
			return RefreshResult{}, internalError(err)
		}
		s.recordFailedAudit(ctx, "", "AUTH_REFRESH_FAILURE", CodeUnauthorized, meta)
		return RefreshResult{}, unauthorized("Refresh token has expired.", nil)
	}

	item, err := s.players.GetByID(ctx, tx, current.PlayerID)
	if err != nil {
		return RefreshResult{}, internalError(err)
	}
	if item.AccountStatus == player.AccountStatusDeleted {
		if err := s.repository.RevokeSession(ctx, tx, current.ID, now, "ACCOUNT_DELETED"); err != nil {
			return RefreshResult{}, internalError(err)
		}
		if err := tx.Commit(ctx); err != nil {
			return RefreshResult{}, internalError(err)
		}
		s.recordFailedAudit(ctx, item.SteamID, "AUTH_REFRESH_FAILURE", CodeAccountDeleted, meta)
		return RefreshResult{}, &ServiceError{Status: 403, Code: CodeAccountDeleted, Message: "Account has been deleted."}
	}

	replacement, rawRefreshToken, err := s.newSession(
		item.ID, current.TokenFamilyID, current.TokenVersion+1,
		current.AuthProvider, current.AuthLevel, meta, now,
	)
	if err != nil {
		return RefreshResult{}, internalError(err)
	}
	replacement.PEMFingerprint = append([]byte(nil), current.PEMFingerprint...)
	replacement.IntegrityTrusted = current.IntegrityTrusted
	if len(replacement.DeviceIDHash) == 0 {
		replacement.DeviceIDHash = current.DeviceIDHash
		replacement.DeviceIDSuffix = current.DeviceIDSuffix
		replacement.DeviceFingerprintID = current.DeviceFingerprintID
	}
	if err := s.repository.RotateSession(ctx, tx, current.ID, replacement, now); err != nil {
		return RefreshResult{}, internalError(err)
	}
	issued, err := s.issueTokens(item, replacement, rawRefreshToken)
	if err != nil {
		return RefreshResult{}, internalError(fmt.Errorf("issue refreshed tokens: %w", err))
	}
	if err := s.repository.InsertAudit(ctx, tx, AuditEvent{
		ID:        NewID("aal_"),
		PlayerID:  item.ID,
		SteamID:   item.SteamID,
		Event:     "AUTH_REFRESH_SUCCESS",
		Success:   true,
		RequestID: meta.RequestID,
		IPAddress: meta.IPAddress,
		UserAgent: meta.UserAgent,
		CreatedAt: now,
	}); err != nil {
		return RefreshResult{}, internalError(err)
	}
	if err := s.repository.InsertLoginEvent(ctx, tx, LoginEvent{
		ID: NewID("ale_"), PlayerID: item.ID, SteamID: item.SteamID, SessionID: replacement.ID,
		DeviceIDHash: replacement.DeviceIDHash, DeviceFingerprintID: replacement.DeviceFingerprintID,
		IPAddress: meta.IPAddress,
		UserAgent: meta.UserAgent, Result: "SUCCESS", CreatedAt: now,
	}); err != nil {
		return RefreshResult{}, internalError(err)
	}
	if current.IPAddress != "" && meta.IPAddress != "" && current.IPAddress != meta.IPAddress {
		if err := s.repository.InsertRiskEvent(ctx, tx, RiskEvent{
			ID: NewID("are_"), PlayerID: item.ID, SteamID: item.SteamID,
			DeviceIDHash: replacement.DeviceIDHash, DeviceFingerprintID: replacement.DeviceFingerprintID,
			IPAddress: meta.IPAddress,
			EventType: "RAPID_IP_CHANGE", Severity: "MEDIUM",
			Details: map[string]any{"previous_session_id": current.ID}, CreatedAt: now,
		}); err != nil {
			return RefreshResult{}, internalError(err)
		}
	}
	if len(current.DeviceIDHash) > 0 && len(replacement.DeviceIDHash) > 0 &&
		!bytes.Equal(current.DeviceIDHash, replacement.DeviceIDHash) {
		if err := s.repository.InsertRiskEvent(ctx, tx, RiskEvent{
			ID: NewID("are_"), PlayerID: item.ID, SteamID: item.SteamID,
			DeviceIDHash: replacement.DeviceIDHash, DeviceFingerprintID: replacement.DeviceFingerprintID,
			IPAddress: meta.IPAddress,
			EventType: "MULTI_DEVICE_LOGIN", Severity: "LOW",
			Details: map[string]any{"previous_session_id": current.ID}, CreatedAt: now,
		}); err != nil {
			return RefreshResult{}, internalError(err)
		}
	}
	capabilities := []string{}
	if s.entitlements != nil {
		capabilities, err = s.entitlements.ListWith(ctx, tx, item.ID)
		if err != nil {
			return RefreshResult{}, internalError(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return RefreshResult{}, internalError(fmt.Errorf("commit refresh transaction: %w", err))
	}
	if s.integritySessions != nil {
		s.integritySessions.RotateSession(current.ID, replacement.ID, replacement.ExpiresAt)
	}
	return RefreshResult{Tokens: issued, Capabilities: capabilities}, nil
}

func (s *Service) AuthenticateAccess(ctx context.Context, accessToken string) (Principal, error) {
	claims, err := s.tokens.Verify(accessToken)
	if err != nil {
		return Principal{}, unauthorized("Invalid access token.", err)
	}
	session, err := s.repository.GetSessionForAccess(ctx, s.pool, claims.SessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Principal{}, unauthorized("Invalid access token.", err)
		}
		return Principal{}, internalError(err)
	}
	if session.PlayerID != claims.Subject || session.TokenVersion != claims.TokenVersion {
		return Principal{}, unauthorized("Invalid access token.", nil)
	}
	if session.RevokedAt != nil {
		s.recordRiskEvent(ctx, RiskEvent{
			PlayerID: session.PlayerID, DeviceIDHash: session.DeviceIDHash,
			DeviceFingerprintID: session.DeviceFingerprintID,
			EventType:           "REVOKED_SESSION_USAGE", Severity: "MEDIUM",
			Details: map[string]any{"session_id": session.ID, "revoked_reason": session.RevokedReason},
		}, RequestMeta{})
		return Principal{}, &ServiceError{Status: 401, Code: CodeSessionRevoked, Message: "Session has been revoked."}
	}
	item, err := s.players.GetByIDForAccess(ctx, s.pool, claims.Subject)
	if err != nil {
		return Principal{}, internalError(err)
	}
	authLevelMatches := session.AuthLevel == claims.AuthLevel ||
		(session.AuthLevel == player.AuthLevelTrusted && claims.AuthLevel == player.AuthLevelVerified)
	if session.AuthProvider != claims.Provider || !authLevelMatches ||
		session.SteamVerified != claims.SteamVerified {
		return Principal{}, unauthorized("Invalid access token.", nil)
	}
	if item.AccountStatus == player.AccountStatusDeleted {
		return Principal{}, &ServiceError{Status: 403, Code: CodeAccountDeleted, Message: "Account has been deleted."}
	}
	if err := s.repository.TouchSession(ctx, s.pool, session.ID, s.now().UTC()); err != nil {
		s.logger.WarnContext(ctx, "update authentication session activity", "session_id", session.ID, "error", err)
	}
	capabilities := []string{}
	if s.entitlements != nil {
		capabilities, err = s.entitlements.List(ctx, item.ID)
		if err != nil {
			return Principal{}, internalError(err)
		}
	}
	return Principal{
		Player: item, SessionID: session.ID,
		AuthProvider: session.AuthProvider, AuthLevel: session.AuthLevel,
		SteamVerified:    session.SteamVerified,
		PEMFingerprint:   append([]byte(nil), session.PEMFingerprint...),
		IntegrityTrusted: session.IntegrityTrusted,
		Capabilities:     capabilities,
	}, nil
}

func (s *Service) PromoteIntegrityTrusted(
	ctx context.Context,
	principal Principal,
	meta RequestMeta,
) error {
	meta = sanitizeMeta(meta)
	now := s.now().UTC()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return internalError(fmt.Errorf("begin integrity promotion transaction: %w", err))
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	promoted, err := s.repository.PromoteIntegrityTrusted(
		ctx,
		tx,
		principal.SessionID,
		principal.Player.ID,
		now,
	)
	if err != nil {
		return internalError(err)
	}
	if !promoted {
		return &ServiceError{
			Status:  http.StatusUnauthorized,
			Code:    CodeSessionRevoked,
			Message: "Session is no longer eligible for integrity verification.",
		}
	}
	session, err := s.repository.GetSession(ctx, tx, principal.SessionID)
	if err != nil {
		return internalError(err)
	}
	if err := s.repository.InsertAudit(ctx, tx, AuditEvent{
		ID: NewID("aal_"), PlayerID: principal.Player.ID, SteamID: principal.Player.SteamID,
		Event: "INTEGRITY_VERIFY_SUCCESS", Success: true,
		RequestID: meta.RequestID, IPAddress: meta.IPAddress, UserAgent: meta.UserAgent,
		DeviceIDHash: session.DeviceIDHash, DeviceFingerprintID: session.DeviceFingerprintID,
		CreatedAt: now,
	}); err != nil {
		return internalError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return internalError(fmt.Errorf("commit integrity promotion: %w", err))
	}
	return nil
}

func (s *Service) RecordIntegrityFailure(
	ctx context.Context,
	principal Principal,
	attempts int,
	component string,
	reason string,
	terminal bool,
	meta RequestMeta,
) error {
	meta = sanitizeMeta(meta)
	now := s.now().UTC()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return internalError(fmt.Errorf("begin integrity failure transaction: %w", err))
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	session, err := s.repository.GetSession(ctx, tx, principal.SessionID)
	if err != nil {
		return internalError(err)
	}
	if session.PlayerID != principal.Player.ID {
		return unauthorized("Invalid access token.", nil)
	}
	cleared, err := s.repository.ClearIntegrityTrusted(
		ctx,
		tx,
		principal.SessionID,
		principal.Player.ID,
		now,
	)
	if err != nil {
		return internalError(err)
	}
	if !cleared {
		return &ServiceError{
			Status:  http.StatusUnauthorized,
			Code:    CodeSessionRevoked,
			Message: "Session is no longer eligible for integrity verification.",
		}
	}
	if err := s.repository.InsertAudit(ctx, tx, AuditEvent{
		ID: NewID("aal_"), PlayerID: principal.Player.ID, SteamID: principal.Player.SteamID,
		Event: "INTEGRITY_FAILED", Success: false, FailureCode: CodeIntegrityFailed,
		RequestID: meta.RequestID, IPAddress: meta.IPAddress, UserAgent: meta.UserAgent,
		DeviceIDHash: session.DeviceIDHash, DeviceFingerprintID: session.DeviceFingerprintID,
		CreatedAt: now,
	}); err != nil {
		return internalError(err)
	}
	if terminal {
		if err := s.repository.RevokeSession(
			ctx,
			tx,
			principal.SessionID,
			now,
			"INTEGRITY_FAILED",
		); err != nil {
			return internalError(err)
		}
		deviceBanned, err := s.repository.IsDeviceFingerprintBanned(
			ctx,
			tx,
			session.DeviceFingerprintID,
		)
		if err != nil {
			return internalError(err)
		}
		severity := "HIGH"
		if deviceBanned {
			severity = "CRITICAL"
		}
		if err := s.repository.InsertRiskEvent(ctx, tx, RiskEvent{
			ID: NewID("are_"), PlayerID: principal.Player.ID, SteamID: principal.Player.SteamID,
			DeviceIDHash: session.DeviceIDHash, DeviceFingerprintID: session.DeviceFingerprintID,
			IPAddress: meta.IPAddress, EventType: "INTEGRITY_FAILED", Severity: severity,
			Details: map[string]any{
				"attempts": attempts, "component": component, "reason": reason,
				"session_id": principal.SessionID, "device_ban_match": deviceBanned,
			},
			CreatedAt: now,
		}); err != nil {
			return internalError(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return internalError(fmt.Errorf("commit integrity failure: %w", err))
	}
	return nil
}

func (s *Service) Logout(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return unauthorized("Invalid access token.", nil)
	}
	if err := s.repository.RevokeSession(ctx, s.pool, sessionID, s.now().UTC(), "LOGOUT"); err != nil {
		return internalError(err)
	}
	if s.integritySessions != nil {
		s.integritySessions.RemoveSession(sessionID)
	}
	return nil
}

func (s *Service) ListUserSessions(ctx context.Context, playerID, currentSessionID string) ([]UserSession, error) {
	if playerID == "" || currentSessionID == "" {
		return nil, unauthorized("Invalid access token.", nil)
	}
	items, err := s.repository.ListPlayerSessions(ctx, s.pool, playerID, s.now().UTC())
	if err != nil {
		return nil, internalError(err)
	}
	result := make([]UserSession, 0, len(items))
	for _, item := range items {
		result = append(result, UserSession{
			ID: item.ID, DeviceIDSuffix: item.DeviceIDSuffix,
			IPAddress: maskIPAddress(item.IPAddress), CreatedAt: item.CreatedAt,
			LastUsedAt: item.LastUsedAt, IsCurrent: item.ID == currentSessionID,
		})
	}
	return result, nil
}

func (s *Service) RevokeUserSession(ctx context.Context, playerID, sessionID string) error {
	if playerID == "" || strings.TrimSpace(sessionID) == "" {
		return invalidRequest("Invalid session ID.", nil)
	}
	revoked, err := s.repository.RevokeOwnedSession(ctx, s.pool, playerID, strings.TrimSpace(sessionID), s.now().UTC())
	if err != nil {
		return internalError(err)
	}
	if !revoked {
		return &ServiceError{Status: 404, Code: CodeSessionNotFound, Message: "Session not found."}
	}
	if s.integritySessions != nil {
		s.integritySessions.RemoveSession(strings.TrimSpace(sessionID))
	}
	return nil
}

func (s *Service) RevokeOtherUserSessions(ctx context.Context, playerID, currentSessionID string) (int64, error) {
	if playerID == "" || currentSessionID == "" {
		return 0, unauthorized("Invalid access token.", nil)
	}
	revoked, err := s.repository.RevokeOtherSessions(ctx, s.pool, playerID, currentSessionID, s.now().UTC())
	if err != nil {
		return 0, internalError(err)
	}
	return revoked, nil
}

func (s *Service) ListRiskEvents(
	ctx context.Context,
	cursor, playerID, eventType, severity string,
	unresolvedOnly bool,
	limit int,
) (RiskEventList, error) {
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 100 {
		return RiskEventList{}, invalidRequest("Invalid limit.", map[string]any{"limit": "must be between 1 and 100"})
	}
	eventType = strings.ToUpper(strings.TrimSpace(eventType))
	severity = strings.ToUpper(strings.TrimSpace(severity))
	if eventType != "" && !validRiskEventTypes[eventType] {
		return RiskEventList{}, invalidRequest("Invalid risk event type.", nil)
	}
	if severity != "" && !validRiskSeverities[severity] {
		return RiskEventList{}, invalidRequest("Invalid risk severity.", nil)
	}
	items, err := s.repository.ListRiskEvents(
		ctx, s.pool, strings.TrimSpace(cursor), strings.TrimSpace(playerID),
		eventType, severity, unresolvedOnly, limit+1,
	)
	if err != nil {
		return RiskEventList{}, internalError(err)
	}
	nextCursor := ""
	if len(items) > limit {
		nextCursor = items[limit-1].ID
		items = items[:limit]
	}
	for index := range items {
		items[index].IPAddress = maskIPAddress(items[index].IPAddress)
		items[index].DeviceIDHash = nil
	}
	return RiskEventList{Items: items, NextCursor: nextCursor}, nil
}

func (s *Service) AuditBindDecodeFailure(ctx context.Context, meta RequestMeta) {
	s.recordFailedAudit(ctx, "", "AUTH_BIND_FAILURE", CodeInvalidRequest, sanitizeMeta(meta))
}

func (s *Service) newSession(
	playerID, familyID string,
	tokenVersion int,
	authProvider, authLevel string,
	meta RequestMeta,
	now time.Time,
) (Session, string, error) {
	rawRefreshToken, refreshHash, err := NewRefreshToken()
	if err != nil {
		return Session{}, "", err
	}
	deviceIDSuffix := DeviceIDSuffix(meta.DeviceID)
	if meta.DeviceFingerprintID != "" {
		// Structured fingerprints use the opaque internal record ID for display so
		// even a short suffix of an individual hardware factor is never exposed.
		deviceIDSuffix = DeviceIDSuffix(meta.DeviceFingerprintID)
	}
	return Session{
		ID:                  NewID("ses_"),
		PlayerID:            playerID,
		RefreshTokenHash:    refreshHash,
		TokenFamilyID:       familyID,
		TokenVersion:        tokenVersion,
		AuthProvider:        authProvider,
		AuthLevel:           authLevel,
		SteamVerified:       authLevel == player.AuthLevelVerified || authLevel == player.AuthLevelTrusted,
		DeviceIDHash:        HashDeviceID(meta.DeviceID),
		DeviceIDSuffix:      deviceIDSuffix,
		DeviceFingerprintID: meta.DeviceFingerprintID,
		IPAddress:           meta.IPAddress,
		UserAgent:           meta.UserAgent,
		ExpiresAt:           now.Add(s.config.RefreshTokenTTL()),
		CreatedAt:           now,
	}, rawRefreshToken, nil
}

func (s *Service) issueTokens(item player.Player, session Session, rawRefreshToken string) (SessionTokens, error) {
	accessToken, accessExpiresAt, err := s.tokens.Sign(
		item.ID,
		session.ID,
		session.AuthProvider,
		session.AuthLevel,
		session.TokenVersion,
		s.config.AccessTokenTTL(),
	)
	if err != nil {
		return SessionTokens{}, err
	}
	return SessionTokens{
		AccessToken:           accessToken,
		AccessTokenExpiresAt:  accessExpiresAt,
		RefreshToken:          rawRefreshToken,
		RefreshTokenExpiresAt: session.ExpiresAt,
		SessionID:             session.ID,
	}, nil
}

func (s *Service) recordFailedAudit(ctx context.Context, steamID, event, failureCode string, meta RequestMeta) {
	steamID = validSteamIDOrEmpty(steamID)
	now := s.now().UTC()
	err := s.repository.InsertAudit(ctx, s.pool, AuditEvent{
		ID:                  NewID("aal_"),
		SteamID:             steamID,
		Event:               event,
		Success:             false,
		FailureCode:         failureCode,
		RequestID:           meta.RequestID,
		IPAddress:           meta.IPAddress,
		UserAgent:           meta.UserAgent,
		DeviceIDHash:        HashDeviceID(meta.DeviceID),
		DeviceFingerprintID: meta.DeviceFingerprintID,
		CreatedAt:           now,
	})
	if err != nil {
		s.logger.ErrorContext(ctx, "write failed authentication audit", "event", event, "error", err)
	}
	if err := s.repository.InsertLoginEvent(ctx, s.pool, LoginEvent{
		ID:                  NewID("ale_"),
		SteamID:             steamID,
		DeviceIDHash:        HashDeviceID(meta.DeviceID),
		DeviceFingerprintID: meta.DeviceFingerprintID,
		IPAddress:           meta.IPAddress,
		UserAgent:           meta.UserAgent,
		Result:              "FAILURE",
		FailureCode:         failureCode,
		CreatedAt:           now,
	}); err != nil {
		s.logger.ErrorContext(ctx, "write failed login event", "event", event, "error", err)
	}
}

func (s *Service) rejectTicket(
	ctx context.Context,
	steamID, failureCode, reason string,
	meta RequestMeta,
	cause error,
) error {
	s.recordRiskEvent(ctx, RiskEvent{
		SteamID: validSteamIDOrEmpty(steamID), DeviceIDHash: HashDeviceID(meta.DeviceID),
		DeviceFingerprintID: meta.DeviceFingerprintID, IPAddress: meta.IPAddress,
		EventType: "STEAM_TICKET_VERIFY_FAILED", Severity: "MEDIUM",
		Details: map[string]any{"reason": reason},
	}, meta)
	s.recordFailedAudit(ctx, steamID, "STEAM_TICKET_VERIFY_FAILED", failureCode, meta)
	if cause != nil && s.logger != nil {
		s.logger.WarnContext(ctx, "Steam ticket verification rejected", "reason", reason, "error", cause)
	}
	return &ServiceError{
		Status: 401, Code: failureCode, Message: "Steam ticket verification failed.",
	}
}

func (s *Service) recordRiskEvent(ctx context.Context, event RiskEvent, meta RequestMeta) {
	event.ID = NewID("are_")
	event.CreatedAt = s.now().UTC()
	if event.IPAddress == "" {
		event.IPAddress = meta.IPAddress
	}
	if event.DeviceFingerprintID == "" {
		event.DeviceFingerprintID = meta.DeviceFingerprintID
	}
	if err := s.repository.InsertRiskEvent(ctx, s.pool, event); err != nil {
		s.logger.ErrorContext(ctx, "write authentication risk event", "event_type", event.EventType, "error", err)
	}
}

func (s *Service) resolveDeviceFingerprint(
	ctx context.Context,
	executor Executor,
	deviceID string,
	now time.Time,
) (string, error) {
	values, recognized, err := ParseDeviceFingerprint(deviceID)
	if err != nil {
		return "", err
	}
	if !recognized {
		return "", nil
	}
	if s.deviceFingerprinter == nil {
		return "", errors.New("device fingerprinter is not configured")
	}
	fingerprint := s.deviceFingerprinter.Fingerprint(values)
	fingerprint.ID = NewID("dfp_")
	fingerprint.FirstSeenAt = now
	fingerprint.LastSeenAt = now
	return s.repository.UpsertDeviceFingerprint(ctx, executor, fingerprint)
}

func validSteamIDOrEmpty(steamID string) string {
	if !steamIDPattern.MatchString(steamID) {
		return ""
	}
	return steamID
}

var validRiskEventTypes = map[string]bool{
	"BIND_RATE_LIMITED": true, "REFRESH_TOKEN_REUSE": true, "MULTI_DEVICE_LOGIN": true,
	"RAPID_IP_CHANGE": true, "MULTI_ACCOUNT_FROM_DEVICE": true, "MULTI_ACCOUNT_FROM_IP": true,
	"INVALID_STEAM_ID": true, "INVALID_INVITE_CODE": true, "REVOKED_SESSION_USAGE": true,
	"STEAM_TICKET_VERIFY_FAILED": true, "DEVICE_MISMATCH": true, "INTEGRITY_FAILED": true,
}

var validRiskSeverities = map[string]bool{"LOW": true, "MEDIUM": true, "HIGH": true, "CRITICAL": true}

func maskIPAddress(value string) string {
	ip := net.ParseIP(value)
	if ip == nil {
		if parsed, _, err := net.ParseCIDR(value); err == nil {
			ip = parsed
		}
	}
	if ip == nil {
		return ""
	}
	if ipv4 := ip.To4(); ipv4 != nil {
		return fmt.Sprintf("%d.%d.%d.xxx", ipv4[0], ipv4[1], ipv4[2])
	}
	ipv6 := ip.To16()
	return fmt.Sprintf("%x:%x:%x:%x::",
		binary.BigEndian.Uint16(ipv6[0:2]), binary.BigEndian.Uint16(ipv6[2:4]),
		binary.BigEndian.Uint16(ipv6[4:6]), binary.BigEndian.Uint16(ipv6[6:8]))
}

func sanitizeMeta(meta RequestMeta) RequestMeta {
	if net.ParseIP(meta.IPAddress) == nil {
		meta.IPAddress = ""
	}
	meta.RequestID = truncateRunes(strings.TrimSpace(meta.RequestID), 128)
	meta.UserAgent = truncateRunes(strings.TrimSpace(meta.UserAgent), 512)
	meta.DeviceID = strings.TrimSpace(meta.DeviceID)
	return meta
}

func truncateRunes(value string, max int) string {
	if utf8.RuneCountInString(value) <= max {
		return value
	}
	runes := []rune(value)
	return string(runes[:max])
}
