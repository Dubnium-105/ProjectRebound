package matchlobby

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

type admissionHeader struct {
	Algorithm string `json:"alg"`
	Type      string `json:"typ"`
	KeyID     string `json:"kid"`
}

const (
	allocationAudience = "project-rebound-match-authority"
	joinGrantAudience  = "project-rebound-match-client"
)

type AdmissionSigner struct {
	privateKey ed25519.PrivateKey
	publicKey  ed25519.PublicKey
	keyID      string
	now        func() time.Time
	ephemeral  bool
}

func NewAdmissionSigner(keyID, encodedPrivateKey, environment string) (*AdmissionSigner, error) {
	keyID = strings.TrimSpace(keyID)
	if keyID == "" {
		return nil, errors.New("match admission signing key ID is required")
	}
	var privateKey ed25519.PrivateKey
	ephemeral := false
	if strings.TrimSpace(encodedPrivateKey) == "" {
		if strings.EqualFold(environment, "production") {
			return nil, errors.New("match admission private key is required in production")
		}
		_, generated, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, fmt.Errorf("generate match admission key: %w", err)
		}
		privateKey = generated
		ephemeral = true
	} else {
		decoded, err := decodeAdmissionKey(encodedPrivateKey)
		if err != nil {
			return nil, fmt.Errorf("decode match admission key: %w", err)
		}
		switch len(decoded) {
		case ed25519.SeedSize:
			privateKey = ed25519.NewKeyFromSeed(decoded)
		case ed25519.PrivateKeySize:
			privateKey = ed25519.PrivateKey(decoded)
		default:
			return nil, errors.New("match admission key must be a 32-byte seed or 64-byte Ed25519 private key")
		}
	}
	publicKey := privateKey.Public().(ed25519.PublicKey)
	return &AdmissionSigner{privateKey: privateKey, publicKey: publicKey, keyID: keyID, now: time.Now, ephemeral: ephemeral}, nil
}

func (s *AdmissionSigner) SignAllocation(claims AllocationClaims, ttl time.Duration) (string, time.Time, error) {
	now := s.now().UTC()
	expires := now.Add(ttl)
	claims.Issuer = "game-control-plane"
	claims.Audience = allocationAudience
	claims.KeyID = s.keyID
	if claims.TokenID == "" {
		claims.TokenID = newAdmissionID("ma_")
	}
	claims.NotBefore = now.Add(-5 * time.Second).Unix()
	claims.ExpiresAt = expires.Unix()
	token, err := s.sign("match-allocation+jwt", claims)
	return token, expires, err
}

func (s *AdmissionSigner) SignJoinGrant(claims JoinGrantClaims, ttl time.Duration) (string, time.Time, error) {
	now := s.now().UTC()
	expires := now.Add(ttl)
	claims.Issuer = "game-control-plane"
	claims.Audience = joinGrantAudience
	claims.KeyID = s.keyID
	if claims.TokenID == "" {
		claims.TokenID = newAdmissionID("mj_")
	}
	claims.NotBefore = now.Add(-5 * time.Second).Unix()
	claims.ExpiresAt = expires.Unix()
	token, err := s.sign("match-join+jwt", claims)
	return token, expires, err
}

func (s *AdmissionSigner) sign(tokenType string, claims any) (string, error) {
	header, err := json.Marshal(admissionHeader{Algorithm: "EdDSA", Type: tokenType, KeyID: s.keyID})
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(body)
	signature := ed25519.Sign(s.privateKey, []byte(unsigned))
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func (s *AdmissionSigner) PublicKeyBase64() string {
	return base64.RawStdEncoding.EncodeToString(s.publicKey)
}

func (s *AdmissionSigner) KeyID() string   { return s.keyID }
func (s *AdmissionSigner) Ephemeral() bool { return s.ephemeral }

func newAdmissionID(prefix string) string {
	return prefix + strings.ReplaceAll(uuid.NewString(), "-", "")
}

func decodeAdmissionKey(value string) ([]byte, error) {
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(value))
	if err == nil {
		return decoded, nil
	}
	return base64.RawStdEncoding.DecodeString(strings.TrimSpace(value))
}
