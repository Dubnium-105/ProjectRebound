package update

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	cfg.VNTRoomsEnabled = true
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
		VNTRuntime: &VNTRuntimeRelease{VNTSVersion: "1.2.12", WrapperVersion: "0.1.0"},
		Files:      []SourceFile{{FileID: "file_toolbox", Path: "rebound_toolbox.exe", Size: 40, SHA256: repeatHex("d"), Compression: "none", ObjectKey: "toolbox/0.9.0/Rebound_Toolbox.exe"}},
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
	toolboxRuntimes, err := service.PublishedVNTRuntimes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(toolboxRuntimes) != 1 || toolboxRuntimes[0].VNTSVersion != "1.2.12" ||
		toolboxRuntimes[0].WrapperVersion != "0.1.0" {
		t.Fatalf("toolbox VNT runtimes = %#v", toolboxRuntimes)
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
	if !clientConfig.Relay.Available || len(clientConfig.Relay.Regions) != 2 ||
		clientConfig.RealtimeURL != cfg.RealtimeURL || !clientConfig.Features.VNTRooms {
		t.Fatalf("client config = %#v", clientConfig)
	}
}

func TestResolveVNTRuntimeReadsVerifiedToolboxReleaseSidecar(t *testing.T) {
	body := []byte(`{"releaseId":"project-rebound-vnt-runtime-0.1.0","wrapperVersion":"0.1.0","vnts":{"version":"1.2.12"}}`)
	digest := sha256.Sum256(body)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/objects/toolbox/0.9.0/vnt-runtime-manifest.json" {
			http.NotFound(w, request)
			return
		}
		_, _ = w.Write(body)
	}))
	defer server.Close()
	cfg := testUpdateConfig(t)
	service, err := NewService(cfg, "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	service.SetManagedReleaseURLs(server.URL+"/public", server.URL+"/objects")
	source := SourceRelease{
		SchemaVersion: 1, Product: cfg.Product, Platform: "windows", Architecture: "amd64", Channel: ChannelToolbox,
		Version: "0.9.0", MinimumSupportedVersion: "0.9.0", PublishedAt: time.Now().UTC(),
		Files: []SourceFile{
			{
				FileID: "file_runtime", Path: "vnt-runtime-manifest.json", Size: int64(len(body)),
				SHA256: fmt.Sprintf("%x", digest[:]), Compression: "none",
				ObjectKey: "toolbox/0.9.0/vnt-runtime-manifest.json",
			},
			{
				FileID: "file_toolbox", Path: "rebound_toolbox.exe", Size: 1,
				SHA256: repeatHex("a"), Compression: "none", ObjectKey: "toolbox/0.9.0/rebound_toolbox.exe",
			},
		},
	}
	resolved, err := service.ResolveVNTRuntime(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.VNTRuntime == nil || resolved.VNTRuntime.VNTSVersion != "1.2.12" ||
		resolved.VNTRuntime.WrapperVersion != "0.1.0" {
		t.Fatalf("resolved VNT runtime = %#v", resolved.VNTRuntime)
	}
	manifest, err := service.BuildAndSign(resolved)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.VerifySignedManifest(manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Files) != 1 || manifest.Files[0].Path != "rebound_toolbox.exe" {
		t.Fatalf("runtime attestation sidecar leaked into install files: %#v", manifest.Files)
	}

	tampered := source
	tampered.Files = append([]SourceFile(nil), source.Files...)
	tampered.Files[0].SHA256 = repeatHex("0")
	if _, err := service.ResolveVNTRuntime(context.Background(), tampered); err == nil {
		t.Fatal("runtime sidecar with a mismatched SHA-256 was accepted")
	}

	withoutSidecar := source
	withoutSidecar.Files = append([]SourceFile(nil), source.Files[1:]...)
	withoutSidecar.VNTRuntime = &VNTRuntimeRelease{VNTSVersion: "9.9.9", WrapperVersion: "9.9.9"}
	if _, err := service.ResolveVNTRuntime(context.Background(), withoutSidecar); err == nil {
		t.Fatal("caller-supplied runtime versions bypassed the required release sidecar")
	}
}

func TestToolboxManifestRequiresCanonicalUncompressedExecutable(t *testing.T) {
	cfg := testUpdateConfig(t)
	service, err := NewService(cfg, "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	base := SourceRelease{
		SchemaVersion: 1, Product: cfg.Product, Platform: "windows", Architecture: "amd64", Channel: ChannelToolbox,
		Version: "0.9.11", MinimumSupportedVersion: "0.9.11", PublishedAt: time.Now().UTC(),
		Files: []SourceFile{{
			FileID: "file_toolbox", Path: toolboxExecutablePath, Size: 1,
			SHA256: repeatHex("a"), Compression: "none", ObjectKey: "toolbox/0.9.11/Rebound_Toolbox.exe",
		}},
	}
	if _, err := service.BuildAndSign(base); err != nil {
		t.Fatalf("canonical toolbox manifest was rejected: %v", err)
	}

	for name, mutate := range map[string]func(*SourceRelease){
		"wrong path case": func(source *SourceRelease) { source.Files[0].Path = "Rebound_Toolbox.exe" },
		"compressed":      func(source *SourceRelease) { source.Files[0].Compression = "zstd" },
		"extra payload": func(source *SourceRelease) {
			source.Files = append(source.Files, SourceFile{
				FileID: "file_extra", Path: "extra.bin", Size: 1,
				SHA256: repeatHex("b"), Compression: "none", ObjectKey: "toolbox/0.9.11/extra.bin",
			})
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := base
			candidate.Files = append([]SourceFile(nil), base.Files...)
			mutate(&candidate)
			if _, err := service.BuildAndSign(candidate); err == nil {
				t.Fatal("invalid toolbox manifest was accepted")
			}
		})
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

func TestManagedReleaseUsesDownloadStoragePublicBaseURL(t *testing.T) {
	cfg := testUpdateConfig(t)
	service, err := NewService(cfg, "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	service.SetManagedReleaseURLs(
		" https://downloads.example.test/project-rebound-downloads/ ",
		"http://minio:9000/project-rebound-downloads",
	)
	manifest, err := service.BuildAndSign(SourceRelease{
		SchemaVersion: 1, Product: cfg.Product, Platform: "windows", Architecture: "amd64", Channel: "stable",
		Version: "1.0.0", MinimumSupportedVersion: "1.0.0", PublishedAt: time.Now().UTC(),
		Files: []SourceFile{{
			FileID: "file_a", Path: "game.exe", Size: 1, SHA256: repeatHex("a"), Compression: "none",
			ObjectKey: "downloads/client/dver_test/game.exe",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "https://downloads.example.test/project-rebound-downloads/downloads/client/dver_test/game.exe"
	if got := manifest.Files[0].DownloadURL; got != want {
		t.Fatalf("managed release download URL = %q, want %q", got, want)
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
