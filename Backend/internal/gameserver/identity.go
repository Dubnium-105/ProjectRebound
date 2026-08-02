package gameserver

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"strings"
)

// NewNodeIdentity creates a node-owned Ed25519 key and a proof-of-possession CSR.
// Callers must persist the returned private key in a node-local secret store.
func NewNodeIdentity(commonName string) (ed25519.PrivateKey, string, error) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, "", fmt.Errorf("generate Ed25519 node key: %w", err)
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: strings.TrimSpace(commonName)},
	}, privateKey)
	if err != nil {
		return nil, "", fmt.Errorf("create node certificate request: %w", err)
	}
	return privateKey, string(pem.EncodeToMemory(&pem.Block{
		Type: "CERTIFICATE REQUEST", Bytes: csrDER,
	})), nil
}
