package gameserver

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
)

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
