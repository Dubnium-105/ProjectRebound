package update

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The fixture uses a public test seed. It never contains an operational key.
func TestStrictOnlineManifestWireFixture(t *testing.T) {
	cfg := testUpdateConfig(t)
	cfg.SigningKeyID = "interop-test-key"
	cfg.SigningPrivateKeyBase64 = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{17}, 32))
	signer, err := NewSigner(cfg, "test")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := signer.Sign(Manifest{
		OnlineCompatibility: testOnlineCompatibility(), SchemaVersion: 1,
		Product: "project-rebound", Platform: "windows", Architecture: "amd64", Channel: ChannelToolbox,
		Version: "0.9.12", MinimumSupportedVersion: "0.9.12", PublishedAt: time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC),
		Files: []File{{FileID: "strict-test-executable", Path: toolboxExecutablePath, Size: 1234,
			SHA256: repeatHex("a"), Compression: "none", DownloadURL: "https://fixture.invalid/toolbox.exe?a=1&b=2"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	publicKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{17}, 32)).Public().(ed25519.PublicKey)
	fixture := struct {
		PublicKey string   `json:"public_key"`
		Manifest  Manifest `json:"manifest"`
	}{
		base64.StdEncoding.EncodeToString(publicKey), manifest,
	}
	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	path := filepath.Join("..", "..", "api", "fixtures", "strict-online-update-v1.json")
	if os.Getenv("UPDATE_RELAY_FIXTURE") == "1" {
		if err := os.WriteFile(path, encoded, 0600); err != nil {
			t.Fatal(err)
		}
	}
	expected, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.ReplaceAll(expected, []byte("\r\n"), []byte("\n")), encoded) {
		t.Fatal("Go signed manifest no longer matches the cross-language fixture")
	}
}
