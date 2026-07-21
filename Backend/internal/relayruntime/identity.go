package relayruntime

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Identity struct {
	NodeID            string    `json:"node_id"`
	NodeToken         string    `json:"node_token"`
	PrivateKeyBase64  string    `json:"private_key_base64"`
	CertificatePEM    string    `json:"certificate_pem"`
	CACertificatePEM  string    `json:"ca_certificate_pem"`
	CertificateExpiry time.Time `json:"certificate_expires_at"`
	Keyset            Keyset    `json:"relay_token_keyset"`
}

func LoadOrEnroll(ctx context.Context, cfg Config) (Identity, error) {
	statePath := filepath.Join(cfg.DataDir, "identity.json")
	identity, err := loadIdentity(statePath)
	if err == nil {
		if !certificateRenewalDue(identity, time.Now().UTC()) {
			return identity, nil
		}
		renewed, renewErr := renewIdentity(ctx, cfg, identity)
		if renewErr == nil {
			if err := saveIdentity(statePath, renewed); err != nil {
				return Identity{}, err
			}
			return renewed, nil
		}
		if time.Until(identity.CertificateExpiry) > 5*time.Minute {
			return identity, nil
		}
		return Identity{}, fmt.Errorf("renew expiring relay certificate: %w", renewErr)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return Identity{}, err
	}
	if len(strings.TrimSpace(cfg.BootstrapToken)) < 32 {
		return Identity{}, errors.New("EDGE_RELAY_BOOTSTRAP_TOKEN is required for first enrollment")
	}
	identity, err = enrollIdentity(ctx, cfg)
	if err != nil {
		return Identity{}, err
	}
	if err := saveIdentity(statePath, identity); err != nil {
		return Identity{}, err
	}
	return identity, nil
}

func certificateRenewalDue(identity Identity, now time.Time) bool {
	block, _ := pem.Decode([]byte(identity.CertificatePEM))
	if block == nil {
		return true
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return true
	}
	lifetime := certificate.NotAfter.Sub(certificate.NotBefore)
	return !now.Before(certificate.NotAfter.Add(-lifetime / 4))
}

func loadIdentity(path string) (Identity, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return Identity{}, err
	}
	var identity Identity
	if err := json.Unmarshal(contents, &identity); err != nil {
		return Identity{}, fmt.Errorf("parse relay identity state: %w", err)
	}
	if err := validateIdentity(identity); err != nil {
		return Identity{}, fmt.Errorf("validate relay identity state: %w", err)
	}
	return identity, nil
}

