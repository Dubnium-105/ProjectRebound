package admin

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	totpPeriodSeconds = int64(30)
	totpDigits        = 6
)

type SecretBox struct {
	aead cipher.AEAD
}

func NewSecretBox(encodedKey string, environment string) (*SecretBox, bool, error) {
	var key []byte
	ephemeral := false
	if strings.TrimSpace(encodedKey) == "" {
		if strings.EqualFold(environment, "production") {
			return nil, false, errors.New("administrator MFA encryption key is required in production")
		}
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, false, fmt.Errorf("generate development MFA encryption key: %w", err)
		}
		ephemeral = true
	} else {
		decoded, err := base64.StdEncoding.DecodeString(encodedKey)
		if err != nil {
			decoded, err = base64.RawStdEncoding.DecodeString(encodedKey)
		}
		if err != nil || len(decoded) != 32 {
			return nil, false, errors.New("administrator MFA encryption key must be a base64-encoded 32-byte key")
		}
		key = decoded
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, false, fmt.Errorf("initialize administrator MFA cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, false, fmt.Errorf("initialize administrator MFA GCM: %w", err)
	}
	return &SecretBox{aead: aead}, ephemeral, nil
}

func (b *SecretBox) Encrypt(adminID, secret string) ([]byte, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate MFA nonce: %w", err)
	}
	return b.aead.Seal(nonce, nonce, []byte(secret), []byte(adminID)), nil
}

func (b *SecretBox) Decrypt(adminID string, ciphertext []byte) (string, error) {
	if len(ciphertext) < b.aead.NonceSize() {
		return "", errors.New("administrator MFA ciphertext is truncated")
	}
	nonce := ciphertext[:b.aead.NonceSize()]
	plaintext, err := b.aead.Open(nil, nonce, ciphertext[b.aead.NonceSize():], []byte(adminID))
	if err != nil {
		return "", errors.New("administrator MFA credential cannot be decrypted")
	}
	return string(plaintext), nil
}

func NewTOTPSecret() (string, error) {
	raw := make([]byte, 20)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate TOTP secret: %w", err)
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw), nil
}

func TOTPProvisioningURI(issuer, username, secret string) string {
	label := strings.TrimSpace(issuer) + ":" + normalizeUsername(username)
	values := url.Values{}
	values.Set("secret", secret)
	values.Set("issuer", strings.TrimSpace(issuer))
	values.Set("algorithm", "SHA1")
	values.Set("digits", strconv.Itoa(totpDigits))
	values.Set("period", strconv.FormatInt(totpPeriodSeconds, 10))
	return "otpauth://totp/" + url.PathEscape(label) + "?" + values.Encode()
}

func ValidateTOTP(secret, code string, now time.Time) bool {
	normalizedCode := strings.TrimSpace(code)
	if len(normalizedCode) != totpDigits {
		return false
	}
	for _, character := range normalizedCode {
		if character < '0' || character > '9' {
			return false
		}
	}
	counter := now.UTC().Unix() / totpPeriodSeconds
	for offset := int64(-1); offset <= 1; offset++ {
		if hmac.Equal([]byte(totpCode(secret, counter+offset)), []byte(normalizedCode)) {
			return true
		}
	}
	return false
}

func totpCode(secret string, counter int64) string {
	decoded, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil || len(decoded) == 0 {
		return ""
	}
	message := make([]byte, 8)
	binary.BigEndian.PutUint64(message, uint64(counter))
	mac := hmac.New(sha1.New, decoded)
	_, _ = mac.Write(message)
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	value := (uint32(sum[offset])&0x7f)<<24 |
		uint32(sum[offset+1])<<16 |
		uint32(sum[offset+2])<<8 |
		uint32(sum[offset+3])
	return fmt.Sprintf("%06d", value%1_000_000)
}

func HashRecoveryCode(code string) []byte {
	hash := sha256.Sum256([]byte(strings.ToUpper(strings.TrimSpace(code))))
	return hash[:]
}

func NewRecoveryCodes(count int) ([]string, [][]byte, error) {
	if count < 1 || count > 20 {
		return nil, nil, errors.New("recovery code count must be between 1 and 20")
	}
	codes := make([]string, 0, count)
	hashes := make([][]byte, 0, count)
	for range count {
		raw := make([]byte, 10)
		if _, err := rand.Read(raw); err != nil {
			return nil, nil, fmt.Errorf("generate recovery code: %w", err)
		}
		encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw)
		code := encoded[:4] + "-" + encoded[4:8] + "-" + encoded[8:12] + "-" + encoded[12:]
		codes = append(codes, code)
		hashes = append(hashes, HashRecoveryCode(code))
	}
	return codes, hashes, nil
}
