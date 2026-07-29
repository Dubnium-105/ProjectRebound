package admin

import (
	"context"
	"errors"
	"strings"
	"time"

	playerauth "github.com/Dubnium-105/ProjectRebound/Backend/internal/auth"
	"github.com/jackc/pgx/v5"
)

// SessionVerifier exposes only administrator access and step-up verification.
// It is used by services that must authorize administrator actions but must
// never receive an administrator signing or MFA-encryption key.
type SessionVerifier struct {
	repository   *AuthRepository
	tokenManager *playerauth.TokenManager
	now          func() time.Time
}

func NewSessionVerifier(
	repository *AuthRepository,
	tokenManager *playerauth.TokenManager,
) *SessionVerifier {
	return &SessionVerifier{
		repository: repository, tokenManager: tokenManager, now: time.Now,
	}
}

func (s *SessionVerifier) AuthenticateAccess(
	ctx context.Context,
	token string,
) (*Principal, error) {
	claims, err := s.tokenManager.Verify(strings.TrimSpace(token))
	if err != nil || claims.Provider != "admin" || claims.AuthLevel != "mfa" {
		return nil, adminSessionUnauthorized()
	}
	current, tokenVersion, expiresAt, revokedAt, err := s.repository.GetCurrentAdminAuthorization(
		ctx, claims.SessionID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, adminSessionUnauthorized()
	}
	if err != nil {
		return nil, internal(err)
	}
	if current.User.ID != claims.Subject ||
		current.User.Status != AdminStatusActive ||
		tokenVersion != claims.TokenVersion ||
		revokedAt != nil ||
		!expiresAt.After(s.now().UTC()) {
		return nil, adminSessionUnauthorized()
	}
	return &Principal{
		AdminID: current.User.ID, SessionID: current.SessionID,
		Username: current.User.Username, DisplayName: current.User.DisplayName,
		Roles: current.Roles, Permissions: current.Permissions,
	}, nil
}

func (s *SessionVerifier) AuthenticateStepUp(
	ctx context.Context,
	token string,
	principal *Principal,
) error {
	if principal == nil || principal.Automation {
		return stepUpRequired()
	}
	claims, err := s.tokenManager.Verify(strings.TrimSpace(token))
	if err != nil ||
		claims.Provider != "admin" ||
		claims.AuthLevel != "step_up" ||
		claims.Subject != principal.AdminID ||
		claims.SessionID != principal.SessionID {
		return stepUpRequired()
	}
	current, tokenVersion, expiresAt, revokedAt, err := s.repository.GetCurrentAdminAuthorization(
		ctx, claims.SessionID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return stepUpRequired()
	}
	if err != nil {
		return internal(err)
	}
	if current.User.ID != principal.AdminID ||
		current.User.Status != AdminStatusActive ||
		tokenVersion != claims.TokenVersion ||
		revokedAt != nil ||
		!expiresAt.After(s.now().UTC()) {
		return stepUpRequired()
	}
	return nil
}
