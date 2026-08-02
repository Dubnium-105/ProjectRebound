package gameserverregistration

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"
)

const tokenPrefix = "gsr_"

func GenerateToken() (string, []byte, error) {
	random := make([]byte, 48)
	if _, err := rand.Read(random); err != nil {
		return "", nil, fmt.Errorf("generate game server registration token: %w", err)
	}
	plaintext := tokenPrefix + base64.RawURLEncoding.EncodeToString(random)
	return plaintext, HashToken(plaintext), nil
}

func HashToken(plaintext string) []byte {
	hash := sha256.Sum256([]byte(strings.TrimSpace(plaintext)))
	return hash[:]
}

func HasValidShape(plaintext string) bool {
	plaintext = strings.TrimSpace(plaintext)
	return strings.HasPrefix(plaintext, tokenPrefix) && len(plaintext) == len(tokenPrefix)+64
}
