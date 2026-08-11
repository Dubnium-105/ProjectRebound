package integrity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/auth"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/config"
)

const toolboxComponent = "toolbox"

type Recorder interface {
	PromoteIntegrityTrusted(context.Context, auth.Principal, auth.RequestMeta) error
	RecordIntegrityFailure(
		context.Context,
		auth.Principal,
		int,
		string,
		string,
		bool,
		auth.RequestMeta,
	) error
}

type Service struct {
	mu                   sync.Mutex
	publicKey            []byte
	publicKeyFingerprint []byte
	challengeTTL         time.Duration
	maximumFailure       int
	recorder             Recorder
	logger               *slog.Logger
	now                  func() time.Time
	sessions             map[string]*sessionState
}

type sessionState struct {
	ticket         []byte
	expiresAt      time.Time
	nonce          string
	nonceExpiresAt time.Time
	failures       int
}

type VerifyResult struct {
	OK       bool
	Failures int
	Revoked  bool
}

func NewService(cfg config.AuthConfig, recorder Recorder, logger *slog.Logger) (*Service, error) {
	publicKey, err := loadPublicKey(cfg.IntegrityPublicKeyPath, cfg.IntegrityPublicKeyPEM)
	if err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	service := &Service{
		publicKey:      publicKey,
		challengeTTL:   time.Duration(cfg.IntegrityChallengeTTLSeconds) * time.Second,
		maximumFailure: cfg.IntegrityMaximumFailures,
		recorder:       recorder,
		logger:         logger,
		now:            time.Now,
		sessions:       make(map[string]*sessionState),
	}
	if len(publicKey) > 0 {
		fingerprint := sha256.Sum256(publicKey)
		service.publicKeyFingerprint = append([]byte(nil), fingerprint[:]...)
	}
	if len(publicKey) == 0 {
		logger.Warn("integrity challenge is disabled because no ToolBox public certificate is configured")
	}
	return service, nil
}

func (s *Service) PEMFingerprint() []byte {
	return append([]byte(nil), s.publicKeyFingerprint...)
}

func loadPublicKey(path string, inline string) ([]byte, error) {
	path = strings.TrimSpace(path)
	if path != "" && strings.TrimSpace(inline) != "" {
		return nil, errors.New("configure only one of TOOLBOX_PUBKEY_PATH or TOOLBOX_PUBKEY")
	}
	var (
		value []byte
		err   error
	)
	switch {
	case path != "":
		value, err = os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read ToolBox public certificate: %w", err)
		}
	case strings.TrimSpace(inline) != "":
		value = []byte(inline)
	default:
		return nil, nil
	}
	block, rest := pem.Decode(value)
	if block == nil || len(strings.TrimSpace(string(rest))) != 0 {
		return nil, errors.New("ToolBox public certificate must contain exactly one PEM block")
	}
	switch block.Type {
	case "CERTIFICATE", "PUBLIC KEY", "RSA PUBLIC KEY":
	default:
		return nil, fmt.Errorf("unsupported ToolBox PEM block type %q", block.Type)
	}
	return append([]byte(nil), value...), nil
}

func (s *Service) RegisterSession(
	sessionID string,
	ticket []byte,
	expiresAt time.Time,
) (auth.IntegrityChallenge, error) {
	if len(s.publicKey) == 0 || strings.TrimSpace(sessionID) == "" || len(ticket) == 0 {
		return auth.IntegrityChallenge{}, nil
	}
	nonce, err := newNonce()
	if err != nil {
		return auth.IntegrityChallenge{}, err
	}
	now := s.now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneExpiredLocked(now)
	s.sessions[sessionID] = &sessionState{
		ticket:         append([]byte(nil), ticket...),
		expiresAt:      expiresAt.UTC(),
		nonce:          nonce,
		nonceExpiresAt: now.Add(s.challengeTTL),
	}
	return auth.IntegrityChallenge{Nonce: nonce}, nil
}

func (s *Service) RotateSession(oldSessionID, newSessionID string, expiresAt time.Time) {
	if oldSessionID == "" || newSessionID == "" {
		return
	}
	now := s.now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneExpiredLocked(now)
	state, ok := s.sessions[oldSessionID]
	if !ok {
		return
	}
	delete(s.sessions, oldSessionID)
	state.expiresAt = expiresAt.UTC()
	state.nonce = ""
	state.nonceExpiresAt = time.Time{}
	s.sessions[newSessionID] = state
}

func (s *Service) RemoveSession(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if state, ok := s.sessions[sessionID]; ok {
		clear(state.ticket)
		delete(s.sessions, sessionID)
	}
}

func (s *Service) Challenge(principal auth.Principal) (auth.IntegrityChallenge, error) {
	if len(s.publicKey) == 0 {
		return auth.IntegrityChallenge{}, nil
	}
	sessionID := strings.TrimSpace(principal.SessionID)
	if sessionID == "" {
		return auth.IntegrityChallenge{}, nil
	}
	nonce, err := newNonce()
	if err != nil {
		return auth.IntegrityChallenge{}, err
	}
	now := s.now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneExpiredLocked(now)
	state, ok := s.sessions[sessionID]
	if !principal.IntegrityTrusted {
		if !ok || len(state.ticket) == 0 {
			return auth.IntegrityChallenge{}, nil
		}
	} else if !ok {
		state = &sessionState{expiresAt: now.Add(s.challengeTTL)}
		s.sessions[sessionID] = state
	}
	if !state.expiresAt.After(now) {
		state.expiresAt = now.Add(s.challengeTTL)
	}
	state.nonce = nonce
	state.nonceExpiresAt = now.Add(s.challengeTTL)
	return auth.IntegrityChallenge{Nonce: nonce}, nil
}