func saveIdentity(path string, identity Identity) error {
	contents, err := json.MarshalIndent(identity, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create relay data directory: %w", err)
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, contents, 0o600); err != nil {
		return fmt.Errorf("write relay identity state: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("replace relay identity state: %w", err)
	}
	return nil
}

func enrollIdentity(ctx context.Context, cfg Config) (Identity, error) {
	privateKey, csrPEM, err := newCSR(cfg.DisplayName)
	if err != nil {
		return Identity{}, err
	}
	request := map[string]any{
		"display_name": cfg.DisplayName, "region": cfg.Region, "zone": cfg.Zone, "provider": cfg.Provider,
		"software_version": cfg.SoftwareVersion, "protocol_version": cfg.ProtocolVersion,
		"advertised_endpoints": cfg.AdvertisedEndpoints, "supported_protocols": cfg.SupportedProtocols,
		"capacity": map[string]any{"max_allocations": cfg.MaxAllocations, "max_egress_bps": cfg.MaxEgressBPS},
		"csr_pem":  csrPEM,
	}
	var response struct {
		Data struct {
			Node struct {
				NodeID string `json:"node_id"`
			} `json:"node"`
			NodeToken         string    `json:"node_token"`
			CertificatePEM    string    `json:"certificate_pem"`
			CACertificatePEM  string    `json:"ca_certificate_pem"`
			CertificateExpiry time.Time `json:"certificate_expires_at"`
			Keyset            Keyset    `json:"relay_token_keyset"`
		} `json:"data"`
	}
	if err := postJSON(ctx, cfg.ControlPlaneURL, "/internal/v1/relay-nodes/enroll", cfg.BootstrapToken, request, &response); err != nil {
		return Identity{}, fmt.Errorf("enroll relay node: %w", err)
	}
	identity := Identity{
		NodeID: response.Data.Node.NodeID, NodeToken: response.Data.NodeToken,
		PrivateKeyBase64: base64.RawStdEncoding.EncodeToString(privateKey),
		CertificatePEM:   response.Data.CertificatePEM, CACertificatePEM: response.Data.CACertificatePEM,
		CertificateExpiry: response.Data.CertificateExpiry, Keyset: response.Data.Keyset,
	}
	if err := validateIdentity(identity); err != nil {
		return Identity{}, fmt.Errorf("validate enrolled relay identity: %w", err)
	}
	return identity, nil
}

func renewIdentity(ctx context.Context, cfg Config, current Identity) (Identity, error) {
	privateKey, csrPEM, err := newCSR(cfg.DisplayName)
	if err != nil {
		return Identity{}, err
	}
	var response struct {
		Data struct {
			CertificatePEM    string    `json:"certificate_pem"`
			CACertificatePEM  string    `json:"ca_certificate_pem"`
			CertificateExpiry time.Time `json:"certificate_expires_at"`
			Keyset            Keyset    `json:"relay_token_keyset"`
		} `json:"data"`
	}
	path := "/internal/v1/relay-nodes/" + url.PathEscape(current.NodeID) + "/certificate/renew"
	if err := postJSON(ctx, cfg.ControlPlaneURL, path, current.NodeToken, map[string]any{"csr_pem": csrPEM}, &response); err != nil {
		return Identity{}, err
	}
	current.PrivateKeyBase64 = base64.RawStdEncoding.EncodeToString(privateKey)
	current.CertificatePEM = response.Data.CertificatePEM
	current.CACertificatePEM = response.Data.CACertificatePEM
	current.CertificateExpiry = response.Data.CertificateExpiry
	current.Keyset = response.Data.Keyset
	if err := validateIdentity(current); err != nil {
		return Identity{}, fmt.Errorf("validate renewed relay identity: %w", err)
	}
	return current, nil
}

func validateIdentity(identity Identity) error {
	if identity.NodeID == "" || len(identity.NodeToken) < 64 || identity.CertificatePEM == "" ||
		identity.CACertificatePEM == "" || identity.PrivateKeyBase64 == "" || len(identity.Keyset.Keys) == 0 ||
		identity.CertificateExpiry.IsZero() {
		return errors.New("relay identity state is incomplete")
	}
	if _, err := identity.TLSCertificate(); err != nil {
		return err
	}
	if _, err := NewTokenVerifier(identity.Keyset); err != nil {
		return err
	}
	return nil
}

func newCSR(commonName string) (ed25519.PrivateKey, string, error) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, "", err
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: commonName},
	}, privateKey)
	if err != nil {
		return nil, "", err
	}
	return privateKey, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})), nil
}

func (i Identity) TLSCertificate() (tls.Certificate, error) {
	privateKey, err := base64.RawStdEncoding.DecodeString(i.PrivateKeyBase64)
	if err != nil || len(privateKey) != ed25519.PrivateKeySize {
		return tls.Certificate{}, errors.New("relay identity private key is invalid")
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(ed25519.PrivateKey(privateKey))
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.X509KeyPair(
		[]byte(i.CertificatePEM), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER}),
	)
}

func postJSON(ctx context.Context, baseURL, path, bearerToken string, requestBody, responseBody any) error {
	encoded, err := json.Marshal(requestBody)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+path, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+bearerToken)
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 15 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	contents, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var failure struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(contents, &failure)
		if failure.Error.Code == "" {
			failure.Error.Code = "HTTP_" + response.Status
		}
		return fmt.Errorf("control plane rejected request: %s", failure.Error.Code)
	}
	if err := json.Unmarshal(contents, responseBody); err != nil {
		return fmt.Errorf("decode control plane response: %w", err)
	}
	return nil
}
