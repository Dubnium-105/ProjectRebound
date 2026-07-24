package admin

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	playerauth "github.com/projectrebound/matchserver/internal/auth"
	"github.com/projectrebound/matchserver/internal/config"
)

type AdminAuthService struct {
	pool         *pgxpool.Pool
	repository   *AuthRepository
	turnstile    TurnstileVerifier
	limiter      LoginLimiter
	tokenManager *playerauth.TokenManager
	secretBox    *SecretBox
	cfg          config.AdminConfig
	logger       *slog.Logger
	dummyHash    string
	now          func() time.Time
}

func NewAdminAuthService(
	pool *pgxpool.Pool,
	repository *AuthRepository,
	turnstile TurnstileVerifier,
	limiter LoginLimiter,
	tokenManager *playerauth.TokenManager,
	secretBox *SecretBox,
	cfg config.AdminConfig,
	logger *slog.Logger,
) (*AdminAuthService, error) {
	dummyHash, err := HashPassword("not-a-real-administrator-password")
	if err != nil {
		return nil, fmt.Errorf("initialize administrator password verifier: %w", err)
	}
	return &AdminAuthService{
		pool:         pool,
		repository:   repository,
		turnstile:    turnstile,
		limiter:      limiter,
		tokenManager: tokenManager,
		secretBox:    secretBox,
		cfg:          cfg,
		logger:       logger,
		dummyHash:    dummyHash,
		now:          time.Now,
	}, nil
}

func (s *AdminAuthService) TurnstileSiteKey() string {
	if s.turnstile == nil {
		return ""
	}
	return s.turnstile.SiteKey()
}

func (s *AdminAuthService) TurnstileConfigured() bool {
	return s.turnstile != nil && s.turnstile.Configured()
}

