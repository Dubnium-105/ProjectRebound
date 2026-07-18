package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/projectrebound/matchserver/internal/config"
	"github.com/projectrebound/matchserver/internal/player"
)

type Service struct {
	pool       *pgxpool.Pool
	repository *Repository
	players    *player.Repository
	tokens     *TokenManager
	config     config.AuthConfig
	logger     *slog.Logger
	now        func() time.Time
	metrics    interface{ RefreshTokenReuse() }
}

func (s *Service) SetMetrics(metrics interface{ RefreshTokenReuse() }) { s.metrics = metrics }

func NewService(
	pool *pgxpool.Pool,
	repository *Repository,
	players *player.Repository,
	tokens *TokenManager,
	cfg config.AuthConfig,
	logger *slog.Logger,
) *Service {
	return &Service{
		pool:       pool,
		repository: repository,
		players:    players,
		tokens:     tokens,
		config:     cfg,
		logger:     logger,
		now:        time.Now,
	}
}

func (s *Service) Bind(ctx context.Context, input BindInput, meta RequestMeta) (BindResult, error) {
	meta = sanitizeMeta(meta)
	if err := ValidateSteamID(input.SteamID); err != nil {
		s.recordFailedAudit(ctx, input.SteamID, "AUTH_BIND_FAILURE", CodeInvalidRequest, meta)
		return BindResult{}, invalidRequest("Invalid SteamID.", map[string]any{"steam_id": err.Error()})
	}
	personaName, err := NormalizePersonaName(input.PersonaName, s.config.DefaultPersonaName)
	if err != nil {
		s.recordFailedAudit(ctx, input.SteamID, "AUTH_BIND_FAILURE", CodeInvalidRequest, meta)
		return BindResult{}, invalidRequest("Invalid persona name.", map[string]any{"persona_name": err.Error()})
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return BindResult{}, internalError(fmt.Errorf("begin bind transaction: %w", err))
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	now := s.now().UTC()
	item, isNew, err := s.players.UpsertSteamIdentity(ctx, tx, NewID("p_"), input.SteamID, personaName, now)
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

	session, rawRefreshToken, err := s.newSession(item.ID, NewID("fam_"), 1, meta, now)
	if err != nil {
		return BindResult{}, internalError(err)
	}
	if err := s.repository.InsertSession(ctx, tx, session); err != nil {
		return BindResult{}, internalError(err)
	}
	issued, err := s.issueTokens(item, session, rawRefreshToken)
	if err != nil {
		return BindResult{}, internalError(fmt.Errorf("issue bind tokens: %w", err))
	}
	if err := s.repository.InsertAudit(ctx, tx, AuditEvent{
		ID:        NewID("aal_"),
		PlayerID:  item.ID,
		SteamID:   item.SteamID,
		Event:     "AUTH_BIND_SUCCESS",
		Success:   true,
		RequestID: meta.RequestID,
		IPAddress: meta.IPAddress,
		UserAgent: meta.UserAgent,
		CreatedAt: now,
	}); err != nil {
		return BindResult{}, internalError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return BindResult{}, internalError(fmt.Errorf("commit bind transaction: %w", err))
	}
	return BindResult{Player: item, Tokens: issued, IsNewPlayer: isNew}, nil
}

func (s *Service) Refresh(ctx context.Context, refreshToken string, meta RequestMeta) (RefreshResult, error) {
	meta = sanitizeMeta(meta)
	if !strings.HasPrefix(refreshToken, "rfr_") || len(refreshToken) < 64 {
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
			return RefreshResult{}, unauthorized("Invalid refresh token.", nil)
		}
		return RefreshResult{}, internalError(fmt.Errorf("find refresh session: %w", err))
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
		return RefreshResult{}, &ServiceError{Status: 401, Code: CodeSessionRevoked, Message: "Session has been revoked."}
	}
	if !current.ExpiresAt.After(now) {
		if err := s.repository.RevokeSession(ctx, tx, current.ID, now, "EXPIRED"); err != nil {
			return RefreshResult{}, internalError(err)
		}
		if err := tx.Commit(ctx); err != nil {
			return RefreshResult{}, internalError(err)
		}
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
		return RefreshResult{}, &ServiceError{Status: 403, Code: CodeAccountDeleted, Message: "Account has been deleted."}
	}

	replacement, rawRefreshToken, err := s.newSession(item.ID, current.TokenFamilyID, current.TokenVersion+1, meta, now)
	if err != nil {
		return RefreshResult{}, internalError(err)
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
	if err := tx.Commit(ctx); err != nil {
		return RefreshResult{}, internalError(fmt.Errorf("commit refresh transaction: %w", err))
	}
	return RefreshResult{Tokens: issued}, nil
}

func (s *Service) AuthenticateAccess(ctx context.Context, accessToken string) (Principal, error) {
	claims, err := s.tokens.Verify(accessToken)
	if err != nil {
		return Principal{}, unauthorized("Invalid access token.", err)
	}
	session, err := s.repository.GetSession(ctx, s.pool, claims.SessionID)
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
		return Principal{}, &ServiceError{Status: 401, Code: CodeSessionRevoked, Message: "Session has been revoked."}
	}
	item, err := s.players.GetByID(ctx, s.pool, claims.Subject)
	if err != nil {
		return Principal{}, internalError(err)
	}
	if item.AuthProvider != claims.Provider || item.AuthLevel != claims.AuthLevel {
		return Principal{}, unauthorized("Invalid access token.", nil)
	}
	if item.AccountStatus == player.AccountStatusDeleted {
		return Principal{}, &ServiceError{Status: 403, Code: CodeAccountDeleted, Message: "Account has been deleted."}
	}
	return Principal{Player: item, SessionID: session.ID}, nil
}

