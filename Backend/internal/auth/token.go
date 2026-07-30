package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/config"
	"github.com/google/uuid"
)

type AccessClaims struct {
	Issuer        string `json:"iss"`
	Audience      string `json:"aud"`
	Subject       string `json:"sub"`
	UserID        string `json:"user_id"`
	SessionID     string `json:"sid"`
	Provider      string `json:"provider"`
	AuthLevel     string `json:"auth_level"`
	SteamVerified bool   `json:"steam_verified"`
	IssuedAt      int64  `json:"iat"`
	ExpiresAt     int64  `json:"exp"`
	TokenVersion  int    `json:"token_version"`
}

type tokenHeader struct {
	Algorithm string `json:"alg"`
	Type      string `json:"typ"`
	KeyID     string `json:"kid"`
}

type TokenManager struct {
	privateKey ed25519.PrivateKey
	publicKey  ed25519.PublicKey
	issuer     string
	audience   string
	keyID      string
	now        func() time.Time
}

func NewTokenManager(cfg config.AuthConfig, environment string) (*TokenManager, bool, error) {
	var privateKey ed25519.PrivateKey
	ephemeral := false
	if strings.TrimSpace(cfg.AccessTokenPrivateKeyBase64) == "" {
		if strings.EqualFold(environment, "production") {
			return nil, false, errors.New("access token private key is required in production")
		}
		_, generated, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, false, fmt.Errorf("generate development Ed25519 key: %w", err)
		}
		privateKey = generated
		ephemeral = true
	} else {
		decoded, err := decodeKey(cfg.AccessTokenPrivateKeyBase64)
		if err != nil {
			return nil, false, fmt.Errorf("decode access token private key: %w", err)
		}
		switch len(decoded) {
		case ed25519.SeedSize:
			privateKey = ed25519.NewKeyFromSeed(decoded)
		case ed25519.PrivateKeySize:
			privateKey = ed25519.PrivateKey(decoded)
		default:
			return nil, false, fmt.Errorf("access token private key must be %d-byte seed or %d-byte private key", ed25519.SeedSize, ed25519.PrivateKeySize)
		}
	}
	publicKey, ok := privateKey.Public().(ed25519.PublicKey)
	if !ok {
		return nil, false, errors.New("derive Ed25519 public key")
	}
	return &TokenManager{
		privateKey: privateKey,
		publicKey:  publicKey,
		issuer:     cfg.Issuer,
		audience:   cfg.Audience,
		keyID:      cfg.AccessTokenKeyID,
		now:        time.Now,
	}, ephemeral, nil
}

func NewTokenVerifier(cfg config.AuthConfig) (*TokenManager, error) {
	var publicKey ed25519.PublicKey
	if strings.TrimSpace(cfg.AccessTokenPublicKeyBase64) != "" {
		decoded, err := decodeKey(cfg.AccessTokenPublicKeyBase64)
		if err != nil {
			return nil, fmt.Errorf("decode access token public key: %w", err)
		}
		if len(decoded) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("access token public key must be %d bytes", ed25519.PublicKeySize)
		}
		publicKey = ed25519.PublicKey(append([]byte(nil), decoded...))
	} else {
		decoded, err := decodeKey(cfg.AccessTokenPrivateKeyBase64)
		if err != nil {
			return nil, fmt.Errorf("decode development access token key: %w", err)
		}
		var privateKey ed25519.PrivateKey
		switch len(decoded) {
		case ed25519.SeedSize:
			privateKey = ed25519.NewKeyFromSeed(decoded)
		case ed25519.PrivateKeySize:
			privateKey = ed25519.PrivateKey(decoded)
		default:
			return nil, fmt.Errorf("development access token key has invalid length")
		}
		publicKey = append(ed25519.PublicKey(nil), privateKey.Public().(ed25519.PublicKey)...)
	}
	if strings.TrimSpace(cfg.Issuer) == "" ||
		strings.TrimSpace(cfg.Audience) == "" ||
		strings.TrimSpace(cfg.AccessTokenKeyID) == "" {
		return nil, errors.New("access token verifier identity is incomplete")
	}
	return &TokenManager{
		publicKey: publicKey,
		issuer:    cfg.Issuer,
		audience:  cfg.Audience,
		keyID:     cfg.AccessTokenKeyID,
		now:       time.Now,
	}, nil
}

