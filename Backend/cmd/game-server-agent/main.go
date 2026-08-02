package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/gameserver"
)

type options struct {
	ControlPlaneURL string
	IdentityFile    string
	InstanceID      string
	DisplayName     string
	Region          string
	Mode            string
	Version         string
	PublicHost      string
	PublicPort      int
	MaxPlayers      int
	RotateBefore    time.Duration
	Once            bool
}

type identity struct {
	ServerID               string    `json:"server_id"`
	InstanceID             string    `json:"instance_id"`
	ServerToken            string    `json:"server_token"`
	PrivateKeyBase64       string    `json:"private_key_base64"`
	CertificatePEM         string    `json:"certificate_pem"`
	CACertificatePEM       string    `json:"ca_certificate_pem"`
	CertificateFingerprint string    `json:"certificate_fingerprint"`
	CertificateExpiresAt   time.Time `json:"certificate_expires_at"`
	TokenExpiresAt         time.Time `json:"token_expires_at"`
	CredentialGeneration   int64     `json:"credential_generation"`
	HeartbeatInterval      int       `json:"heartbeat_interval_seconds"`
	UpdatedAt              time.Time `json:"updated_at"`
}

type envelope[T any] struct {
	Data T `json:"data"`
}

type registrationData struct {
	ServerID               string    `json:"server_id"`
	ServerToken            string    `json:"server_token"`
	HeartbeatInterval      int       `json:"heartbeat_interval_seconds"`
	TokenExpiresAt         time.Time `json:"token_expires_at"`
	CredentialGeneration   int64     `json:"credential_generation"`
	CertificatePEM         string    `json:"certificate_pem"`
	CACertificatePEM       string    `json:"ca_certificate_pem"`
	CertificateFingerprint string    `json:"certificate_fingerprint"`
	CertificateExpiresAt   time.Time `json:"certificate_expires_at"`
}

type rotationData struct {
	ServerID               string    `json:"server_id"`
	ServerToken            string    `json:"server_token"`
	TokenExpiresAt         time.Time `json:"token_expires_at"`
	CredentialGeneration   int64     `json:"credential_generation"`
	CertificatePEM         string    `json:"certificate_pem"`
	CACertificatePEM       string    `json:"ca_certificate_pem"`
	CertificateFingerprint string    `json:"certificate_fingerprint"`
	CertificateExpiresAt   time.Time `json:"certificate_expires_at"`
}

