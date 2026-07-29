package gameserver

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/api"
)

type registrationContextKey uint8

const registrationIssuerKey registrationContextKey = iota

type registrationCredential struct {
	issuer string
	hash   [32]byte
}

type RegistrationAuthenticator struct {
	credentials []registrationCredential
}

func NewRegistrationAuthenticator(tokenSet string) (*RegistrationAuthenticator, error) {
	tokenSet = strings.TrimSpace(tokenSet)
	if tokenSet == "" {
		return &RegistrationAuthenticator{}, nil
	}
	entries := strings.Split(tokenSet, ";")
	credentials := make([]registrationCredential, 0, len(entries))
	for _, entry := range entries {
		issuer, token, ok := strings.Cut(entry, "=")
		issuer = strings.TrimSpace(issuer)
		token = strings.TrimSpace(token)
		if !ok || issuer == "" || len(token) < 32 {
			return nil, errors.New("GAME_SERVER_REGISTRATION_TOKENS must contain issuer=token entries with tokens of at least 32 bytes")
		}
		credentials = append(credentials, registrationCredential{issuer: issuer, hash: sha256.Sum256([]byte(token))})
	}
	return &RegistrationAuthenticator{credentials: credentials}, nil
}

func (a *RegistrationAuthenticator) Configured() bool { return len(a.credentials) > 0 }

func (a *RegistrationAuthenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			api.WriteError(w, r, 401, "REGISTRATION_UNAUTHORIZED", "Invalid registration token.", nil)
			return
		}
		supplied := sha256.Sum256([]byte(strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))))
		issuer := ""
		for _, candidate := range a.credentials {
			if subtle.ConstantTimeCompare(supplied[:], candidate.hash[:]) == 1 {
				issuer = candidate.issuer
			}
		}
		if issuer == "" {
			api.WriteError(w, r, 401, "REGISTRATION_UNAUTHORIZED", "Invalid registration token.", nil)
			return
		}
		ctx := context.WithValue(r.Context(), registrationIssuerKey, issuer)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func RegistrationIssuer(ctx context.Context) string {
	issuer, _ := ctx.Value(registrationIssuerKey).(string)
	return issuer
}

func newServerToken() (string, []byte, error) {
	value := make([]byte, 48)
	if _, err := rand.Read(value); err != nil {
		return "", nil, err
	}
	token := "gst_" + base64.RawURLEncoding.EncodeToString(value)
	hash := sha256.Sum256([]byte(token))
	return token, hash[:], nil
}

func hashServerToken(token string) []byte {
	hash := sha256.Sum256([]byte(token))
	return hash[:]
}
