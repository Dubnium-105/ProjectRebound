package relayregistry

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/config"
	"github.com/google/uuid"
)

type RelayClaims struct {
	Issuer              string `json:"iss"`
	Audience            string `json:"aud"`
	KeyID               string `json:"kid"`
	TokenID             string `json:"jti"`
	RelayNodeID         string `json:"relay_node_id"`
	AllocationID        string `json:"allocation_id"`
	ConnectionID        string `json:"connection_id"`
	RoomID              string `json:"room_id"`
	EndpointRole        string `json:"endpoint_role"`
	Protocol            string `json:"protocol"`
	MaxBPS              int64  `json:"max_bps"`
	MaxPPS              int    `json:"max_pps"`
	MaxTotalBytes       int64  `json:"max_total_bytes"`
	NotBefore           int64  `json:"nbf"`
	ExpiresAt           int64  `json:"exp"`
	AllocationExpiresAt int64  `json:"allocation_expires_at"`
}

type relayTokenHeader struct {
	Algorithm string `json:"alg"`
	Type      string `json:"typ"`
	KeyID     string `json:"kid"`
}

type RelayTokenManager struct {
	mu          sync.RWMutex
	privateKeys map[string]ed25519.PrivateKey
	activeKeyID string
	version     int64
	generatedAt time.Time
	now         func() time.Time
	ephemeral   bool
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
		var err error
		privateKey, err = parseRelayPrivateKey(cfg.RelayTokenPrivateKeyBase64)
		if err != nil {
			return nil, fmt.Errorf("decode relay token private key: %w", err)
		}
	}
	keys := map[string]ed25519.PrivateKey{cfg.RelayTokenKeyID: privateKey}
	for _, entry := range strings.Split(cfg.RelayTokenRotationKeys, ";") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		keyID, encoded, ok := strings.Cut(entry, "=")
		keyID = strings.TrimSpace(keyID)
		if !ok || keyID == "" || encoded == "" || keys[keyID] != nil {
			return nil, errors.New("RELAY_TOKEN_ROTATION_KEYS contains an invalid or duplicate entry")
		}
		key, err := parseRelayPrivateKey(encoded)
		if err != nil {
			return nil, fmt.Errorf("decode relay rotation key %s: %w", keyID, err)
		}
		keys[keyID] = key
	}
	now := time.Now().UTC()
	return &RelayTokenManager{privateKeys: keys, activeKeyID: cfg.RelayTokenKeyID, version: 1, generatedAt: now, now: time.Now, ephemeral: ephemeral}, nil
}

func (m *RelayTokenManager) Sign(claims RelayClaims, ttl time.Duration) (string, time.Time, error) {
	now := m.now().UTC()
	m.mu.RLock()
	keyID := m.activeKeyID
	privateKey := m.privateKeys[keyID]
	m.mu.RUnlock()
	expiresAt := now.Add(ttl)
	claims.Issuer = "game-control-plane"
	claims.Audience = "game-relay"
	claims.KeyID = keyID
	claims.TokenID = "rt_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	claims.NotBefore = now.Add(-5 * time.Second).Unix()
	claims.ExpiresAt = expiresAt.Unix()
	headerBytes, err := json.Marshal(relayTokenHeader{Algorithm: "EdDSA", Type: "relay+jwt", KeyID: keyID})
	if err != nil {
		return "", time.Time{}, err
	}
	claimsBytes, err := json.Marshal(claims)
	if err != nil {
		return "", time.Time{}, err
	}
	unsigned := base64.RawURLEncoding.EncodeToString(headerBytes) + "." + base64.RawURLEncoding.EncodeToString(claimsBytes)
	signature := ed25519.Sign(privateKey, []byte(unsigned))
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
	if err := json.Unmarshal(headerBytes, &header); err != nil || header.Algorithm != "EdDSA" || header.Type != "relay+jwt" || header.KeyID == "" {
		return RelayClaims{}, errors.New("unsupported relay token header")
	}
	m.mu.RLock()
	privateKey := m.privateKeys[header.KeyID]
	m.mu.RUnlock()
	if len(privateKey) != ed25519.PrivateKeySize {
		return RelayClaims{}, errors.New("relay token key is unknown")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !ed25519.Verify(privateKey.Public().(ed25519.PublicKey), []byte(parts[0]+"."+parts[1]), signature) {
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
	if claims.Issuer != "game-control-plane" || claims.Audience != "game-relay" || claims.KeyID != header.KeyID ||
		claims.TokenID == "" || claims.RelayNodeID != expectedNodeID || claims.AllocationID == "" || claims.ConnectionID == "" || claims.RoomID == "" ||
		(claims.EndpointRole != "HOST" && claims.EndpointRole != "PEER") ||
		(claims.Protocol != "UDP" && claims.Protocol != "TCP_TLS") ||
		claims.MaxBPS <= 0 || claims.MaxPPS <= 0 || claims.MaxTotalBytes <= 0 || claims.NotBefore > now || claims.ExpiresAt <= now ||
		claims.AllocationExpiresAt < claims.ExpiresAt {
		return RelayClaims{}, errors.New("invalid or expired relay token claims")
	}
	return claims, nil
}

func (m *RelayTokenManager) Keyset() Keyset {
	m.mu.RLock()
	defer m.mu.RUnlock()
	keyIDs := make([]string, 0, len(m.privateKeys))
	for keyID := range m.privateKeys {
		keyIDs = append(keyIDs, keyID)
	}
	sort.Strings(keyIDs)
	keyset := Keyset{Version: m.version, GeneratedAt: m.generatedAt, SignedBy: m.activeKeyID, Keys: make([]PublicKey, 0, len(keyIDs))}
	for _, keyID := range keyIDs {
		keyset.Keys = append(keyset.Keys, PublicKey{KeyID: keyID, Algorithm: "EdDSA", PublicKey: base64.RawURLEncoding.EncodeToString(m.privateKeys[keyID].Public().(ed25519.PublicKey))})
	}
	body, _ := keysetSigningBytes(keyset)
	keyset.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(m.privateKeys[m.activeKeyID], body))
	return keyset
}

func (m *RelayTokenManager) Activate(keyID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.privateKeys[keyID]) != ed25519.PrivateKeySize {
		return errors.New("relay signing key is not staged")
	}
	if keyID == m.activeKeyID {
		return nil
	}
	m.activeKeyID = keyID
	m.version++
	m.generatedAt = m.now().UTC()
	return nil
}

func keysetSigningBytes(keyset Keyset) ([]byte, error) {
	keyset.Signature = ""
	return json.Marshal(keyset)
}

func parseRelayPrivateKey(value string) (ed25519.PrivateKey, error) {
	decoded, err := decodeBase64(strings.TrimSpace(value))
	if err != nil {
		return nil, err
	}
	switch len(decoded) {
	case ed25519.SeedSize:
		return ed25519.NewKeyFromSeed(decoded), nil
	case ed25519.PrivateKeySize:
		return ed25519.PrivateKey(decoded), nil
	default:
		return nil, errors.New("relay token private key must be a 32-byte seed or 64-byte Ed25519 private key")
	}
}

func (m *RelayTokenManager) Ephemeral() bool { return m.ephemeral }
