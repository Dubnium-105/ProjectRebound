package relayregistry

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
	"github.com/projectrebound/matchserver/internal/config"
)

type RelayClaims struct {
	Issuer        string `json:"iss"`
	Audience      string `json:"aud"`
	KeyID         string `json:"kid"`
	TokenID       string `json:"jti"`
	RelayNodeID   string `json:"relay_node_id"`
	AllocationID  string `json:"allocation_id"`
	ConnectionID  string `json:"connection_id"`
	RoomID        string `json:"room_id"`
	EndpointRole  string `json:"endpoint_role"`
	Protocol      string `json:"protocol"`
	MaxBPS        int64  `json:"max_bps"`
	MaxPPS        int    `json:"max_pps"`
	MaxTotalBytes int64  `json:"max_total_bytes"`
	NotBefore     int64  `json:"nbf"`
	ExpiresAt     int64  `json:"exp"`
}

type relayTokenHeader struct {
	Algorithm string `json:"alg"`
	Type      string `json:"typ"`
	KeyID     string `json:"kid"`
}

type RelayTokenManager struct {
	privateKey ed25519.PrivateKey
	publicKey  ed25519.PublicKey
	keyID      string
	now        func() time.Time
	ephemeral  bool
}

func NewRelayTokenManager(cfg config.RelayRegistryConfig, environment string) (*RelayTokenManager, error) {
	var privateKey ed25519.PrivateKey
	ephemeral := false
	if strings.TrimSpace(cfg.RelayTokenPrivateKeyBase64) == "" {
		if strings.EqualFold(environment, "production") {
			return nil, errors.New("relay token private key is required in production")
		}
		_, generated, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		privateKey = generated
		ephemeral = true
	} else {
		decoded, err := decodeBase64(cfg.RelayTokenPrivateKeyBase64)
		if err != nil {
			return nil, fmt.Errorf("decode relay token private key: %w", err)
		}
		switch len(decoded) {
		case ed25519.SeedSize:
			privateKey = ed25519.NewKeyFromSeed(decoded)
		case ed25519.PrivateKeySize:
			privateKey = ed25519.PrivateKey(decoded)
		default:
			return nil, errors.New("relay token private key must be a 32-byte seed or 64-byte Ed25519 private key")
		}
	}
	return &RelayTokenManager{
		privateKey: privateKey, publicKey: privateKey.Public().(ed25519.PublicKey),
		keyID: cfg.RelayTokenKeyID, now: time.Now, ephemeral: ephemeral,
	}, nil
}

func (m *RelayTokenManager) Sign(claims RelayClaims, ttl time.Duration) (string, time.Time, error) {
	now := m.now().UTC()
	expiresAt := now.Add(ttl)
	claims.Issuer = "game-control-plane"
	claims.Audience = "game-relay"
	claims.KeyID = m.keyID
	claims.TokenID = "rt_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	claims.NotBefore = now.Add(-5 * time.Second).Unix()
	claims.ExpiresAt = expiresAt.Unix()
	headerBytes, err := json.Marshal(relayTokenHeader{Algorithm: "EdDSA", Type: "relay+jwt", KeyID: m.keyID})
	if err != nil {
		return "", time.Time{}, err
	}
	claimsBytes, err := json.Marshal(claims)
	if err != nil {
		return "", time.Time{}, err
	}
	unsigned := base64.RawURLEncoding.EncodeToString(headerBytes) + "." + base64.RawURLEncoding.EncodeToString(claimsBytes)
	signature := ed25519.Sign(m.privateKey, []byte(unsigned))
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature), expiresAt, nil
}

func (m *RelayTokenManager) Verify(token, expectedNodeID string) (RelayClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return RelayClaims{}, errors.New("relay token must have three segments")
	}
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return RelayClaims{}, errors.New("invalid relay token header")
	}
	var header relayTokenHeader
	if err := json.Unmarshal(headerBytes, &header); err != nil || header.Algorithm != "EdDSA" || header.Type != "relay+jwt" || header.KeyID != m.keyID {
		return RelayClaims{}, errors.New("unsupported relay token header")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !ed25519.Verify(m.publicKey, []byte(parts[0]+"."+parts[1]), signature) {
		return RelayClaims{}, errors.New("invalid relay token signature")
	}
	claimsBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return RelayClaims{}, errors.New("invalid relay token claims")
	}
	var claims RelayClaims
	if err := json.Unmarshal(claimsBytes, &claims); err != nil {
		return RelayClaims{}, errors.New("invalid relay token claims")
	}
	now := m.now().UTC().Unix()
	if claims.Issuer != "game-control-plane" || claims.Audience != "game-relay" || claims.KeyID != m.keyID ||
		claims.TokenID == "" || claims.RelayNodeID != expectedNodeID || claims.AllocationID == "" || claims.ConnectionID == "" || claims.RoomID == "" ||
		(claims.EndpointRole != "HOST" && claims.EndpointRole != "PEER") ||
		(claims.Protocol != "UDP" && claims.Protocol != "TCP_TLS") ||
		claims.MaxBPS <= 0 || claims.MaxPPS <= 0 || claims.MaxTotalBytes <= 0 || claims.NotBefore > now || claims.ExpiresAt <= now {
		return RelayClaims{}, errors.New("invalid or expired relay token claims")
	}
	return claims, nil
}

func (m *RelayTokenManager) Keyset() Keyset {
	return Keyset{Keys: []PublicKey{{
		KeyID: m.keyID, Algorithm: "EdDSA",
		PublicKey: base64.RawURLEncoding.EncodeToString(m.publicKey),
	}}}
}

func (m *RelayTokenManager) Ephemeral() bool { return m.ephemeral }
