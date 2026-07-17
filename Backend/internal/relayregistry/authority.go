package relayregistry

import (
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/projectrebound/matchserver/internal/config"
)

type Authority struct {
	certificate    *x509.Certificate
	privateKey     crypto.Signer
	certificatePEM []byte
	now            func() time.Time
	ephemeral      bool
}

func NewAuthority(cfg config.RelayRegistryConfig, environment string) (*Authority, error) {
	if strings.TrimSpace(cfg.CACertificatePEMBase64) == "" || strings.TrimSpace(cfg.CAPrivateKeyPEMBase64) == "" {
		if strings.EqualFold(environment, "production") {
			return nil, errors.New("relay CA certificate and private key are required in production")
		}
		return generateDevelopmentAuthority()
	}
	certificatePEM, err := decodeBase64(cfg.CACertificatePEMBase64)
	if err != nil {
		return nil, fmt.Errorf("decode relay CA certificate: %w", err)
	}
	privateKeyPEM, err := decodeBase64(cfg.CAPrivateKeyPEMBase64)
	if err != nil {
		return nil, fmt.Errorf("decode relay CA private key: %w", err)
	}
	certificateBlock, _ := pem.Decode(certificatePEM)
	if certificateBlock == nil || certificateBlock.Type != "CERTIFICATE" {
		return nil, errors.New("relay CA certificate PEM is invalid")
	}
	certificate, err := x509.ParseCertificate(certificateBlock.Bytes)
	if err != nil || !certificate.IsCA {
		return nil, errors.New("relay CA certificate must be a valid CA")
	}
	privateKeyBlock, _ := pem.Decode(privateKeyPEM)
	if privateKeyBlock == nil {
		return nil, errors.New("relay CA private key PEM is invalid")
	}
	parsedKey, err := x509.ParsePKCS8PrivateKey(privateKeyBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse relay CA private key: %w", err)
	}
	privateKey, ok := parsedKey.(crypto.Signer)
	if !ok {
		return nil, errors.New("relay CA private key is not a supported signer")
	}
	publicDER, err := x509.MarshalPKIXPublicKey(privateKey.Public())
	if err != nil {
		return nil, err
	}
	certificatePublicDER, err := x509.MarshalPKIXPublicKey(certificate.PublicKey)
	if err != nil || !stringEqual(publicDER, certificatePublicDER) {
		return nil, errors.New("relay CA certificate and private key do not match")
	}
	return &Authority{certificate: certificate, privateKey: privateKey, certificatePEM: certificatePEM, now: time.Now}, nil
}

func generateDevelopmentAuthority() (*Authority, error) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber: randomSerial(), Subject: pkix.Name{CommonName: "Project Rebound Development Relay CA"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(7 * 24 * time.Hour),
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

func (a *Authority) IssueClientCertificate(nodeID, csrPEM string, ttl time.Duration) (string, string, time.Time, error) {
	block, _ := pem.Decode([]byte(csrPEM))
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		return "", "", time.Time{}, invalid("CSR must be a PEM encoded certificate request.", nil)
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil || csr.CheckSignature() != nil {
		return "", "", time.Time{}, invalid("CSR signature is invalid.", nil)
	}
	switch csr.PublicKey.(type) {
	case ed25519.PublicKey:
	default:
		return "", "", time.Time{}, invalid("Relay node CSR must use an Ed25519 key.", nil)
	}
	now := a.now().UTC()
	expiresAt := now.Add(ttl)
	identity, _ := url.Parse("spiffe://projectrebound/relay/" + nodeID)
	template := &x509.Certificate{
		SerialNumber: randomSerial(), Subject: pkix.Name{CommonName: nodeID},
		NotBefore: now.Add(-time.Minute), NotAfter: expiresAt,
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		URIs:        []*url.URL{identity},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, a.certificate, csr.PublicKey, a.privateKey)
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("sign relay client certificate: %w", err)
	}
	fingerprint := sha256.Sum256(der)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), hex.EncodeToString(fingerprint[:]), expiresAt, nil
}

func (a *Authority) ServerTLSConfig() (*tls.Config, error) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	now := a.now().UTC()
	template := &x509.Certificate{
		SerialNumber: randomSerial(), Subject: pkix.Name{CommonName: "control-plane"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(24 * time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:    []string{"control-plane", "localhost"},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, a.certificate, publicKey, a.privateKey)
	if err != nil {
		return nil, err
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return nil, err
	}
	serverCertificate, err := tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER}),
	)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	pool.AddCert(a.certificate)
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{serverCertificate},
		ClientCAs:    pool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
	}, nil
}

func (a *Authority) CACertificatePEM() string { return string(a.certificatePEM) }
func (a *Authority) Ephemeral() bool          { return a.ephemeral }

func randomSerial() *big.Int {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return big.NewInt(time.Now().UnixNano())
	}
	return serial
}

func decodeBase64(value string) ([]byte, error) {
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err == nil {
		return decoded, nil
	}
	return base64.RawStdEncoding.DecodeString(value)
}

func stringEqual(left, right []byte) bool {
	leftHash := sha256.Sum256(left)
	rightHash := sha256.Sum256(right)
	return leftHash == rightHash
}
