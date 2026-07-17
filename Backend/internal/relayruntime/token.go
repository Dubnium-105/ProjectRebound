package relayruntime

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"
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

type PublicKey struct {
	KeyID     string `json:"kid"`
	Algorithm string `json:"alg"`
	PublicKey string `json:"public_key"`
}

type Keyset struct {
	Keys []PublicKey `json:"keys"`
}

type TokenVerifier struct {
	mu   sync.RWMutex
	keys map[string]ed25519.PublicKey
}

func NewTokenVerifier(keyset Keyset) (*TokenVerifier, error) {
	verifier := &TokenVerifier{}
	if err := verifier.Update(keyset); err != nil {
		return nil, err
	}
	return verifier, nil
}

func (v *TokenVerifier) Update(keyset Keyset) error {
	if len(keyset.Keys) == 0 {
		return errors.New("relay token keyset is empty")
	}
	keys := make(map[string]ed25519.PublicKey, len(keyset.Keys))
	for _, item := range keyset.Keys {
		if strings.TrimSpace(item.KeyID) == "" || item.Algorithm != "EdDSA" {
			return errors.New("relay keyset contains an unsupported key")
		}
		decoded, err := base64.RawURLEncoding.DecodeString(item.PublicKey)
		if err != nil || len(decoded) != ed25519.PublicKeySize {
			return errors.New("relay keyset contains an invalid Ed25519 public key")
		}
		if _, duplicate := keys[item.KeyID]; duplicate {
			return errors.New("relay keyset contains a duplicate key ID")
		}
		keys[item.KeyID] = ed25519.PublicKey(decoded)
	}
	v.mu.Lock()
	v.keys = keys
	v.mu.Unlock()
	return nil
}

func (v *TokenVerifier) Verify(token, expectedNodeID string, now time.Time) (RelayClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return RelayClaims{}, errors.New("relay token must have three segments")
	}
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return RelayClaims{}, errors.New("invalid relay token header")
	}
	var header struct {
		Algorithm string `json:"alg"`
		Type      string `json:"typ"`
		KeyID     string `json:"kid"`
	}
	if err := json.Unmarshal(headerBytes, &header); err != nil || header.Algorithm != "EdDSA" || header.Type != "relay+jwt" || header.KeyID == "" {
		return RelayClaims{}, errors.New("unsupported relay token header")
	}
	v.mu.RLock()
	publicKey := v.keys[header.KeyID]
	v.mu.RUnlock()
	if len(publicKey) != ed25519.PublicKeySize {
		return RelayClaims{}, errors.New("relay token key is unknown")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !ed25519.Verify(publicKey, []byte(parts[0]+"."+parts[1]), signature) {
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
	role, validRole := parseRole(claims.EndpointRole)
	unixNow := now.UTC().Unix()
	if claims.Issuer != "game-control-plane" || claims.Audience != "game-relay" || claims.KeyID != header.KeyID ||
		claims.TokenID == "" || claims.RelayNodeID != expectedNodeID || claims.AllocationID == "" ||
		claims.ConnectionID == "" || claims.RoomID == "" || !validRole || role == 0 || claims.Protocol != "UDP" ||
		claims.MaxBPS <= 0 || claims.MaxPPS <= 0 || claims.MaxTotalBytes <= 0 || claims.NotBefore > unixNow || claims.ExpiresAt <= unixNow ||
		claims.AllocationExpiresAt < claims.ExpiresAt {
		return RelayClaims{}, errors.New("invalid or expired relay token claims")
	}
	return claims, nil
}
