package auth

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/api"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/player"
)

type principalContextKey uint8

const principalKey principalContextKey = iota

type AccessAuthenticator interface {
	AuthenticateAccess(context.Context, string) (Principal, error)
}

func RequireAccess(authenticator AccessAuthenticator, logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			header := r.Header.Get("Authorization")
			if !strings.HasPrefix(header, "Bearer ") || strings.TrimSpace(strings.TrimPrefix(header, "Bearer ")) == "" {
				api.WriteError(w, r, http.StatusUnauthorized, CodeUnauthorized, "Bearer access token is required.", nil)
				return
			}
			principal, err := authenticator.AuthenticateAccess(r.Context(), strings.TrimSpace(strings.TrimPrefix(header, "Bearer ")))
			if err != nil {
				status, code, message, details := ErrorDetails(err)
				if status >= 500 {
					logger.ErrorContext(r.Context(), "access authentication failed", "code", code, "error", err)
				}
				api.WriteError(w, r, status, code, message, details)
				return
			}
			ctx := context.WithValue(r.Context(), principalKey, &principal)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func PrincipalFromContext(ctx context.Context) *Principal {
	principal, _ := ctx.Value(principalKey).(*Principal)
	return principal
}

func RequireActive(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal := PrincipalFromContext(r.Context())
		if principal == nil {
			api.WriteError(w, r, http.StatusUnauthorized, CodeUnauthorized, "Authentication is required.", nil)
			return
		}
		if principal.Player.AccountStatus != player.AccountStatusActive {
			api.WriteError(w, r, http.StatusForbidden, "ACCOUNT_NOT_ACTIVE", "An active account is required.", nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}
