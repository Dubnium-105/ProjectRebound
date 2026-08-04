package vnt

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"

	"github.com/google/uuid"
)

func newID(prefix string) string { return prefix + uuid.NewString() }

func newSecret(prefix string) (string, []byte, error) {
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return "", nil, fmt.Errorf("generate VNT credential: %w", err)
	}
	secret := prefix + base64.RawURLEncoding.EncodeToString(random)
	hash := sha256.Sum256([]byte(secret))
	return secret, hash[:], nil
}

func hashSecret(secret string) []byte {
	hash := sha256.Sum256([]byte(secret))
	return hash[:]
}
