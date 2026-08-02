package gameserver

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"testing"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/config"
)

func TestAuthorityIssuesNodeOwnedEd25519Identity(t *testing.T) {
	authority, err := NewAuthority(config.Defaults.GameServer, "development")
	if err != nil {
		t.Fatal(err)
	}
	privateKey, csrPEM, err := NewNodeIdentity("instance-test")
	if err != nil {
		t.Fatal(err)
	}
	issued, err := authority.IssueClientCertificate("gs_test", csrPEM, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode([]byte(issued.PEM))
	if block == nil {
		t.Fatal("issued certificate is not PEM")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, ok := certificate.PublicKey.(ed25519.PublicKey)
	if !ok || !publicKey.Equal(privateKey.Public()) {
		t.Fatal("issued certificate does not retain the node-owned public key")
	}
	if len(certificate.URIs) != 1 || certificate.URIs[0].String() != "spiffe://projectrebound/game-server/gs_test" {
		t.Fatalf("certificate identity = %#v", certificate.URIs)
	}
	if issued.Fingerprint == "" || len(issued.PublicKey) != ed25519.PublicKeySize {
		t.Fatalf("issued certificate metadata = %#v", issued)
	}
}

func TestSignedRequestCanonicalFormDetectsTampering(t *testing.T) {
	privateKey, _, err := NewNodeIdentity("canonical-test")
	if err != nil {
		t.Fatal(err)
	}
	input := SignedRequestInput{
		ServerID: "gs_test", ServerToken: "gst_abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789__",
		Timestamp: 123456789, Nonce: "bm9uY2Utbm9uY2Utbm9uY2U", CredentialGeneration: 3,
		Method: "POST", RequestTarget: "/v1/game-servers/gs_test/heartbeat",
		Body: []byte(`{"state":"READY"}`),
	}
	signature, err := SignRequest(input, privateKey)
	if err != nil || signature == "" {
		t.Fatalf("signature = %q, %v", signature, err)
	}
	original := CanonicalSignedRequest(input)
	input.Body = []byte(`{"state":"RUNNING"}`)
	if CanonicalSignedRequest(input) == original {
		t.Fatal("canonical request did not bind the body")
	}
}