func (s *Service) Logout(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return unauthorized("Invalid access token.", nil)
	}
	if err := s.repository.RevokeSession(ctx, s.pool, sessionID, s.now().UTC(), "LOGOUT"); err != nil {
		return internalError(err)
	}
	return nil
}

func (s *Service) AuditBindDecodeFailure(ctx context.Context, meta RequestMeta) {
	s.recordFailedAudit(ctx, "", "AUTH_BIND_FAILURE", CodeInvalidRequest, sanitizeMeta(meta))
}

func (s *Service) newSession(playerID, familyID string, tokenVersion int, meta RequestMeta, now time.Time) (Session, string, error) {
	rawRefreshToken, refreshHash, err := NewRefreshToken()
	if err != nil {
		return Session{}, "", err
	}
	return Session{
		ID:               NewID("ses_"),
		PlayerID:         playerID,
		RefreshTokenHash: refreshHash,
		TokenFamilyID:    familyID,
		TokenVersion:     tokenVersion,
		DeviceID:         meta.DeviceID,
		IPAddress:        meta.IPAddress,
		UserAgent:        meta.UserAgent,
		ExpiresAt:        now.Add(s.config.RefreshTokenTTL()),
		CreatedAt:        now,
	}, rawRefreshToken, nil
}

func (s *Service) issueTokens(item player.Player, session Session, rawRefreshToken string) (SessionTokens, error) {
	accessToken, accessExpiresAt, err := s.tokens.Sign(
		item.ID,
		session.ID,
		item.AuthProvider,
		item.AuthLevel,
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
	if !steamIDPattern.MatchString(steamID) {
		steamID = ""
	}
	err := s.repository.InsertAudit(ctx, s.pool, AuditEvent{
		ID:          NewID("aal_"),
		SteamID:     steamID,
		Event:       event,
		Success:     false,
		FailureCode: failureCode,
		RequestID:   meta.RequestID,
		IPAddress:   meta.IPAddress,
		UserAgent:   meta.UserAgent,
		CreatedAt:   s.now().UTC(),
	})
	if err != nil {
		s.logger.ErrorContext(ctx, "write failed authentication audit", "event", event, "error", err)
	}
}

func sanitizeMeta(meta RequestMeta) RequestMeta {
	if net.ParseIP(meta.IPAddress) == nil {
		meta.IPAddress = ""
	}
	meta.RequestID = truncateRunes(strings.TrimSpace(meta.RequestID), 128)
	meta.UserAgent = truncateRunes(strings.TrimSpace(meta.UserAgent), 512)
	meta.DeviceID = truncateRunes(strings.TrimSpace(meta.DeviceID), 128)
	return meta
}

func truncateRunes(value string, max int) string {
	if utf8.RuneCountInString(value) <= max {
		return value
	}
	runes := []rune(value)
	return string(runes[:max])
}