func (m *TokenManager) Sign(playerID, sessionID, provider, authLevel string, tokenVersion int, ttl time.Duration) (string, time.Time, error) {
	if len(m.privateKey) != ed25519.PrivateKeySize {
		return "", time.Time{}, errors.New("token manager is verifier-only")
	}
	now := m.now().UTC()
	expiresAt := now.Add(ttl)
	headerBytes, err := json.Marshal(tokenHeader{Algorithm: "EdDSA", Type: "JWT", KeyID: m.keyID})
	if err != nil {
		return "", time.Time{}, err
	}
	claimsBytes, err := json.Marshal(AccessClaims{
		Issuer:        m.issuer,
		Audience:      m.audience,
		Subject:       playerID,
		UserID:        playerID,
		SessionID:     sessionID,
		Provider:      provider,
		AuthLevel:     authLevel,
		SteamVerified: authLevel == "verified" || authLevel == "trusted",
		IssuedAt:      now.Unix(),
		ExpiresAt:     expiresAt.Unix(),
		TokenVersion:  tokenVersion,
	})
	if err != nil {
		return "", time.Time{}, err
	}
	unsigned := base64.RawURLEncoding.EncodeToString(headerBytes) + "." + base64.RawURLEncoding.EncodeToString(claimsBytes)
	signature := ed25519.Sign(m.privateKey, []byte(unsigned))
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature), expiresAt, nil
}

func (m *TokenManager) Verify(token string) (AccessClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return AccessClaims{}, errors.New("access token must have three segments")
	}
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return AccessClaims{}, errors.New("invalid access token header")
	}
	var header tokenHeader
	if err := json.Unmarshal(headerBytes, &header); err != nil || header.Algorithm != "EdDSA" || header.Type != "JWT" || header.KeyID != m.keyID {
		return AccessClaims{}, errors.New("unsupported access token header")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !ed25519.Verify(m.publicKey, []byte(parts[0]+"."+parts[1]), signature) {
		return AccessClaims{}, errors.New("invalid access token signature")
	}
	claimsBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return AccessClaims{}, errors.New("invalid access token claims")
	}
	var claims AccessClaims
	if err := json.Unmarshal(claimsBytes, &claims); err != nil {
		return AccessClaims{}, errors.New("invalid access token claims")
	}
	now := m.now().UTC()
	if claims.Issuer != m.issuer || claims.Audience != m.audience ||
		claims.Subject == "" || (claims.UserID != "" && claims.UserID != claims.Subject) ||
		claims.SessionID == "" || claims.Provider == "" || claims.AuthLevel == "" ||
		claims.TokenVersion < 1 || claims.IssuedAt < 1 || claims.ExpiresAt <= claims.IssuedAt {
		return AccessClaims{}, errors.New("invalid access token claims")
	}
	if claims.IssuedAt > now.Add(30*time.Second).Unix() || claims.ExpiresAt <= now.Unix() {
		return AccessClaims{}, errors.New("access token is expired or not yet valid")
	}
	return claims, nil
}

func NewID(prefix string) string {
	return prefix + strings.ReplaceAll(uuid.NewString(), "-", "")
}

func NewRefreshToken() (string, []byte, error) {
	randomBytes := make([]byte, 48)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", nil, fmt.Errorf("generate refresh token: %w", err)
	}
	token := "rfr_" + base64.RawURLEncoding.EncodeToString(randomBytes)
	return token, HashRefreshToken(token), nil
}

func HashRefreshToken(token string) []byte {
	hash := sha256.Sum256([]byte(token))
	return hash[:]
}

func decodeKey(value string) ([]byte, error) {
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err == nil {
		return decoded, nil
	}
	return base64.RawStdEncoding.DecodeString(value)
}
