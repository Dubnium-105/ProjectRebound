package update

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dubnium-105/ProjectRebound/Backend/internal/config"
)

type fixedRelayDirectory struct {
	regions []string
	err     error
}

func (d fixedRelayDirectory) AvailableRegions(context.Context) ([]string, error) {
	return append([]string(nil), d.regions...), d.err
}

func TestSignedManifestCatalogAndChannels(t *testing.T) {
	cfg := testUpdateConfig(t)
	writeRelease(t, cfg.ManifestDirectory, "stable.json", SourceRelease{
		SchemaVersion: 1, Product: cfg.Product, Platform: "windows", Architecture: "amd64", Channel: "stable",
		Version: "1.2.0", MinimumSupportedVersion: "1.1.0", PublishedAt: time.Date(2026, 7, 18, 1, 2, 3, 0, time.UTC),
		Files: []SourceFile{
			{FileID: "file_b", Path: "bin/z.dll", Size: 20, SHA256: repeatHex("b"), Compression: "zstd", ObjectKey: "stable/1.2.0/z.dll.zst"},
			{FileID: "file_a", Path: "bin/a.exe", Size: 10, SHA256: repeatHex("a"), Compression: "none", ObjectKey: "stable/1.2.0/a.exe"},
		},
	})
	writeRelease(t, cfg.ManifestDirectory, "beta.json", SourceRelease{
		SchemaVersion: 1, Product: cfg.Product, Platform: "windows", Architecture: "amd64", Channel: "beta",
		Version: "1.3.0-beta.1", MinimumSupportedVersion: "1.1.0", PublishedAt: time.Date(2026, 7, 18, 2, 3, 4, 0, time.UTC),
		Files: []SourceFile{{FileID: "file_beta", Path: "game.exe", Size: 30, SHA256: repeatHex("c"), Compression: "none", ObjectKey: "beta/1.3.0-beta.1/game.exe"}},
	})
	writeRelease(t, cfg.ManifestDirectory, "toolbox.json", SourceRelease{
		SchemaVersion: 1, Product: cfg.Product, Platform: "windows", Architecture: "amd64", Channel: "toolbox",
		Version: "0.9.0", MinimumSupportedVersion: "0.8.0", PublishedAt: time.Date(2026, 7, 18, 3, 4, 5, 0, time.UTC),
		Files: []SourceFile{{FileID: "file_toolbox", Path: "Rebound_Toolbox.exe", Size: 40, SHA256: repeatHex("d"), Compression: "none", ObjectKey: "toolbox/0.9.0/Rebound_Toolbox.exe"}},
	})
	service, err := NewService(cfg, "test", fixedRelayDirectory{regions: []string{"hk", "us-west"}})
	if err != nil {
		t.Fatal(err)
	}
	stable, err := service.Check(context.Background(), CheckInput{Platform: "windows", Version: "1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	if stable.LatestVersion != "1.2.0" || !stable.UpdateAvailable || !stable.UpdateRequired || stable.Channel != "stable" {
		t.Fatalf("stable check = %#v", stable)
	}
	beta, err := service.Check(context.Background(), CheckInput{Platform: "windows", Architecture: "amd64", Channel: "beta", Version: "1.2.0"})
	if err != nil {
		t.Fatal(err)
	}
	if beta.LatestVersion != "1.3.0-beta.1" || !beta.UpdateAvailable {
		t.Fatalf("beta check = %#v", beta)
	}
	toolbox, err := service.Check(context.Background(), CheckInput{Platform: "windows", Architecture: "amd64", Channel: "toolbox", Version: "0.8.0"})
	if err != nil {
		t.Fatal(err)
	}
	if toolbox.LatestVersion != "0.9.0" || !toolbox.UpdateAvailable || toolbox.Channel != "toolbox" {
		t.Fatalf("toolbox check = %#v", toolbox)
	}
	manifest, err := service.Manifest(context.Background(), "windows", "amd64", "stable", "1.2.0")
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Files[0].Path != "bin/a.exe" || manifest.Files[1].Path != "bin/z.dll" {
		t.Fatalf("manifest file order = %#v", manifest.Files)
	}
	if err := service.signer.Verify(manifest); err != nil {
		t.Fatalf("verify signed manifest: %v", err)
	}
	mutated := manifest
	mutated.Files = append([]File(nil), manifest.Files...)
	mutated.Files[0].SHA256 = repeatHex("f")
	if err := service.signer.Verify(mutated); err == nil {
		t.Fatal("mutated manifest signature was accepted")
	}
	download, err := service.File(context.Background(), "file_a")
	if err != nil {
		t.Fatal(err)
	}
	if download.DownloadURL != "https://cdn.example.test/releases/stable/1.2.0/a.exe" {
		t.Fatalf("download URL = %q", download.DownloadURL)
	}
	clientConfig, err := service.ClientConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !clientConfig.Relay.Available || len(clientConfig.Relay.Regions) != 2 || clientConfig.RealtimeURL != cfg.RealtimeURL {
		t.Fatalf("client config = %#v", clientConfig)
	}
}

func TestCanonicalManifestSigningIsDeterministic(t *testing.T) {
	cfg := testUpdateConfig(t)
	signer, err := NewSigner(cfg, "test")
	if err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{
		SchemaVersion: 1, Product: cfg.Product, Platform: "windows", Architecture: "amd64", Channel: "stable",
		Version: "1.0.0", MinimumSupportedVersion: "1.0.0", PublishedAt: time.Unix(1_700_000_000, 0).UTC(),
		Files: []File{{FileID: "file_a", Path: "game.exe", Size: 1, SHA256: repeatHex("a"), Compression: "none", DownloadURL: "https://cdn.example.test/game.exe"}},
	}
	first, err := signer.Sign(manifest)
	if err != nil {
		t.Fatal(err)
	}
	second, err := signer.Sign(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if first.ManifestHash != second.ManifestHash || first.Signature != second.Signature {
		t.Fatalf("signing is not deterministic: %#v / %#v", first, second)
	}
	if err := signer.Verify(first); err != nil {
		t.Fatal(err)
	}
}

func TestCatalogRejectsUnsafeObjectKey(t *testing.T) {
	cfg := testUpdateConfig(t)
	writeRelease(t, cfg.ManifestDirectory, "unsafe.json", SourceRelease{
		SchemaVersion: 1, Product: cfg.Product, Platform: "windows", Architecture: "amd64", Channel: "stable",
		Version: "1.0.0", MinimumSupportedVersion: "1.0.0", PublishedAt: time.Now().UTC(),
		Files: []SourceFile{{FileID: "file_a", Path: "game.exe", Size: 1, SHA256: repeatHex("a"), Compression: "none", ObjectKey: "../private.key"}},
	})
	if _, err := NewService(cfg, "test", nil); err == nil {
		t.Fatal("unsafe object key was accepted")
	}
}

func TestProductionRequiresSigningKeyAndAllowsManagedCatalogBootstrap(t *testing.T) {
	cfg := config.Defaults.Update
	cfg.ManifestDirectory = t.TempDir()
	cfg.SigningPrivateKeyBase64 = ""
	if _, err := NewService(cfg, "production", nil); err == nil {
		t.Fatal("production accepted a missing update signing key")
	}
	cfg.SigningPrivateKeyBase64 = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x24}, ed25519.SeedSize))
	if _, err := NewService(cfg, "production", nil); err != nil {
		t.Fatalf("production rejected empty static catalog before managed catalog initialization: %v", err)
	}
}