func (s *AdminAuthService) Login(
	ctx context.Context,
	input LoginInput,
	meta RequestMeta,
) (LoginResult, error) {
	meta = sanitizeMeta(meta)
	username := normalizeUsername(input.Username)
	if username == "" || len(username) > 128 || input.Password == "" {
		return LoginResult{}, adminLoginFailed()
	}
	if s.limiter != nil {
		decision := s.limiter.Check(ctx, meta.IPAddress, username)
		if !decision.Allowed {
			s.auditLogin(ctx, nil, username, "", "LOGIN", "FAILURE", "RATE_LIMITED", meta, nil)
			return LoginResult{}, &ServiceError{
				Status:  http.StatusTooManyRequests,
				Code:    "ADMIN_LOGIN_RATE_LIMITED",
				Message: "Too many sign-in attempts. Try again later.",
				Details: map[string]any{
					"retry_after_seconds": int(decision.RetryAfter.Round(time.Second).Seconds()),
				},
			}
		}
	}

	if s.turnstile == nil {
		s.auditLogin(ctx, nil, username, "", "LOGIN", "FAILURE", "TURNSTILE_UNAVAILABLE", meta, nil)
		return LoginResult{}, &ServiceError{
			Status:  http.StatusServiceUnavailable,
			Code:    "ADMIN_SECURITY_CHECK_UNAVAILABLE",
			Message: "Security verification is temporarily unavailable. Try again.",
		}
	}
	turnstileResult, err := s.turnstile.Verify(ctx, input.TurnstileToken, meta.IPAddress)
	if err != nil {
		s.auditLogin(ctx, nil, username, "", "LOGIN", "FAILURE", "TURNSTILE_UNAVAILABLE", meta, &turnstileResult)
		s.logger.WarnContext(ctx, "administrator Turnstile verification unavailable", "error", err)
		return LoginResult{}, &ServiceError{
			Status:  http.StatusServiceUnavailable,
			Code:    "ADMIN_SECURITY_CHECK_UNAVAILABLE",
			Message: "Security verification is temporarily unavailable. Try again.",
		}
	}
	if !turnstileResult.Success {
		s.auditLogin(ctx, nil, username, "", "LOGIN", "FAILURE", "TURNSTILE_REJECTED", meta, &turnstileResult)
		return LoginResult{}, adminLoginFailed()
	}

	user, err := s.repository.FindUserByUsername(ctx, username)
	if errors.Is(err, pgx.ErrNoRows) {
		_ = VerifyPassword(s.dummyHash, input.Password)
		s.auditLogin(ctx, nil, username, "", "LOGIN", "FAILURE", "INVALID_CREDENTIALS", meta, &turnstileResult)
		return LoginResult{}, adminLoginFailed()
	}
	if err != nil {
		return LoginResult{}, internal(fmt.Errorf("find administrator by username: %w", err))
	}
	if user.Status != AdminStatusActive || !VerifyPassword(user.PasswordHash, input.Password) {
		s.auditLogin(ctx, nil, username, user.ID, "LOGIN", "FAILURE", "INVALID_CREDENTIALS", meta, &turnstileResult)
		return LoginResult{}, adminLoginFailed()
	}
	if !user.MFARequired {
		s.logger.ErrorContext(ctx, "administrator account does not require MFA", "admin_id", user.ID)
		s.auditLogin(ctx, nil, username, user.ID, "LOGIN", "FAILURE", "MFA_NOT_REQUIRED", meta, &turnstileResult)
		return LoginResult{}, adminLoginFailed()
	}
	if _, err := s.repository.GetMFASecret(ctx, user.ID); err != nil {
		s.logger.ErrorContext(ctx, "administrator account has no verified MFA credential", "admin_id", user.ID, "error", err)
		s.auditLogin(ctx, nil, username, user.ID, "LOGIN", "FAILURE", "MFA_NOT_CONFIGURED", meta, &turnstileResult)
		return LoginResult{}, adminLoginFailed()
	}

	challengeToken, challengeHash, err := newOpaqueAdminToken("amc_", 32)
	if err != nil {
		return LoginResult{}, internal(err)
	}
	now := s.now().UTC()
	challenge := LoginChallenge{
		ID:        playerauth.NewID("amc_"),
		Admin:     user,
		TokenHash: challengeHash,
		RequestID: meta.RequestID,
		IPAddress: meta.IPAddress,
		UserAgent: meta.UserAgent,
		ExpiresAt: now.Add(s.cfg.LoginChallengeTTL()),
		CreatedAt: now,
	}
	if err := s.repository.InsertLoginChallenge(ctx, challenge); err != nil {
		return LoginResult{}, internal(err)
	}
	if err := s.auditLogin(ctx, nil, username, user.ID, "PASSWORD_ACCEPTED", "SUCCESS", "", meta, &turnstileResult); err != nil {
		return LoginResult{}, internal(err)
	}
	return LoginResult{
		MFARequired:    true,
		ChallengeToken: challengeToken,
		ExpiresAt:      challenge.ExpiresAt,
	}, nil
}

