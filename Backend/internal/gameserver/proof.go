package gameserver

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/config"
	"github.com/jackc/pgx/v5"
)

const (
	HeaderGameServerID           = "X-Game-Server-Id"
	HeaderCertificateFingerprint = "X-Game-Server-Certificate"
	HeaderRequestTimestamp       = "X-Game-Server-Timestamp"
	HeaderRequestNonce           = "X-Game-Server-Nonce"
	HeaderCredentialGeneration   = "X-Game-Server-Generation"
	HeaderRequestSignature       = "X-Game-Server-Signature"
	signatureContext             = "PR-GAME-SERVER-V1"
)

type ProofVerifier struct {
	repository *Repository
	config     config.GameServerConfig
	now        func() time.Time
}

func NewProofVerifier(repository *Repository, cfg config.GameServerConfig) *ProofVerifier {
	return &ProofVerifier{repository: repository, config: cfg, now: time.Now}
}

func SignedRequestFromHTTP(r *http.Request, serverID string) (SignedRequestInput, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return SignedRequestInput{}, invalid("Invalid signed request body.", nil)
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	timestamp, timestampErr := strconv.ParseInt(strings.TrimSpace(r.Header.Get(HeaderRequestTimestamp)), 10, 64)
	generation, generationErr := strconv.ParseInt(strings.TrimSpace(r.Header.Get(HeaderCredentialGeneration)), 10, 64)
	if timestampErr != nil {
		timestamp = 0
	}
	if generationErr != nil {
		generation = 0
	}
	target := r.URL.EscapedPath()
	if target == "" {
		target = "/"
	}
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	return SignedRequestInput{
		ServerID: strings.TrimSpace(serverID), ServerToken: bearerToken(r),
		CertificateFingerprint: strings.ToLower(strings.TrimSpace(r.Header.Get(HeaderCertificateFingerprint))),
		Timestamp:              timestamp, Nonce: strings.TrimSpace(r.Header.Get(HeaderRequestNonce)),
		CredentialGeneration: generation, Signature: strings.TrimSpace(r.Header.Get(HeaderRequestSignature)),
		Method: strings.ToUpper(r.Method), RequestTarget: target, Body: body,
	}, nil
}

func CanonicalSignedRequest(input SignedRequestInput) string {
	bodyHash := sha256.Sum256(input.Body)
	tokenHash := sha256.Sum256([]byte(input.ServerToken))
	return strings.Join([]string{
		signatureContext,
		strings.ToUpper(input.Method),
		input.RequestTarget,
		hex.EncodeToString(bodyHash[:]),
		strconv.FormatInt(input.Timestamp, 10),
		input.Nonce,
		input.ServerID,
		strconv.FormatInt(input.CredentialGeneration, 10),
		hex.EncodeToString(tokenHash[:]),
	}, "\n")
}

func (v *ProofVerifier) Verify(ctx context.Context, input SignedRequestInput) (SignedRequestPrincipal, error) {
	if input.ServerID == "" || !strings.HasPrefix(input.ServerToken, "gst_") || len(input.ServerToken) < 64 {
		return SignedRequestPrincipal{}, signatureUnauthorized("GAME_SERVER_UNAUTHORIZED", "Valid Game Server credentials are required.")
	}
	now := v.now().UTC()
	tx, err := v.repository.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return SignedRequestPrincipal{}, internal(err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	tokenHash := hashServerToken(input.ServerToken)
	server, err := v.repository.GetForManagement(ctx, tx, input.ServerID, tokenHash, now)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return SignedRequestPrincipal{}, signatureUnauthorized("GAME_SERVER_UNAUTHORIZED", "Valid Game Server credentials are required.")
		}
		return SignedRequestPrincipal{}, internal(err)
	}
	if len(server.CertificatePublicKey) == 0 {
		if server.LegacyAuthExpiresAt == nil || !now.Before(*server.LegacyAuthExpiresAt) {
			return SignedRequestPrincipal{}, signatureUnauthorized("GAME_SERVER_SIGNATURE_REQUIRED", "This Game Server must enroll a node certificate.")
		}
		if err := tx.Commit(ctx); err != nil {
			return SignedRequestPrincipal{}, internal(err)
		}
		return SignedRequestPrincipal{
			ServerID: server.ID, CredentialGeneration: server.CredentialGeneration, Legacy: true,
		}, nil
	}
	if err := v.verifyProof(server, tokenHash, input, now); err != nil {
		return SignedRequestPrincipal{}, err
	}
	nonceDigest := sha256.Sum256([]byte(server.ID + "\x00" + input.Nonce))
	recorded, err := v.repository.RecordRequestNonce(
		ctx, tx, server.ID, nonceDigest[:], now,
		now.Add(2*v.config.SignatureMaxClockSkew()),
	)
	if err != nil {
		return SignedRequestPrincipal{}, internal(err)
	}
	if !recorded {
		return SignedRequestPrincipal{}, signatureUnauthorized("GAME_SERVER_SIGNATURE_REPLAY", "The signed request nonce was already used.")
	}
	if err := tx.Commit(ctx); err != nil {
		return SignedRequestPrincipal{}, internal(err)
	}
	return SignedRequestPrincipal{
		ServerID: server.ID, CredentialGeneration: input.CredentialGeneration,
	}, nil
}

