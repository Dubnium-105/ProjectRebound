package admin

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/projectrebound/matchserver/internal/api"
	"github.com/projectrebound/matchserver/internal/config"
	appmiddleware "github.com/projectrebound/matchserver/internal/middleware"
)

type adminContextKey uint8

const adminPrincipalKey adminContextKey = iota

type Principal struct {
	AdminID     string
	SessionID   string
	Username    string
	DisplayName string
	Roles       []string
	Permissions []string
	Automation  bool
}

type credential struct {
	adminID string
	hash    [32]byte
}

type Authenticator struct {
	credentials []credential
}

func (a *Authenticator) Configured() bool { return len(a.credentials) > 0 }

func NewAuthenticator(cfg config.AdminConfig) (*Authenticator, error) {
	tokenSet := strings.TrimSpace(cfg.TokenSet)
	if tokenSet == "" {
		return &Authenticator{}, nil
	}
	entries := strings.Split(tokenSet, ";")
	credentials := make([]credential, 0, len(entries))
	seen := make(map[string]struct{})
	for _, entry := range entries {
		adminID, token, ok := strings.Cut(entry, "=")
		adminID = strings.TrimSpace(adminID)
		token = strings.TrimSpace(token)
		if !ok || adminID == "" || len(token) < 32 {
			return nil, errors.New("ADMIN_TOKENS must contain admin_id=token entries with tokens of at least 32 bytes")
		}
		if _, duplicate := seen[adminID]; duplicate {
			return nil, fmt.Errorf("ADMIN_TOKENS contains duplicate admin ID %q", adminID)
		}
		credentials = append(credentials, credential{adminID: adminID, hash: sha256.Sum256([]byte(token))})
		seen[adminID] = struct{}{}
	}
	return &Authenticator{credentials: credentials}, nil
}

func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			api.WriteError(w, r, http.StatusUnauthorized, "ADMIN_UNAUTHORIZED", "Administrator authentication is required.", nil)
			return
		}
		supplied := sha256.Sum256([]byte(strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))))
		adminID := ""
		for _, candidate := range a.credentials {
			if subtle.ConstantTimeCompare(supplied[:], candidate.hash[:]) == 1 {
				adminID = candidate.adminID
			}
		}
		if adminID == "" {
			api.WriteError(w, r, http.StatusUnauthorized, "ADMIN_UNAUTHORIZED", "Administrator authentication is required.", nil)
			return
		}
		ctx := context.WithValue(r.Context(), adminPrincipalKey, &Principal{
			AdminID:    adminID,
			Roles:      []string{"AUTOMATION"},
			Automation: true,
		})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func PrincipalFromContext(ctx context.Context) *Principal {
	principal, _ := ctx.Value(adminPrincipalKey).(*Principal)
	return principal
}

func (p *Principal) HasPermission(permission string) bool {
	if p == nil {
		return false
	}
	for _, candidate := range p.Permissions {
		if candidate == permission {
			return true
		}
	}
	return false
}

type AccessAuthenticator interface {
	AuthenticateAccess(context.Context, string) (*Principal, error)
}

type StepUpAuthenticator interface {
	AuthenticateStepUp(context.Context, string, *Principal) error
}

type SessionAuthenticator struct {
	service AccessAuthenticator
}

func NewSessionAuthenticator(service AccessAuthenticator) *SessionAuthenticator {
	return &SessionAuthenticator{service: service}
}

func (a *SessionAuthenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			api.WriteError(w, r, http.StatusUnauthorized, "ADMIN_UNAUTHORIZED", "Administrator authentication is required.", nil)
			return
		}
		principal, err := a.service.AuthenticateAccess(
			r.Context(),
			strings.TrimSpace(strings.TrimPrefix(header, "Bearer ")),
		)
		if err != nil || principal == nil {
			api.WriteError(w, r, http.StatusUnauthorized, "ADMIN_UNAUTHORIZED", "Administrator authentication is required.", nil)
			return
		}
		ctx := context.WithValue(r.Context(), adminPrincipalKey, principal)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func RequirePermission(permission string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal := PrincipalFromContext(r.Context())
			if principal == nil || !principal.HasPermission(permission) {
				api.WriteError(w, r, http.StatusForbidden, "ADMIN_FORBIDDEN", "Administrator permission is required.", nil)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func RequireStepUp(service StepUpAuthenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal := PrincipalFromContext(r.Context())
			token := strings.TrimSpace(r.Header.Get("X-Admin-Step-Up"))
			if token == "" {
				api.WriteError(
					w, r, http.StatusForbidden, "ADMIN_STEP_UP_REQUIRED",
					"Recent multi-factor verification is required for this operation.", nil,
				)
				return
			}
			if err := service.AuthenticateStepUp(r.Context(), token, principal); err != nil {
				status, code, message, details := errorDetails(err)
				api.WriteError(w, r, status, code, message, details)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

type NetworkGuard struct {
	networks   []*net.IPNet
	trustProxy bool
}

func NewNetworkGuard(cidrs []string, trustProxy bool) (*NetworkGuard, error) {
	networks := make([]*net.IPNet, 0, len(cidrs))
	for _, raw := range cidrs {
		_, network, err := net.ParseCIDR(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid admin trusted CIDR %q: %w", raw, err)
		}
		networks = append(networks, network)
	}
	return &NetworkGuard{networks: networks, trustProxy: trustProxy}, nil
}

func (g *NetworkGuard) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clientIP := net.ParseIP(appmiddleware.ClientIP(r, g.trustProxy))
		allowed := false
		for _, network := range g.networks {
			if network.Contains(clientIP) {
				allowed = true
				break
			}
		}
		if !allowed {
			api.WriteError(w, r, http.StatusNotFound, "NOT_FOUND", "Resource not found.", nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}
