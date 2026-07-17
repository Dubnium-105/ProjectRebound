package relayregistry

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"net"
	"testing"
	"time"

	"github.com/projectrebound/matchserver/internal/config"
)

func TestAuthorityIssuesNodeBoundEd25519Certificate(t *testing.T) {
	authority, err := NewAuthority(config.Defaults.RelayRegistry, "development")
	if err != nil || !authority.Ephemeral() {
		t.Fatalf("authority = %#v, %v", authority, err)
	}
	csr := testCSR(t)
	certificatePEM, fingerprint, expiresAt, err := authority.IssueClientCertificate("relay_test", csr, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode([]byte(certificatePEM))
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(authority.certificate)
	if _, err := certificate.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Fatalf("verify issued certificate: %v", err)
	}
	if certificate.Subject.CommonName != "relay_test" || len(certificate.URIs) != 1 || certificate.URIs[0].String() != "spiffe://projectrebound/relay/relay_test" {
		t.Fatalf("issued identity = %q, %#v", certificate.Subject.CommonName, certificate.URIs)
	}
	if fingerprint == "" || !expiresAt.After(time.Now()) {
		t.Fatalf("fingerprint/expires = %q, %v", fingerprint, expiresAt)
	}
	tlsConfig, err := authority.ServerTLSConfig()
	if err != nil {
		t.Fatal(err)
	}
	if tlsConfig.MinVersion != tls.VersionTLS13 || tlsConfig.ClientAuth != tls.RequireAndVerifyClientCert {
		t.Fatalf("unsafe relay TLS config: %#v", tlsConfig)
	}
}

func TestAuthorityRejectsMalformedCSR(t *testing.T) {
	authority, err := NewAuthority(config.Defaults.RelayRegistry, "development")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := authority.IssueClientCertificate("relay_test", "not a csr", time.Hour); err == nil {
		t.Fatal("malformed CSR was accepted")
	}
}

func TestAuthorityRequiresMutuallyAuthenticatedTLS(t *testing.T) {
	authority, err := NewAuthority(config.Defaults.RelayRegistry, "development")
	if err != nil {
		t.Fatal(err)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: "relay-node"},
	}, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	certificatePEM, _, _, err := authority.IssueClientCertificate(
		"relay_mtls", string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})), time.Hour,
	)
	if err != nil {
		t.Fatal(err)
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	clientCertificate, err := tls.X509KeyPair(
		[]byte(certificatePEM), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !publicKey.Equal(clientCertificate.PrivateKey.(ed25519.PrivateKey).Public()) {
		t.Fatal("issued certificate did not retain the CSR key")
	}
	roots := x509.NewCertPool()
	roots.AddCert(authority.certificate)
	serverConfig, err := authority.ServerTLSConfig()
	if err != nil {
		t.Fatal(err)
	}
	if err := testTLSHandshake(serverConfig, &tls.Config{
		MinVersion: tls.VersionTLS13, RootCAs: roots, ServerName: "localhost",
		Certificates: []tls.Certificate{clientCertificate},
	}); err != nil {
		t.Fatalf("valid mTLS handshake: %v", err)
	}
	if err := testTLSHandshake(serverConfig, &tls.Config{
		MinVersion: tls.VersionTLS13, RootCAs: roots, ServerName: "localhost",
	}); err == nil {
		t.Fatal("client without a relay certificate completed mTLS")
	}
}

func testTLSHandshake(serverConfig, clientConfig *tls.Config) error {
	serverSide, clientSide := net.Pipe()
	defer serverSide.Close()
	defer clientSide.Close()
	server := tls.Server(serverSide, serverConfig)
	client := tls.Client(clientSide, clientConfig)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	serverResult := make(chan error, 1)
	go func() { serverResult <- server.HandshakeContext(ctx) }()
	clientErr := client.HandshakeContext(ctx)
	serverErr := <-serverResult
	if clientErr != nil {
		return clientErr
	}
	return serverErr
}

func testCSR(t *testing.T) string {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: "relay-node"},
	}, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}))
}
