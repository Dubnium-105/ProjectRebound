package relayruntime

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestLoadOrEnrollPersistsNodeIdentityAndConsumesBootstrapLocally(t *testing.T) {
	now := time.Now().UTC()
	caPublic, caPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test relay CA"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, caPublic, caPrivate)
	if err != nil {
		t.Fatal(err)
	}
	caCertificate, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	caPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}))
	keySigner := newTestSigner(t, "relay-key-a")
	bootstrap := "01234567890123456789012345678901"
	var enrollCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		enrollCalls.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+bootstrap {
			t.Errorf("bootstrap authorization header was not supplied")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var request struct {
			CSRPEM string `json:"csr_pem"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		block, _ := pem.Decode([]byte(request.CSRPEM))
		csr, err := x509.ParseCertificateRequest(block.Bytes)
		if err != nil || csr.CheckSignature() != nil {
			t.Errorf("invalid enrollment CSR: %v", err)
			return
		}
		expiresAt := now.Add(12 * time.Hour)
		certificateDER, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
			SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "relay_test"},
			NotBefore: now.Add(-time.Minute), NotAfter: expiresAt,
			KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		}, caCertificate, csr.PublicKey, caPrivate)
		if err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"node":               map[string]any{"node_id": "relay_test"},
				"node_token":         "rnt_abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789abcd",
				"certificate_pem":    string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER})),
				"ca_certificate_pem": caPEM, "certificate_expires_at": expiresAt,
				"relay_token_keyset": Keyset{Keys: []PublicKey{keySigner.key()}},
			},
			"request_id": "req_test",
		})
	}))
	defer server.Close()

	cfg := DefaultConfig
	cfg.ControlPlaneURL = server.URL
	cfg.BootstrapToken = bootstrap
	cfg.DataDir = t.TempDir()
	cfg.AdvertisedEndpoints = []Endpoint{{Protocol: "UDP", Host: "203.0.113.20", Port: 443}}
	identity, err := LoadOrEnroll(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if identity.NodeID != "relay_test" || identity.NodeToken == "" || len(identity.Keyset.Keys) != 1 {
		t.Fatalf("identity = %#v", identity)
	}
	if _, err := identity.TLSCertificate(); err != nil {
		t.Fatalf("stored identity certificate: %v", err)
	}
	if certificateRenewalDue(identity, now) || !certificateRenewalDue(identity, identity.CertificateExpiry.Add(-2*time.Hour)) {
		t.Fatal("certificate 25-percent renewal threshold was not enforced")
	}
	if _, err := os.Stat(filepath.Join(cfg.DataDir, "identity.json")); err != nil {
		t.Fatal(err)
	}
	cfg.BootstrapToken = ""
	reloaded, err := LoadOrEnroll(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.NodeID != identity.NodeID || enrollCalls.Load() != 1 {
		t.Fatalf("persisted identity was not reused: calls=%d, identity=%#v", enrollCalls.Load(), reloaded)
	}
}