func (s *AdminAuthService) VerifyMFA(
	ctx context.Context,
	input MFAVerifyInput,
	meta RequestMeta,
) (MFAVerifyResult, error) {
	meta = sanitizeMeta(meta)
	challengeHash := hashOpaqueAdminToken(input.ChallengeToken)
	if len(challengeHash) == 0 || strings.TrimSpace(input.Code) == "" {
		return MFAVerifyResult{}, adminMFAFailed()
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return MFAVerifyResult{}, internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	challenge, err := s.repository.FindLoginChallengeForUpdate(ctx, tx, challengeHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return MFAVerifyResult{}, adminMFAFailed()
	}
	if err != nil {
		return MFAVerifyResult{}, internal(err)
	}
	now := s.now().UTC()
	if challenge.Admin.Status != AdminStatusActive || !challenge.ExpiresAt.After(now) || challenge.Attempts >= 5 {
		_ = s.repository.DeleteLoginChallenge(ctx, tx, challenge.ID)
		if err := s.auditLogin(ctx, tx, challenge.Admin.Username, challenge.Admin.ID, "MFA", "FAILURE", "CHALLENGE_EXPIRED", meta, nil); err != nil {
			return MFAVerifyResult{}, internal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			return MFAVerifyResult{}, internal(err)
		}
		return MFAVerifyResult{}, adminMFAFailed()
	}
	secret, err := s.secretBox.Decrypt(challenge.Admin.ID, challenge.SecretCiphertext)
	if err != nil {
		s.logger.ErrorContext(ctx, "decrypt administrator MFA credential", "admin_id", challenge.Admin.ID, "error", err)
		return MFAVerifyResult{}, internal(err)
	}
	valid := ValidateTOTP(secret, input.Code, now)
	if !valid {
		valid, err = s.repository.ConsumeRecoveryCode(
			ctx,
			tx,
			challenge.Admin.ID,
			HashRecoveryCode(input.Code),
			now,
		)
		if err != nil {
			return MFAVerifyResult{}, internal(err)
		}
	}
	if !valid {
		attempts := challenge.Attempts + 1
		if err := s.repository.RecordFailedMFA(ctx, tx, challenge.ID, attempts); err != nil {
			return MFAVerifyResult{}, internal(err)
		}
		if err := s.auditLogin(ctx, tx, challenge.Admin.Username, challenge.Admin.ID, "MFA", "FAILURE", "INVALID_MFA_CODE", meta, nil); err != nil {
			return MFAVerifyResult{}, internal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			return MFAVerifyResult{}, internal(err)
		}
		return MFAVerifyResult{}, adminMFAFailed()
	}

	refreshToken, refreshHash, err := playerauth.NewRefreshToken()
	if err != nil {
		return MFAVerifyResult{}, internal(err)
	}
	session := AdminSession{
		ID:               playerauth.NewID("adm_ses_"),
		AdminID:          challenge.Admin.ID,
		RefreshTokenHash: refreshHash,
		TokenVersion:     1,
		IPAddress:        meta.IPAddress,
		UserAgent:        meta.UserAgent,
		CreatedAt:        now,
		ExpiresAt:        now.Add(s.cfg.RefreshTokenTTL()),
	}
	accessToken, accessExpiresAt, err := s.tokenManager.Sign(
		challenge.Admin.ID,
		session.ID,
		"admin",
		"mfa",
		session.TokenVersion,
		s.cfg.AccessTokenTTL(),
	)
	if err != nil {
		return MFAVerifyResult{}, internal(err)
	}
	if err := s.repository.InsertSession(ctx, tx, session); err != nil {
		return MFAVerifyResult{}, internal(err)
	}
	if err := s.repository.DeleteLoginChallenge(ctx, tx, challenge.ID); err != nil {
		return MFAVerifyResult{}, internal(err)
	}
	if err := s.repository.UpdateLastLogin(ctx, tx, challenge.Admin.ID, now); err != nil {
		return MFAVerifyResult{}, internal(err)
	}
	roles, permissions, err := s.repository.LoadAccess(ctx, tx, challenge.Admin.ID)
	if err != nil {
		return MFAVerifyResult{}, internal(err)
	}
	if err := s.auditLogin(ctx, tx, challenge.Admin.Username, challenge.Admin.ID, "LOGIN", "SUCCESS", "", meta, nil); err != nil {
		return MFAVerifyResult{}, internal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return MFAVerifyResult{}, internal(err)
	}
	challenge.Admin.LastLoginAt = &now
	return MFAVerifyResult{
		Tokens: AdminTokens{
			AccessToken:          accessToken,
			AccessTokenExpiresAt: accessExpiresAt,
			RefreshToken:         refreshToken,
			RefreshExpiresAt:     session.ExpiresAt,
		},
		Admin: CurrentAdmin{
			User:        challenge.Admin,
			SessionID:   session.ID,
			Roles:       roles,
			Permissions: permissions,
		},
	}, nil
}

func (s *AdminAuthService) Refresh(
	ctx context.Context,
	refreshToken string,
	meta RequestMeta,
) (RefreshAdminResult, error) {
	meta = sanitizeMeta(meta)
	refreshHash := hashOpaqueAdminToken(refreshToken)
	if len(refreshHash) == 0 {
		return RefreshAdminResult{}, adminSessionUnauthorized()
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return RefreshAdminResult{}, internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	session, user, err := s.repository.FindSessionByRefreshForUpdate(ctx, tx, refreshHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return RefreshAdminResult{}, adminSessionUnauthorized()
	}
	if err != nil {
		return RefreshAdminResult{}, internal(err)
	}
	now := s.now().UTC()
	if len(session.PreviousRefreshTokenHash) > 0 && bytes.Equal(refreshHash, session.PreviousRefreshTokenHash) {
		if err := s.repository.RevokeSession(ctx, tx, session.ID, now, "REFRESH_REUSE"); err != nil {
			return RefreshAdminResult{}, internal(err)
		}
		if err := s.auditLogin(ctx, tx, user.Username, user.ID, "REFRESH", "FAILURE", "REFRESH_REUSE", meta, nil); err != nil {
			return RefreshAdminResult{}, internal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			return RefreshAdminResult{}, internal(err)
		}
		return RefreshAdminResult{}, adminSessionUnauthorized()
	}
	if user.Status != AdminStatusActive || session.RevokedAt != nil || !session.ExpiresAt.After(now) ||
		!bytes.Equal(refreshHash, session.RefreshTokenHash) {
		return RefreshAdminResult{}, adminSessionUnauthorized()
	}
	replacementToken, replacementHash, err := playerauth.NewRefreshToken()
	if err != nil {
		return RefreshAdminResult{}, internal(err)
	}
	nextVersion := session.TokenVersion + 1
	accessToken, accessExpiresAt, err := s.tokenManager.Sign(
		user.ID,
		session.ID,
		"admin",
		"mfa",
		nextVersion,
		s.cfg.AccessTokenTTL(),
	)
	if err != nil {
		return RefreshAdminResult{}, internal(err)
	}
	if err := s.repository.RotateSession(
		ctx,
		tx,
		session.ID,
		session.RefreshTokenHash,
		replacementHash,
		nextVersion,
		now,
	); err != nil {
		return RefreshAdminResult{}, internal(err)
	}
	roles, permissions, err := s.repository.LoadAccess(ctx, tx, user.ID)
	if err != nil {
		return RefreshAdminResult{}, internal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return RefreshAdminResult{}, internal(err)
	}
	return RefreshAdminResult{
		Tokens: AdminTokens{
			AccessToken:          accessToken,
			AccessTokenExpiresAt: accessExpiresAt,
			RefreshToken:         replacementToken,
			RefreshExpiresAt:     session.ExpiresAt,
		},
		Admin: CurrentAdmin{
			User:        user,
			SessionID:   session.ID,
			Roles:       roles,
			Permissions: permissions,
		},
	}, nil
}

func (s *AdminAuthService) AuthenticateAccess(ctx context.Context, token string) (*Principal, error) {
	claims, err := s.tokenManager.Verify(strings.TrimSpace(token))
	if err != nil || claims.Provider != "admin" || claims.AuthLevel != "mfa" {
		return nil, adminSessionUnauthorized()
	}
	current, tokenVersion, expiresAt, revokedAt, err := s.repository.GetCurrentAdmin(ctx, claims.SessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, adminSessionUnauthorized()
	}
	if err != nil {
		return nil, internal(err)
	}
	now := s.now().UTC()
	if current.User.ID != claims.Subject ||
		current.User.Status != AdminStatusActive ||
		tokenVersion != claims.TokenVersion ||
		revokedAt != nil ||
		!expiresAt.After(now) {
		return nil, adminSessionUnauthorized()
	}
	return &Principal{
		AdminID:     current.User.ID,
		SessionID:   current.SessionID,
		Username:    current.User.Username,
		DisplayName: current.User.DisplayName,
		Roles:       current.Roles,
		Permissions: current.Permissions,
	}, nil
}

func (s *AdminAuthService) StepUp(
	ctx context.Context,
	adminID, sessionID, code string,
	meta RequestMeta,
) (StepUpResult, error) {
	meta = sanitizeMeta(meta)
	code = strings.TrimSpace(code)
	if adminID == "" || sessionID == "" || code == "" {
		return StepUpResult{}, adminMFAFailed()
	}
	current, tokenVersion, expiresAt, revokedAt, err := s.repository.GetCurrentAdmin(ctx, sessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return StepUpResult{}, adminSessionUnauthorized()
	}
	if err != nil {
		return StepUpResult{}, internal(err)
	}
	now := s.now().UTC()
	if current.User.ID != adminID || current.User.Status != AdminStatusActive ||
		revokedAt != nil || !expiresAt.After(now) {
		return StepUpResult{}, adminSessionUnauthorized()
	}
	ciphertext, err := s.repository.GetMFASecret(ctx, adminID)
	if err != nil {
		return StepUpResult{}, internal(err)
	}
	secret, err := s.secretBox.Decrypt(adminID, ciphertext)
	if err != nil {
		s.logger.ErrorContext(ctx, "decrypt administrator step-up MFA credential", "admin_id", adminID, "error", err)
		return StepUpResult{}, internal(err)
	}
	valid := ValidateTOTP(secret, code, now)
	if !valid {
		tx, beginErr := s.pool.BeginTx(ctx, pgx.TxOptions{})
		if beginErr != nil {
			return StepUpResult{}, internal(beginErr)
		}
		defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
		valid, err = s.repository.ConsumeRecoveryCode(ctx, tx, adminID, HashRecoveryCode(code), now)
		if err != nil {
			return StepUpResult{}, internal(err)
		}
		result, reason := "SUCCESS", ""
		if !valid {
			result, reason = "FAILURE", "INVALID_MFA_CODE"
		}
		if err := s.auditLogin(
			ctx, tx, current.User.Username, adminID, "STEP_UP", result, reason, meta, nil,
		); err != nil {
			return StepUpResult{}, internal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			return StepUpResult{}, internal(err)
		}
		if !valid {
			return StepUpResult{}, adminMFAFailed()
		}
	} else if err := s.auditLogin(
		ctx, nil, current.User.Username, adminID, "STEP_UP", "SUCCESS", "", meta, nil,
	); err != nil {
		return StepUpResult{}, internal(err)
	}
	token, tokenExpiresAt, err := s.tokenManager.Sign(
		adminID, sessionID, "admin", "step_up", tokenVersion, s.cfg.StepUpTTL(),
	)
	if err != nil {
		return StepUpResult{}, internal(err)
	}
	return StepUpResult{Token: token, ExpiresAt: tokenExpiresAt}, nil
}

func (s *AdminAuthService) AuthenticateStepUp(
	ctx context.Context,
	token string,
	principal *Principal,
) error {
	if principal == nil || principal.Automation {
		return stepUpRequired()
	}
	claims, err := s.tokenManager.Verify(strings.TrimSpace(token))
	if err != nil || claims.Provider != "admin" || claims.AuthLevel != "step_up" ||
		claims.Subject != principal.AdminID || claims.SessionID != principal.SessionID {
		return stepUpRequired()
	}
	current, tokenVersion, expiresAt, revokedAt, err := s.repository.GetCurrentAdmin(ctx, claims.SessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return stepUpRequired()
	}
	if err != nil {
		return internal(err)
	}
	if current.User.ID != principal.AdminID || current.User.Status != AdminStatusActive ||
		tokenVersion != claims.TokenVersion || revokedAt != nil || !expiresAt.After(s.now().UTC()) {
		return stepUpRequired()
	}
	return nil
}

func (s *AdminAuthService) Logout(ctx context.Context, sessionID string) error {
	if strings.TrimSpace(sessionID) == "" {
		return adminSessionUnauthorized()
	}
	return s.repository.RevokeSession(ctx, s.pool, sessionID, s.now().UTC(), "LOGOUT")
}

func (s *AdminAuthService) ListSessions(
	ctx context.Context,
	adminID, currentSessionID string,
) ([]SessionListItem, error) {
	return s.repository.ListSessions(ctx, adminID, currentSessionID, s.now().UTC())
}

func (s *AdminAuthService) RevokeOwnedSession(
	ctx context.Context,
	adminID, sessionID string,
) error {
	revoked, err := s.repository.RevokeOwnedSession(
		ctx,
		s.pool,
		adminID,
		sessionID,
		s.now().UTC(),
		"ADMIN_REVOKED",
	)
	if err != nil {
		return internal(err)
	}
	if !revoked {
		return &ServiceError{
			Status:  http.StatusNotFound,
			Code:    "ADMIN_SESSION_NOT_FOUND",
			Message: "Administrator session not found.",
		}
	}
	return nil
}

func (s *AdminAuthService) auditLogin(
	ctx context.Context,
	executor adminAuthExecutor,
	username, adminID, eventType, result, reason string,
	meta RequestMeta,
	turnstile *TurnstileResult,
) error {
	if executor == nil {
		executor = s.pool
	}
	audit := LoginAudit{
		ID:           playerauth.NewID("adla_"),
		AdminID:      adminID,
		UsernameHash: hashUsername(username),
		EventType:    eventType,
		Result:       result,
		ReasonCode:   reason,
		RequestID:    meta.RequestID,
		IPAddress:    meta.IPAddress,
		UserAgent:    meta.UserAgent,
		CreatedAt:    s.now().UTC(),
	}
	if turnstile != nil {
		success := turnstile.Success
		latencyMilliseconds := int(turnstile.Latency.Milliseconds())
		audit.TurnstileSuccess = &success
		audit.TurnstileErrorCodes = append([]string(nil), turnstile.ErrorCodes...)
		audit.TurnstileHostname = turnstile.Hostname
		audit.TurnstileAction = turnstile.Action
		audit.TurnstileVerifyLatencyMS = &latencyMilliseconds
	}
	if err := s.repository.InsertLoginAudit(ctx, executor, audit); err != nil {
		s.logger.ErrorContext(ctx, "record administrator login audit", "event_type", eventType, "error", err)
		return fmt.Errorf("record administrator login audit: %w", err)
	}
	return nil
}

func newOpaqueAdminToken(prefix string, length int) (string, []byte, error) {
	randomBytes := make([]byte, length)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", nil, fmt.Errorf("generate administrator token: %w", err)
	}
	token := prefix + base64.RawURLEncoding.EncodeToString(randomBytes)
	return token, hashOpaqueAdminToken(token), nil
}

func hashOpaqueAdminToken(token string) []byte {
	token = strings.TrimSpace(token)
	if token == "" || len(token) > 4096 {
		return nil
	}
	hash := sha256.Sum256([]byte(token))
	return hash[:]
}

func hashUsername(username string) string {
	username = normalizeUsername(username)
	if username == "" {
		return ""
	}
	hash := sha256.Sum256([]byte(username))
	return hex.EncodeToString(hash[:])
}

func adminLoginFailed() error {
	return &ServiceError{
		Status:  http.StatusUnauthorized,
		Code:    "ADMIN_LOGIN_FAILED",
		Message: "Unable to sign in with the supplied credentials.",
	}
}

func adminMFAFailed() error {
	return &ServiceError{
		Status:  http.StatusUnauthorized,
		Code:    "ADMIN_MFA_FAILED",
		Message: "Unable to verify the authentication code.",
	}
}

func stepUpRequired() error {
	return &ServiceError{
		Status: http.StatusForbidden, Code: "ADMIN_STEP_UP_REQUIRED",
		Message: "Recent multi-factor verification is required for this operation.",
	}
}

func adminSessionUnauthorized() error {
	return &ServiceError{
		Status:  http.StatusUnauthorized,
		Code:    "ADMIN_UNAUTHORIZED",
		Message: "Administrator authentication is required.",
	}
}