func (v *ProofVerifier) verifyProof(server Server, tokenHash []byte, input SignedRequestInput, now time.Time) error {
	if input.Timestamp == 0 || input.Nonce == "" || input.Signature == "" || input.CertificateFingerprint == "" {
		return signatureUnauthorized("GAME_SERVER_SIGNATURE_REQUIRED", "A complete Game Server request signature is required.")
	}
	requestTime := time.Unix(input.Timestamp, 0).UTC()
	if requestTime.Before(now.Add(-v.config.SignatureMaxClockSkew())) || requestTime.After(now.Add(v.config.SignatureMaxClockSkew())) {
		return signatureUnauthorized("GAME_SERVER_SIGNATURE_EXPIRED", "The Game Server request timestamp is outside the accepted window.")
	}
	nonce, err := base64.RawURLEncoding.DecodeString(input.Nonce)
	if err != nil || len(nonce) < 16 || len(nonce) > 64 {
		return signatureUnauthorized("GAME_SERVER_SIGNATURE_INVALID", "The Game Server request nonce is invalid.")
	}
	signature, err := base64.RawURLEncoding.DecodeString(input.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return signatureUnauthorized("GAME_SERVER_SIGNATURE_INVALID", "The Game Server request signature is invalid.")
	}
	usingPrevious := !bytes.Equal(server.ServerTokenHash, tokenHash)
	publicKey := server.CertificatePublicKey
	fingerprint := server.CertificateFingerprint
	expiresAt := server.CertificateExpiresAt
	expectedGeneration := server.CredentialGeneration
	if usingPrevious {
		publicKey = server.PreviousCertificatePublicKey
		fingerprint = server.PreviousCertificateFingerprint
		expiresAt = server.PreviousCertificateExpiresAt
		expectedGeneration--
	}
	if len(publicKey) != ed25519.PublicKeySize || expiresAt == nil || !now.Before(*expiresAt) ||
		input.CertificateFingerprint != fingerprint || input.CredentialGeneration != expectedGeneration {
		return signatureUnauthorized("GAME_SERVER_SIGNATURE_INVALID", "The Game Server certificate or credential generation is invalid.")
	}
	if !ed25519.Verify(ed25519.PublicKey(publicKey), []byte(CanonicalSignedRequest(input)), signature) {
		return signatureUnauthorized("GAME_SERVER_SIGNATURE_INVALID", "The Game Server request signature is invalid.")
	}
	return nil
}

func signatureUnauthorized(code, message string) error {
	return &ServiceError{Status: http.StatusUnauthorized, Code: code, Message: message}
}

func SignRequest(input SignedRequestInput, privateKey ed25519.PrivateKey) (string, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return "", fmt.Errorf("Ed25519 private key has invalid length")
	}
	return base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, []byte(CanonicalSignedRequest(input)))), nil
}
