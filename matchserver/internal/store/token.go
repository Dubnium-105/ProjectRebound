package store

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
)

func NewToken(length ...int) string {
	n := 32
	if len(length) > 0 {
		n = length[0]
	}
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func SHA256Hash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func FixedTimeEquals(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
