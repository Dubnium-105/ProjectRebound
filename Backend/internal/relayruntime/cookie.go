package relayruntime

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"net/netip"
	"time"
)

type CookieManager struct {
	secret []byte
	ttl    time.Duration
}

func NewCookieManager(ttl time.Duration) (*CookieManager, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	return &CookieManager{secret: secret, ttl: ttl}, nil
}

func (m *CookieManager) Issue(address netip.AddrPort, token []byte, now time.Time) []byte {
	return m.value(address, token, now.Unix()/int64(m.ttl/time.Second))
}

func (m *CookieManager) Verify(cookie []byte, address netip.AddrPort, token []byte, now time.Time) bool {
	bucket := now.Unix() / int64(m.ttl/time.Second)
	return hmac.Equal(cookie, m.value(address, token, bucket)) || hmac.Equal(cookie, m.value(address, token, bucket-1))
}

func (m *CookieManager) IssueV2(
	address netip.AddrPort,
	clientNonce, serverNonce, token []byte,
	requestedMTU uint16,
	now time.Time,
) []byte {
	return m.valueV2(address, clientNonce, serverNonce, token, requestedMTU, now.Unix()/int64(m.ttl/time.Second))
}

func (m *CookieManager) VerifyV2(
	cookie []byte,
	address netip.AddrPort,
	clientNonce, serverNonce, token []byte,
	requestedMTU uint16,
	now time.Time,
) bool {
	bucket := now.Unix() / int64(m.ttl/time.Second)
	return hmac.Equal(cookie, m.valueV2(address, clientNonce, serverNonce, token, requestedMTU, bucket)) ||
		hmac.Equal(cookie, m.valueV2(address, clientNonce, serverNonce, token, requestedMTU, bucket-1))
}

func (m *CookieManager) TTL() time.Duration { return m.ttl }

func (m *CookieManager) value(address netip.AddrPort, token []byte, bucket int64) []byte {
	mac := hmac.New(sha256.New, m.secret)
	mac.Write([]byte(address.String()))
	tokenHash := sha256.Sum256(token)
	mac.Write(tokenHash[:])
	var encodedBucket [8]byte
	binary.BigEndian.PutUint64(encodedBucket[:], uint64(bucket))
	mac.Write(encodedBucket[:])
	return mac.Sum(nil)
}

func (m *CookieManager) valueV2(
	address netip.AddrPort,
	clientNonce, serverNonce, token []byte,
	requestedMTU uint16,
	bucket int64,
) []byte {
	mac := hmac.New(sha256.New, m.secret)
	mac.Write([]byte("project-rebound-bind-cookie-v2"))
	mac.Write([]byte(address.String()))
	mac.Write(clientNonce)
	mac.Write(serverNonce)
	var encodedMTU [2]byte
	binary.BigEndian.PutUint16(encodedMTU[:], requestedMTU)
	mac.Write(encodedMTU[:])
	tokenHash := sha256.Sum256(token)
	mac.Write(tokenHash[:])
	var encodedBucket [8]byte
	binary.BigEndian.PutUint64(encodedBucket[:], uint64(bucket))
	mac.Write(encodedBucket[:])
	return mac.Sum(nil)
}
