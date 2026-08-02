package gameserver

import (
	"bytes"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"strings"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/config"
)

type Certificate struct {
	PEM         string
	Fingerprint string
	PublicKey   []byte
	Serial      string
	NotBefore   time.Time
	ExpiresAt   time.Time
}

type Authority struct {
	certificate    *x509.Certificate
	privateKey     crypto.Signer
	certificatePEM []byte
	now            func() time.Time
	ephemeral      bool
}

func NewAuthority(cfg config.GameServerConfig, environment string) (*Authority, error) {
	if strings.TrimSpace(cfg.CACertificatePEMBase64) == "" || strings.TrimSpace(cfg.CAPrivateKeyPEMBase64) == "" {
		if strings.EqualFold(environment, "production") {
			return nil, errors.New("game server CA certificate and private key are required in production")
		}
		return generateDevelopmentAuthority()
	}
	certificatePEM, err := base64.StdEncoding.DecodeString(strings.TrimSpace(cfg.CACertificatePEMBase64))
	if err != nil {
		return nil, fmt.Errorf("decode game server CA certificate: %w", err)
	}
	privateKeyPEM, err := base64.StdEncoding.DecodeString(strings.TrimSpace(cfg.CAPrivateKeyPEMBase64))
	if err != nil {
		return nil, fmt.Errorf("decode game server CA private key: %w", err)
	}
	certificateBlock, _ := pem.Decode(certificatePEM)
	if certificateBlock == nil || certificateBlock.Type != "CERTIFICATE" {
		return nil, errors.New("game server CA certificate PEM is invalid")
	}
	certificate, err := x509.ParseCertificate(certificateBlock.Bytes)
	if err != nil || !certificate.IsCA {
		return nil, errors.New("game server CA certificate must be a valid CA")
	}
	privateKeyBlock, _ := pem.Decode(privateKeyPEM)
	if privateKeyBlock == nil {
		return nil, errors.New("game server CA private key PEM is invalid")
	}
	parsedKey, err := x509.ParsePKCS8PrivateKey(privateKeyBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse game server CA private key: %w", err)
	}
	privateKey, ok := parsedKey.(crypto.Signer)
	if !ok {
		return nil, errors.New("game server CA private key is not a supported signer")
	}
	publicDER, err := x509.MarshalPKIXPublicKey(privateKey.Public())
	if err != nil {
		return nil, err
	}
	certificatePublicDER, err := x509.MarshalPKIXPublicKey(certificate.PublicKey)
	if err != nil || !bytes.Equal(publicDER, certificatePublicDER) {
		return nil, errors.New("game server CA certificate and private key do not match")
	}
	return &Authority{
		certificate: certificate, privateKey: privateKey,
		certificatePEM: certificatePEM, now: time.Now,
	}, nil
}

func generateDevelopmentAuthority() (*Authority, error) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber: randomCertificateSerial(),
		Subject:      pkix.Name{CommonName: "Project Rebound Development Game Server CA"},
		NotBefore:    now.Add(-time.Minute), NotAfter: now.Add(7 * 24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, MaxPathLenZero: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, publicKey, privateKey)
	if err != nil {
		return nil, err
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &Authority{
		certificate: certificate, privateKey: privateKey,
		certificatePEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		now:            time.Now, ephemeral: true,
	}, nil
}

func (a *Authority) IssueClientCertificate(serverID, csrPEM string, ttl time.Duration) (Certificate, error) {
	block, _ := pem.Decode([]byte(strings.TrimSpace(csrPEM)))
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return Certificate{}, invalid("CSR must be a PEM encoded certificate request.", map[string]any{"csr_pem": "is invalid"})
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil || csr.CheckSignature() != nil {
		return Certificate{}, invalid("CSR signature is invalid.", map[string]any{"csr_pem": "proof of possession failed"})
	}
	publicKey, ok := csr.PublicKey.(ed25519.PublicKey)
	if !ok || len(publicKey) != ed25519.PublicKeySize {
		return Certificate{}, invalid("Game Server CSR must use an Ed25519 key.", map[string]any{"csr_pem": "unsupported key algorithm"})
	}
	now := a.now().UTC()
	expiresAt := now.Add(ttl)
	if a.certificate.NotAfter.Before(expiresAt) {
		expiresAt = a.certificate.NotAfter
	}
	if !expiresAt.After(now.Add(time.Minute)) {
		return Certificate{}, internal(errors.New("game server CA is expired or too close to expiry"))
	}
	identity, _ := url.Parse("spiffe://projectrebound/game-server/" + serverID)
	template := &x509.Certificate{
		SerialNumber: randomCertificateSerial(), Subject: pkix.Name{CommonName: serverID},
		NotBefore: now.Add(-time.Minute), NotAfter: expiresAt,
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		URIs:        []*url.URL{identity},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, a.certificate, publicKey, a.privateKey)
	if err != nil {
		return Certificate{}, internal(fmt.Errorf("sign game server certificate: %w", err))
	}
	fingerprint := sha256.Sum256(der)
	return Certificate{
		PEM:         string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		Fingerprint: hex.EncodeToString(fingerprint[:]), PublicKey: append([]byte(nil), publicKey...),
		Serial: template.SerialNumber.Text(16), NotBefore: template.NotBefore, ExpiresAt: expiresAt,
	}, nil
}

func (a *Authority) CACertificatePEM() string { return string(a.certificatePEM) }
func (a *Authority) Ephemeral() bool          { return a.ephemeral }

func randomCertificateSerial() *big.Int {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return big.NewInt(time.Now().UnixNano())
	}
	return serial
}