func TestVersionComparison(t *testing.T) {
	for _, testCase := range []struct {
		left, right string
		want        int
	}{
		{left: "1.0.0", right: "1.0.1", want: -1},
		{left: "1.0.0-beta.2", right: "1.0.0-beta.10", want: -1},
		{left: "1.0.0-beta.1", right: "1.0.0", want: -1},
		{left: "2.0.0", right: "1.9.9", want: 1},
	} {
		got, err := compareVersions(testCase.left, testCase.right)
		if err != nil || got != testCase.want {
			t.Fatalf("compareVersions(%q, %q) = %d, %v", testCase.left, testCase.right, got, err)
		}
	}
}

func testUpdateConfig(t *testing.T) config.UpdateConfig {
	t.Helper()
	cfg := config.Defaults.Update
	cfg.ManifestDirectory = t.TempDir()
	cfg.CDNBaseURL = "https://cdn.example.test/releases"
	cfg.SigningKeyID = "update-test-1"
	cfg.SigningPrivateKeyBase64 = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x42}, ed25519.SeedSize))
	cfg.RealtimeURL = "wss://realtime.example.test/v1/realtime/connect"
	return cfg
}

func writeRelease(t *testing.T, directory, name string, release SourceRelease) {
	t.Helper()
	contents, err := json.Marshal(release)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, name), contents, 0o600); err != nil {
		t.Fatal(err)
	}
}

func repeatHex(character string) string { return string(bytes.Repeat([]byte(character), 64)) }