func (s *Service) Verify(
	ctx context.Context,
	principal auth.Principal,
	nonce string,
	proof string,
	component string,
	meta auth.RequestMeta,
) (VerifyResult, error) {
	nonce = strings.TrimSpace(nonce)
	proof = strings.TrimSpace(proof)
	component = strings.ToLower(strings.TrimSpace(component))
	now := s.now().UTC()

	s.mu.Lock()
	s.pruneExpiredLocked(now)
	state, ok := s.sessions[principal.SessionID]
	if !ok {
		s.mu.Unlock()
		return s.recordFailure(ctx, principal, 1, component, "challenge_missing", meta)
	}
	fingerprintMatches := len(principal.PEMFingerprint) == sha256.Size &&
		len(s.publicKeyFingerprint) == sha256.Size &&
		subtle.ConstantTimeCompare(principal.PEMFingerprint, s.publicKeyFingerprint) == 1
	expected := ""
	if principal.IntegrityTrusted {
		expected = expectedIntegrityProof(s.publicKey, nonce)
	} else if len(state.ticket) > 0 {
		expected = expectedProof(s.publicKey, state.ticket, nonce)
	}
	reason := ""
	switch {
	case component != toolboxComponent:
		reason = "unsupported_component"
	case state.nonce == "":
		reason = "challenge_missing"
	case !state.nonceExpiresAt.After(now):
		reason = "challenge_expired"
	case nonce != state.nonce:
		reason = "nonce_mismatch"
	case !fingerprintMatches:
		reason = "pem_fingerprint_mismatch"
	case !principal.IntegrityTrusted && len(state.ticket) == 0:
		reason = "ticket_missing"
	case !validProof(proof, expected):
		reason = "proof_mismatch"
	}
	state.nonce = ""
	state.nonceExpiresAt = time.Time{}
	if reason == "" {
		state.failures = 0
		if !principal.IntegrityTrusted {
			clear(state.ticket)
			state.ticket = nil
		}
		s.mu.Unlock()
		if s.recorder != nil {
			if err := s.recorder.PromoteIntegrityTrusted(ctx, principal, meta); err != nil {
				return VerifyResult{}, err
			}
		}
		return VerifyResult{OK: true}, nil
	}
	state.failures++
	failures := state.failures
	terminal := failures >= s.maximumFailure
	if principal.IntegrityTrusted {
		clear(state.ticket)
		state.ticket = nil
	}
	s.mu.Unlock()
	return s.recordFailureWithCount(ctx, principal, failures, component, reason, terminal, meta)
}

func (s *Service) recordFailure(
	ctx context.Context,
	principal auth.Principal,
	failures int,
	component string,
	reason string,
	meta auth.RequestMeta,
) (VerifyResult, error) {
	return s.recordFailureWithCount(
		ctx,
		principal,
		failures,
		component,
		reason,
		failures >= s.maximumFailure,
		meta,
	)
}

func (s *Service) recordFailureWithCount(
	ctx context.Context,
	principal auth.Principal,
	failures int,
	component string,
	reason string,
	terminal bool,
	meta auth.RequestMeta,
) (VerifyResult, error) {
	if s.recorder != nil {
		if err := s.recorder.RecordIntegrityFailure(
			ctx,
			principal,
			failures,
			component,
			reason,
			terminal,
			meta,
		); err != nil {
			return VerifyResult{}, err
		}
	}
	if terminal {
		s.RemoveSession(principal.SessionID)
	}
	return VerifyResult{Failures: failures, Revoked: terminal}, nil
}

func expectedProof(publicKey []byte, ticket []byte, nonce string) string {
	hash := sha256.New()
	_, _ = hash.Write(publicKey)
	_, _ = hash.Write(ticket)
	_, _ = hash.Write([]byte(nonce))
	return hex.EncodeToString(hash.Sum(nil))
}

func expectedIntegrityProof(publicKey []byte, nonce string) string {
	hash := sha256.New()
	_, _ = hash.Write(publicKey)
	_, _ = hash.Write([]byte(nonce))
	return hex.EncodeToString(hash.Sum(nil))
}

func validProof(actual string, expected string) bool {
	decoded, err := hex.DecodeString(actual)
	if err != nil || len(decoded) != sha256.Size {
		return false
	}
	expectedBytes, _ := hex.DecodeString(expected)
	return subtle.ConstantTimeCompare(decoded, expectedBytes) == 1
}

func newNonce() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func (s *Service) pruneExpiredLocked(now time.Time) {
	for sessionID, state := range s.sessions {
		if !state.expiresAt.After(now) {
			clear(state.ticket)
			delete(s.sessions, sessionID)
		}
	}
}