func main() {
	var cfg options
	flag.StringVar(&cfg.ControlPlaneURL, "control-plane-url", "http://127.0.0.1:8080", "Control Plane base URL")
	flag.StringVar(&cfg.IdentityFile, "identity-file", "game-server-identity.json", "mode-0600 node identity file")
	flag.StringVar(&cfg.InstanceID, "instance-id", "", "stable node instance ID")
	flag.StringVar(&cfg.DisplayName, "display-name", "Dedicated Server", "public display name")
	flag.StringVar(&cfg.Region, "region", "asia-hk", "server region")
	flag.StringVar(&cfg.Mode, "mode", "tdm", "game mode")
	flag.StringVar(&cfg.Version, "version", "1.0.0", "game version")
	flag.StringVar(&cfg.PublicHost, "public-host", "", "public unicast IP")
	flag.IntVar(&cfg.PublicPort, "public-port", 7777, "public game port")
	flag.IntVar(&cfg.MaxPlayers, "max-players", 16, "maximum players")
	flag.DurationVar(&cfg.RotateBefore, "rotate-before", 6*time.Hour, "rotate when token or certificate has less time remaining")
	flag.BoolVar(&cfg.Once, "once", false, "send one heartbeat and exit")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	client := &http.Client{Timeout: 15 * time.Second}
	current, err := loadIdentity(cfg.IdentityFile)
	if errors.Is(err, os.ErrNotExist) {
		current, err = enroll(ctx, client, cfg)
		if err == nil {
			err = saveIdentity(cfg.IdentityFile, current)
		}
	}
	if err != nil {
		logger.Error("initialize game server identity", "error", err)
		os.Exit(1)
	}
	for {
		if rotationDue(current, time.Now().UTC(), cfg.RotateBefore) {
			rotated, rotateErr := rotate(ctx, client, cfg.ControlPlaneURL, current)
			if rotateErr != nil {
				logger.Error("rotate game server identity", "error", rotateErr)
			} else if saveErr := saveIdentity(cfg.IdentityFile, rotated); saveErr != nil {
				logger.Error("persist rotated game server identity", "error", saveErr)
			} else {
				current = rotated
				logger.Info("game server identity rotated", "server_id", current.ServerID, "generation", current.CredentialGeneration)
			}
		}
		if err := heartbeat(ctx, client, cfg.ControlPlaneURL, current); err != nil {
			logger.Error("send game server heartbeat", "error", err)
		} else {
			logger.Info("game server heartbeat accepted", "server_id", current.ServerID)
		}
		if cfg.Once {
			return
		}
		interval := time.Duration(current.HeartbeatInterval) * time.Second
		if interval <= 0 {
			interval = 15 * time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

func enroll(ctx context.Context, client *http.Client, cfg options) (identity, error) {
	registrationToken := strings.TrimSpace(os.Getenv("GAME_SERVER_REGISTRATION_TOKEN"))
	if registrationToken == "" {
		return identity{}, errors.New("identity is absent and GAME_SERVER_REGISTRATION_TOKEN is not set")
	}
	if cfg.InstanceID == "" || cfg.PublicHost == "" {
		return identity{}, errors.New("instance-id and public-host are required for initial enrollment")
	}
	privateKey, csrPEM, err := gameserver.NewNodeIdentity(cfg.InstanceID)
	if err != nil {
		return identity{}, err
	}
	body, err := json.Marshal(map[string]any{
		"instance_id": cfg.InstanceID, "display_name": cfg.DisplayName,
		"region": cfg.Region, "mode": cfg.Mode, "version": cfg.Version,
		"public_host": cfg.PublicHost, "public_port": cfg.PublicPort,
		"max_players": cfg.MaxPlayers, "csr_pem": csrPEM,
	})
	if err != nil {
		return identity{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint(cfg.ControlPlaneURL, "/v1/game-servers"), bytes.NewReader(body))
	if err != nil {
		return identity{}, err
	}
	request.Header.Set("Authorization", "Bearer "+registrationToken)
	request.Header.Set("Content-Type", "application/json")
	var response envelope[registrationData]
	if err := doJSON(client, request, &response); err != nil {
		return identity{}, err
	}
	return identityFromRegistration(cfg.InstanceID, privateKey, response.Data), nil
}

func rotate(ctx context.Context, client *http.Client, baseURL string, current identity) (identity, error) {
	privateKey, csrPEM, err := gameserver.NewNodeIdentity(current.InstanceID)
	if err != nil {
		return identity{}, err
	}
	body, err := json.Marshal(map[string]string{"csr_pem": csrPEM})
	if err != nil {
		return identity{}, err
	}
	path := "/v1/game-servers/" + url.PathEscape(current.ServerID) + "/credential/rotate"
	request, err := signedRequest(ctx, http.MethodPost, baseURL, path, body, current)
	if err != nil {
		return identity{}, err
	}
	var response envelope[rotationData]
	if err := doJSON(client, request, &response); err != nil {
		return identity{}, err
	}
	return identity{
		ServerID: response.Data.ServerID, InstanceID: current.InstanceID,
		ServerToken:      response.Data.ServerToken,
		PrivateKeyBase64: base64.RawStdEncoding.EncodeToString(privateKey),
		CertificatePEM:   response.Data.CertificatePEM, CACertificatePEM: response.Data.CACertificatePEM,
		CertificateFingerprint: response.Data.CertificateFingerprint,
		CertificateExpiresAt:   response.Data.CertificateExpiresAt,
		TokenExpiresAt:         response.Data.TokenExpiresAt,
		CredentialGeneration:   response.Data.CredentialGeneration,
		HeartbeatInterval:      current.HeartbeatInterval, UpdatedAt: time.Now().UTC(),
	}, nil
}

func heartbeat(ctx context.Context, client *http.Client, baseURL string, current identity) error {
	body := []byte(`{"state":"READY","player_count":0}`)
	path := "/v1/game-servers/" + url.PathEscape(current.ServerID) + "/heartbeat"
	request, err := signedRequest(ctx, http.MethodPost, baseURL, path, body, current)
	if err != nil {
		return err
	}
	return doJSON(client, request, nil)
}

func signedRequest(ctx context.Context, method, baseURL, path string, body []byte, current identity) (*http.Request, error) {
	privateKey, err := base64.RawStdEncoding.DecodeString(current.PrivateKeyBase64)
	if err != nil || len(privateKey) != ed25519.PrivateKeySize {
		return nil, errors.New("stored node private key is invalid")
	}
	nonce := make([]byte, 24)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	proof := gameserver.SignedRequestInput{
		ServerID: current.ServerID, ServerToken: current.ServerToken,
		CertificateFingerprint: current.CertificateFingerprint,
		Timestamp:              time.Now().UTC().Unix(), Nonce: base64.RawURLEncoding.EncodeToString(nonce),
		CredentialGeneration: current.CredentialGeneration,
		Method:               method, RequestTarget: path, Body: body,
	}
	proof.Signature, err = gameserver.SignRequest(proof, ed25519.PrivateKey(privateKey))
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint(baseURL, path), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+current.ServerToken)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(gameserver.HeaderGameServerID, current.ServerID)
	request.Header.Set(gameserver.HeaderCertificateFingerprint, current.CertificateFingerprint)
	request.Header.Set(gameserver.HeaderRequestTimestamp, strconv.FormatInt(proof.Timestamp, 10))
	request.Header.Set(gameserver.HeaderRequestNonce, proof.Nonce)
	request.Header.Set(gameserver.HeaderCredentialGeneration, strconv.FormatInt(proof.CredentialGeneration, 10))
	request.Header.Set(gameserver.HeaderRequestSignature, proof.Signature)
	return request, nil
}

func doJSON(client *http.Client, request *http.Request, output any) error {
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("request failed with HTTP %d", response.StatusCode)
	}
	if output != nil && len(body) > 0 {
		if err := json.Unmarshal(body, output); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}

func identityFromRegistration(instanceID string, privateKey ed25519.PrivateKey, data registrationData) identity {
	return identity{
		ServerID: data.ServerID, InstanceID: instanceID, ServerToken: data.ServerToken,
		PrivateKeyBase64: base64.RawStdEncoding.EncodeToString(privateKey),
		CertificatePEM:   data.CertificatePEM, CACertificatePEM: data.CACertificatePEM,
		CertificateFingerprint: data.CertificateFingerprint,
		CertificateExpiresAt:   data.CertificateExpiresAt, TokenExpiresAt: data.TokenExpiresAt,
		CredentialGeneration: data.CredentialGeneration,
		HeartbeatInterval:    data.HeartbeatInterval, UpdatedAt: time.Now().UTC(),
	}
}

func rotationDue(current identity, now time.Time, before time.Duration) bool {
	return !current.TokenExpiresAt.After(now.Add(before)) || !current.CertificateExpiresAt.After(now.Add(before))
}

func loadIdentity(path string) (identity, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return identity{}, err
	}
	var current identity
	if err := json.Unmarshal(data, &current); err != nil {
		return identity{}, fmt.Errorf("decode identity file: %w", err)
	}
	if current.ServerID == "" || current.ServerToken == "" || current.PrivateKeyBase64 == "" {
		return identity{}, errors.New("identity file is incomplete")
	}
	return current, nil
}

func saveIdentity(path string, current identity) error {
	current.UpdatedAt = time.Now().UTC()
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".game-server-identity-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer func() { _ = os.Remove(temporaryName) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	encoder := json.NewEncoder(temporary)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(current); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func endpoint(baseURL, path string) string { return strings.TrimRight(baseURL, "/") + path }
