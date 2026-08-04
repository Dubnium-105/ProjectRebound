package p2proom

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
)

type SecretBox struct {
	aead  cipher.AEAD
	keyID string
}

func NewSecretBox(encodedKey, environment string) (*SecretBox, bool, error) {
	var key []byte
	var err error
	ephemeral := strings.TrimSpace(encodedKey) == ""
	if ephemeral {
		if strings.EqualFold(environment, "production") {
			return nil, false, errors.New("VNT_SECRET_ENCRYPTION_KEY_BASE64 is required in production")
		}
		key = make([]byte, 32)
		_, err = rand.Read(key)
	} else {
		key, err = base64.StdEncoding.DecodeString(strings.TrimSpace(encodedKey))
	}
	if err != nil || len(key) != 32 {
		return nil, false, errors.New("VNT secret encryption key must be 32 bytes of base64")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, false, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, false, err
	}
	return &SecretBox{aead: aead, keyID: "vnt-room-v1"}, ephemeral, nil
}

func (b *SecretBox) Seal(plaintext, associatedData []byte) ([]byte, []byte, string, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, "", fmt.Errorf("generate VNT secret nonce: %w", err)
	}
	return b.aead.Seal(nil, nonce, plaintext, associatedData), nonce, b.keyID, nil
}

func (b *SecretBox) Open(ciphertext, nonce, associatedData []byte) ([]byte, error) {
	plaintext, err := b.aead.Open(nil, nonce, ciphertext, associatedData)
	if err != nil {
		return nil, errors.New("decrypt VNT room secret")
	}
	return plaintext, nil
}
