package integrity

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/auth"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/config"
	"github.com/Dubnium-105/ProjectRebound/Backend/internal/player"
)

const testPublicKey = "-----BEGIN PUBLIC KEY-----\nAQID\n-----END PUBLIC KEY-----\n"

type recordingRecorder struct {
	promotions int
	failures   []recordedFailure
}

type recordedFailure struct {
	attempts int
	reason   string
	terminal bool
}

func (r *recordingRecorder) PromoteIntegrityTrusted(
	context.Context,
	auth.Principal,
	auth.RequestMeta,
) error {
	r.promotions++
	return nil
}

func (r *recordingRecorder) RecordIntegrityFailure(
	_ context.Context,
	_ auth.Principal,
	attempts int,
	_ string,
	reason string,
	terminal bool,
	_ auth.RequestMeta,
) error {
	r.failures = append(r.failures, recordedFailure{
		attempts: attempts, reason: reason, terminal: terminal,
	})
	return nil
}

func newTestService(t *testing.T, recorder Recorder, now time.Time) *Service {
	t.Helper()
	cfg := config.Defaults.Auth
	cfg.IntegrityPublicKeyPEM = testPublicKey
	service, err := NewService(
		cfg,
		recorder,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return now }
	return service
}

func testPrincipal(sessionID string) auth.Principal {
	return auth.Principal{
		Player:    player.Player{ID: "p_test", SteamID: "76561198000000001"},
		SessionID: sessionID, AuthLevel: player.AuthLevelVerified, SteamVerified: true,
	}
}

func TestIntegrityProofPromotesTrustedSession(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	recorder := &recordingRecorder{}
	service := newTestService(t, recorder, now)
	ticket := []byte{0xde, 0xad, 0xbe, 0xef}

	challenge, err := service.RegisterSession("ses_test", ticket, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(challenge.Nonce) != 64 {
		t.Fatalf("nonce length = %d", len(challenge.Nonce))
	}
	proof := expectedProof([]byte(testPublicKey), ticket, challenge.Nonce)
	result, err := service.Verify(
		context.Background(),
		testPrincipal("ses_test"),
		challenge.Nonce,
		proof,
		"toolbox",
		auth.RequestMeta{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK || recorder.promotions != 1 || len(recorder.failures) != 0 {
		t.Fatalf("result=%+v recorder=%+v", result, recorder)
	}
}

func TestIntegrityProofRevokesAfterThreeConsecutiveFailures(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	recorder := &recordingRecorder{}
	service := newTestService(t, recorder, now)
	if _, err := service.RegisterSession(
		"ses_test",
		[]byte{0x01, 0x02},
		now.Add(time.Hour),
	); err != nil {
		t.Fatal(err)
	}

	for attempt := 1; attempt <= 3; attempt++ {
		challenge, err := service.Challenge("ses_test")
		if err != nil {
			t.Fatal(err)
		}
		result, err := service.Verify(
			context.Background(),
			testPrincipal("ses_test"),
			challenge.Nonce,
			"00",
			"toolbox",
			auth.RequestMeta{},
		)
		if err != nil {
			t.Fatal(err)
		}
		if result.Failures != attempt || result.Revoked != (attempt == 3) {
			t.Fatalf("attempt %d result=%+v", attempt, result)
		}
	}
	if len(recorder.failures) != 3 ||
		recorder.failures[2].reason != "proof_mismatch" ||
		!recorder.failures[2].terminal {
		t.Fatalf("failures=%+v", recorder.failures)
	}
	challenge, err := service.Challenge("ses_test")
	if err != nil {
		t.Fatal(err)
	}
	if challenge.Nonce != "" {
		t.Fatalf("revoked session received nonce %q", challenge.Nonce)
	}
}

func TestIntegrityNonceIsOneTimeAndExpires(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	recorder := &recordingRecorder{}
	service := newTestService(t, recorder, now)
	ticket := []byte{0x01}
	challenge, err := service.RegisterSession("ses_test", ticket, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	proof := expectedProof([]byte(testPublicKey), ticket, challenge.Nonce)
	if result, err := service.Verify(
		context.Background(), testPrincipal("ses_test"), challenge.Nonce, proof,
		"toolbox", auth.RequestMeta{},
	); err != nil || !result.OK {
		t.Fatalf("initial result=%+v error=%v", result, err)
	}
	if result, err := service.Verify(
		context.Background(), testPrincipal("ses_test"), challenge.Nonce, proof,
		"toolbox", auth.RequestMeta{},
	); err != nil || result.OK || recorder.failures[0].reason != "challenge_missing" {
		t.Fatalf("replay result=%+v error=%v failures=%+v", result, err, recorder.failures)
	}

	challenge, err = service.Challenge("ses_test")
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time {
		return now.Add(time.Duration(config.Defaults.Auth.IntegrityChallengeTTLSeconds+1) * time.Second)
	}
	proof = expectedProof([]byte(testPublicKey), ticket, challenge.Nonce)
	if result, err := service.Verify(
		context.Background(), testPrincipal("ses_test"), challenge.Nonce, proof,
		"toolbox", auth.RequestMeta{},
	); err != nil || result.OK || recorder.failures[1].reason != "challenge_expired" {
		t.Fatalf("expired result=%+v error=%v failures=%+v", result, err, recorder.failures)
	}
}

func TestIntegritySessionRotatesWithoutRetainingNonce(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	service := newTestService(t, &recordingRecorder{}, now)
	if _, err := service.RegisterSession("ses_old", []byte{0x01}, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	service.RotateSession("ses_old", "ses_new", now.Add(2*time.Hour))
	oldChallenge, _ := service.Challenge("ses_old")
	newChallenge, _ := service.Challenge("ses_new")
	if oldChallenge.Nonce != "" || len(newChallenge.Nonce) != 64 {
		t.Fatalf("old=%+v new=%+v", oldChallenge, newChallenge)
	}
}
