package metaserver

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

var ErrGateTicketInvalid = errors.New("gate ticket is invalid, expired, or already consumed")

// Keep ticket consumption atomic without requiring Redis 6.2 GETDEL. The
// deployment and local lab both support the Lua primitive on older Redis 6.0.
var consumeGateTicketScript = redis.NewScript(`
local value = redis.call("GET", KEYS[1])
if value then
    redis.call("DEL", KEYS[1])
end
return value
`)

type GateSession struct {
	PlayerID        string    `json:"player_id"`
	AuthSessionID   string    `json:"auth_session_id"`
	ClientVersion   string    `json:"client_version"`
	ProtocolVersion int       `json:"protocol_version"`
	IssuedAt        time.Time `json:"issued_at"`
}

type GateStore struct {
	redis   redis.Cmdable
	ttl     time.Duration
	now     func() time.Time
	metrics *MetaMetrics
}

func (s *GateStore) SetMetrics(metrics *MetaMetrics) { s.metrics = metrics }
func (s *GateStore) TTL() time.Duration              { return s.ttl }

func NewGateStore(client redis.Cmdable, ttl time.Duration) *GateStore {
	return &GateStore{redis: client, ttl: ttl, now: time.Now}
}

func (s *GateStore) Issue(ctx context.Context, session GateSession) (string, error) {
	if session.PlayerID == "" || session.AuthSessionID == "" {
		return "", errors.New("player and authentication session are required")
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate gate ticket: %w", err)
	}
	ticket := "mgt_" + base64.RawURLEncoding.EncodeToString(raw)
	session.IssuedAt = s.now().UTC()
	encoded, err := json.Marshal(session)
	if err != nil {
		return "", fmt.Errorf("encode gate session: %w", err)
	}
	if err := s.redis.Set(ctx, gateKey(ticket), encoded, s.ttl).Err(); err != nil {
		return "", fmt.Errorf("store gate session: %w", err)
	}
	if s.metrics != nil {
		s.metrics.gateIssuedTotal.Add(1)
	}
	return ticket, nil
}

func (s *GateStore) Consume(ctx context.Context, ticket string) (GateSession, error) {
	if !strings.HasPrefix(ticket, "mgt_") || len(ticket) < 40 {
		return GateSession{}, ErrGateTicketInvalid
	}
	value, err := consumeGateTicketScript.Run(ctx, s.redis, []string{gateKey(ticket)}).Text()
	if errors.Is(err, redis.Nil) {
		if s.metrics != nil {
			s.metrics.ticketReplayTotal.Add(1)
		}
		return GateSession{}, ErrGateTicketInvalid
	}
	if err != nil {
		return GateSession{}, fmt.Errorf("consume gate session: %w", err)
	}
	var session GateSession
	if err := json.Unmarshal([]byte(value), &session); err != nil || session.PlayerID == "" || session.AuthSessionID == "" {
		return GateSession{}, ErrGateTicketInvalid
	}
	if s.metrics != nil {
		s.metrics.gateConsumedTotal.Add(1)
	}
	return session, nil
}

func gateKey(ticket string) string {
	sum := sha256.Sum256([]byte(ticket))
	return "meta:gate:" + hex.EncodeToString(sum[:])
}
