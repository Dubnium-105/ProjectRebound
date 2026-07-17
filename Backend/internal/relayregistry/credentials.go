package relayregistry

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
)

type BootstrapCredential struct {
	ID   string
	Hash []byte
}

func ParseBootstrapCredentials(tokenSet string) ([]BootstrapCredential, error) {
	tokenSet = strings.TrimSpace(tokenSet)
	if tokenSet == "" {
		return nil, nil
	}
	entries := strings.Split(tokenSet, ";")
	credentials := make([]BootstrapCredential, 0, len(entries))
	seen := make(map[string]bool)
	for _, entry := range entries {
		id, token, ok := strings.Cut(entry, "=")
		id = strings.TrimSpace(id)
		token = strings.TrimSpace(token)
		if !ok || id == "" || len(token) < 32 || seen[id] {
			return nil, errors.New("RELAY_BOOTSTRAP_TOKENS must contain unique id=token entries with tokens of at least 32 bytes")
		}
		hash := sha256.Sum256([]byte(token))
		credentials = append(credentials, BootstrapCredential{ID: id, Hash: hash[:]})
		seen[id] = true
	}
	return credentials, nil
}

func hashToken(token string) []byte {
	hash := sha256.Sum256([]byte(token))
	return hash[:]
}

func newOpaqueToken(prefix string) (string, []byte, error) {
	value := make([]byte, 48)
	if _, err := rand.Read(value); err != nil {
		return "", nil, err
	}
	token := prefix + base64.RawURLEncoding.EncodeToString(value)
	return token, hashToken(token), nil
}
