package update

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/config"
)

type Signer struct {
	privateKey ed25519.PrivateKey
	publicKey  ed25519.PublicKey
	keyID      string
	ephemeral  bool
}

func NewSigner(cfg config.UpdateConfig, environment string) (*Signer, error) {
	var privateKey ed25519.PrivateKey
	ephemeral := false
	if strings.TrimSpace(cfg.SigningPrivateKeyBase64) == "" {
		if strings.EqualFold(environment, "production") {
			return nil, errors.New("update signing private key is required in production")
		}
		_, generated, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, fmt.Errorf("generate development update key: %w", err)
		}
		privateKey = generated
		ephemeral = true
	} else {
		decoded, err := decodeSigningKey(cfg.SigningPrivateKeyBase64)
		if err != nil {
			return nil, fmt.Errorf("decode update signing key: %w", err)
		}
		switch len(decoded) {
		case ed25519.SeedSize:
			privateKey = ed25519.NewKeyFromSeed(decoded)
		case ed25519.PrivateKeySize:
			privateKey = ed25519.PrivateKey(decoded)
		default:
			return nil, fmt.Errorf("update signing key must be a %d-byte seed or %d-byte private key", ed25519.SeedSize, ed25519.PrivateKeySize)
		}
	}
	publicKey, ok := privateKey.Public().(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("derive update signing public key")
	}
	return &Signer{privateKey: privateKey, publicKey: publicKey, keyID: cfg.SigningKeyID, ephemeral: ephemeral}, nil
}

func (s *Signer) Sign(manifest Manifest) (Manifest, error) {
	manifest.SignatureAlgorithm = SignatureAlgorithm
	manifest.KeyID = s.keyID
	manifest.Signature = ""
	digestBytes, err := json.Marshal(hashPayload(manifest))
	if err != nil {
		return Manifest{}, err
	}
	digest := sha256.Sum256(digestBytes)
	manifest.ManifestHash = hex.EncodeToString(digest[:])
	canonical, err := CanonicalUnsigned(manifest)
	if err != nil {
		return Manifest{}, err
	}
	manifest.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(s.privateKey, canonical))
	return manifest, nil
}

func CanonicalUnsigned(manifest Manifest) ([]byte, error) {
	return json.Marshal(unsignedPayload{
		SchemaVersion: manifest.SchemaVersion, Product: manifest.Product, Platform: manifest.Platform,
		Architecture: manifest.Architecture, Channel: manifest.Channel, Version: manifest.Version,
		MinimumSupportedVersion: manifest.MinimumSupportedVersion, PublishedAt: manifest.PublishedAt,
		Files: manifest.Files, ManifestHash: manifest.ManifestHash,
		SignatureAlgorithm: manifest.SignatureAlgorithm, KeyID: manifest.KeyID,
	})
}

func (s *Signer) Verify(manifest Manifest) error {
	if manifest.KeyID != s.keyID || manifest.SignatureAlgorithm != SignatureAlgorithm {
		return errors.New("manifest signing metadata does not match")
	}
	digestBytes, err := json.Marshal(hashPayload(manifest))
	if err != nil {
		return err
	}
	digest := sha256.Sum256(digestBytes)
	if manifest.ManifestHash != hex.EncodeToString(digest[:]) {
		return errors.New("manifest hash does not match")
	}
	signature, err := base64.StdEncoding.DecodeString(manifest.Signature)
	if err != nil {
		return errors.New("manifest signature is not valid base64")
	}
	canonical, err := CanonicalUnsigned(manifest)
	if err != nil {
		return err
	}
	if !ed25519.Verify(s.publicKey, canonical, signature) {
		return errors.New("manifest signature is invalid")
	}
	return nil
}

func (s *Signer) Ephemeral() bool { return s.ephemeral }

type unsignedPayload struct {
	SchemaVersion           int       `json:"schema_version"`
	Product                 string    `json:"product"`
	Platform                string    `json:"platform"`
	Architecture            string    `json:"architecture"`
	Channel                 string    `json:"channel"`
	Version                 string    `json:"version"`
	MinimumSupportedVersion string    `json:"minimum_supported_version"`
	PublishedAt             time.Time `json:"published_at"`
	Files                   []File    `json:"files"`
	ManifestHash            string    `json:"manifest_hash"`
	SignatureAlgorithm      string    `json:"signature_algorithm"`
	KeyID                   string    `json:"key_id"`
}

type manifestHashPayload struct {
	SchemaVersion           int       `json:"schema_version"`
	Product                 string    `json:"product"`
	Platform                string    `json:"platform"`
	Architecture            string    `json:"architecture"`
	Channel                 string    `json:"channel"`
	Version                 string    `json:"version"`
	MinimumSupportedVersion string    `json:"minimum_supported_version"`
	PublishedAt             time.Time `json:"published_at"`
	Files                   []File    `json:"files"`
	SignatureAlgorithm      string    `json:"signature_algorithm"`
	KeyID                   string    `json:"key_id"`
}

func hashPayload(manifest Manifest) manifestHashPayload {
	return manifestHashPayload{
		SchemaVersion: manifest.SchemaVersion, Product: manifest.Product, Platform: manifest.Platform,
		Architecture: manifest.Architecture, Channel: manifest.Channel, Version: manifest.Version,
		MinimumSupportedVersion: manifest.MinimumSupportedVersion, PublishedAt: manifest.PublishedAt,
		Files: manifest.Files, SignatureAlgorithm: manifest.SignatureAlgorithm, KeyID: manifest.KeyID,
	}
}

func decodeSigningKey(value string) ([]byte, error) {
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err == nil {
		return decoded, nil
	}
	return base64.RawStdEncoding.DecodeString(value)
}
