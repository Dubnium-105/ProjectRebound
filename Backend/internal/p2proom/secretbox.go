package p2proom

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

type SecretBox struct {
	active cipher.AEAD
	keyID  string
	keys   map[string]cipher.AEAD
}

var secretKeyIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func NewSecretBox(encodedKey, environment string) (*SecretBox, bool, error) {
	return NewSecretBoxKeyring(encodedKey, "vnt-room-v1", "", environment)
}

// NewSecretBoxKeyring uses one active write key and an optional semicolon-
// separated kid=base64 read keyring so persisted rooms survive key rotation.
func NewSecretBoxKeyring(encodedKey, activeKeyID, encodedReadKeys, environment string) (*SecretBox, bool, error) {
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
	activeKeyID = strings.TrimSpace(activeKeyID)
	if activeKeyID == "" {
		activeKeyID = "vnt-room-v1"
	}
	if !secretKeyIDPattern.MatchString(activeKeyID) {
		return nil, false, errors.New("VNT secret encryption key ID is invalid")
	}
	active, err := newSecretAEAD(key)
	if err != nil {
		return nil, false, err
	}
	keys := map[string]cipher.AEAD{activeKeyID: active}
	for _, entry := range strings.Split(encodedReadKeys, ";") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		keyID, encoded, ok := strings.Cut(entry, "=")
		keyID = strings.TrimSpace(keyID)
		encoded = strings.TrimSpace(encoded)
		if !ok || !secretKeyIDPattern.MatchString(keyID) || encoded == "" {
			return nil, false, errors.New("VNT secret decryption keyring entry is invalid")
		}
		if _, exists := keys[keyID]; exists {
			return nil, false, fmt.Errorf("duplicate VNT secret key ID %q", keyID)
		}
		readKey, decodeErr := base64.StdEncoding.DecodeString(encoded)
		if decodeErr != nil || len(readKey) != 32 {
			return nil, false, fmt.Errorf("VNT secret decryption key %q must be 32 bytes of base64", keyID)
		}
		readAEAD, aeadErr := newSecretAEAD(readKey)
		if aeadErr != nil {
			return nil, false, aeadErr
		}
		keys[keyID] = readAEAD
	}
	return &SecretBox{active: active, keyID: activeKeyID, keys: keys}, ephemeral, nil
}

func (b *SecretBox) Seal(plaintext, associatedData []byte) ([]byte, []byte, string, error) {
	nonce := make([]byte, b.active.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, "", fmt.Errorf("generate VNT secret nonce: %w", err)
	}
	return b.active.Seal(nil, nonce, plaintext, associatedData), nonce, b.keyID, nil
}

func (b *SecretBox) Open(ciphertext, nonce, associatedData []byte, keyID string) ([]byte, error) {
	keyID = strings.TrimSpace(keyID)
	if keyID == "" {
		keyID = b.keyID
	}
	aead, exists := b.keys[keyID]
	if !exists {
		return nil, fmt.Errorf("decrypt VNT room secret: unknown key ID %q", keyID)
	}
	plaintext, err := aead.Open(nil, nonce, ciphertext, associatedData)
	if err != nil {
		return nil, errors.New("decrypt VNT room secret")
	}
	return plaintext, nil
}

func newSecretAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
