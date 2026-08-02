package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/gameserver"
)

func TestHeartbeatTargets(t *testing.T) {
	tests := []struct {
		name              string
		primary, fallback string
		want              []string
	}{
		{name: "primary only", primary: "https://api.example.com/", want: []string{"https://api.example.com"}},
		{name: "fallback", primary: "https://api.example.com", fallback: "https://fallback.example.com/", want: []string{"https://api.example.com", "https://fallback.example.com"}},
		{name: "deduplicate", primary: "https://api.example.com/", fallback: "https://api.example.com", want: []string{"https://api.example.com"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := heartbeatTargets(test.primary, test.fallback); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("heartbeatTargets() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestSaveAndLoadIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity.json")
	want := identity{
		ServerID: "gs_test", InstanceID: "integration-test", ServerToken: "gst_test",
		PrivateKeyBase64: "private-key-test", CredentialGeneration: 3,
	}
	if err := saveIdentity(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := loadIdentity(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.ServerID != want.ServerID || got.InstanceID != want.InstanceID ||
		got.ServerToken != want.ServerToken || got.PrivateKeyBase64 != want.PrivateKeyBase64 ||
		got.CredentialGeneration != want.CredentialGeneration {
		t.Fatalf("loaded identity = %#v, want %#v", got, want)
	}
}

func TestRotationDue(t *testing.T) {
	now := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	current := identity{
		TokenExpiresAt:       now.Add(7 * time.Hour),
		CertificateExpiresAt: now.Add(8 * time.Hour),
	}
	if rotationDue(current, now, 6*time.Hour) {
		t.Fatal("credential rotated before the configured window")
	}
	current.TokenExpiresAt = now.Add(6 * time.Hour)
	if !rotationDue(current, now, 6*time.Hour) {
		t.Fatal("credential was not rotated at the configured boundary")
	}
}

func TestHeartbeatFallsBackWithFreshSignedRequest(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "temporary outage", http.StatusServiceUnavailable)
	}))
	defer primary.Close()

	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		body, readErr := io.ReadAll(request.Body)
		if readErr != nil {
			t.Error(readErr)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var payload struct {
			State       string `json:"state"`
			PlayerCount int    `json:"player_count"`
		}
		if decodeErr := json.Unmarshal(body, &payload); decodeErr != nil || payload.State != "READY" || payload.PlayerCount != 7 {
			t.Errorf("heartbeat payload = %#v, %v", payload, decodeErr)
		}
		timestamp, _ := strconv.ParseInt(request.Header.Get(gameserver.HeaderRequestTimestamp), 10, 64)
		generation, _ := strconv.ParseInt(request.Header.Get(gameserver.HeaderCredentialGeneration), 10, 64)
		proof := gameserver.SignedRequestInput{
			ServerID: "gs_test", ServerToken: strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer "),
			CertificateFingerprint: request.Header.Get(gameserver.HeaderCertificateFingerprint),
			Timestamp:              timestamp, Nonce: request.Header.Get(gameserver.HeaderRequestNonce),
			CredentialGeneration: generation, Signature: request.Header.Get(gameserver.HeaderRequestSignature),
			Method: request.Method, RequestTarget: request.URL.EscapedPath(), Body: body,
		}
		signature, decodeErr := base64.RawURLEncoding.DecodeString(proof.Signature)
		if decodeErr != nil || !ed25519.Verify(publicKey, []byte(gameserver.CanonicalSignedRequest(proof)), signature) {
			t.Errorf("fallback heartbeat signature is invalid: %v", decodeErr)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	defer fallback.Close()

	current := identity{
		ServerID: "gs_test", InstanceID: "test-instance",
		ServerToken:            "gst_" + strings.Repeat("a", 64),
		PrivateKeyBase64:       base64.RawStdEncoding.EncodeToString(privateKey),
		CertificateFingerprint: strings.Repeat("b", 64), CredentialGeneration: 2,
	}
	cfg := options{
		ControlPlaneURL: primary.URL, FallbackURL: fallback.URL,
		HeartbeatState: "ready", PlayerCount: 7,
	}
	if err := heartbeat(context.Background(), &http.Client{Timeout: 5 * time.Second}, cfg, current); err != nil {
		t.Fatal(err)
	}
}
