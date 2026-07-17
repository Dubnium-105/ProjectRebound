package p2proom

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"strings"

	"github.com/google/uuid"
)

func newHostToken() (string, []byte, error) {
	value := make([]byte, 48)
	if _, err := rand.Read(value); err != nil {
		return "", nil, err
	}
	token := "p2h_" + base64.RawURLEncoding.EncodeToString(value)
	return token, hashHostToken(token), nil
}

func hashHostToken(token string) []byte {
	hash := sha256.Sum256([]byte(token))
	return hash[:]
}

func newID(prefix string) string {
	return prefix + strings.ReplaceAll(uuid.NewString(), "-", "")
}
