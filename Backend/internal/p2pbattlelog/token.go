package p2pbattlelog

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"

	"github.com/google/uuid"
)

func newID(prefix string) string { return prefix + uuid.NewString() }

func newOpaque(prefix string, byteCount int) (string, error) {
	value := make([]byte, byteCount)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate secure random value: %w", err)
	}
	return prefix + base64.RawURLEncoding.EncodeToString(value), nil
}

func newReportToken() (string, []byte, error) {
	token, err := newOpaque("p2r_", 32)
	if err != nil {
		return "", nil, err
	}
	digest := sha256.Sum256([]byte(token))
	return token, digest[:], nil
}

func hashReportToken(token string) []byte {
	digest := sha256.Sum256([]byte(token))
	return digest[:]
}
