package metaserver

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpstreamProtocolMirrorMatchesManifest(t *testing.T) {
	root := filepath.Join("..", "..", "api", "proto", "metaserver")
	manifestFile, err := os.Open(filepath.Join(root, "UPSTREAM_MANIFEST.sha256"))
	if err != nil {
		t.Fatal(err)
	}
	defer manifestFile.Close()
	expected := make(map[string]string)
	scanner := bufio.NewScanner(manifestFile)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 || len(fields[0]) != 64 ||
			filepath.IsAbs(fields[1]) || strings.Contains(fields[1], "..") {
			t.Fatalf("invalid upstream manifest line %q", scanner.Text())
		}
		expected[filepath.ToSlash(fields[1])] = fields[0]
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if len(expected) != 41 {
		t.Fatalf("upstream manifest contains %d files, want 41", len(expected))
	}
	actual := make(map[string]string)
	err = filepath.WalkDir(filepath.Join(root, "upstream"), func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(filepath.Join(root, "upstream"), path)
		if err != nil {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		// The manifest records canonical LF bytes so the provenance check has
		// identical semantics on Windows and Linux worktrees.
		raw = bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n"))
		digest := sha256.Sum256(raw)
		actual[filepath.ToSlash(relative)] = hex.EncodeToString(digest[:])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(actual) != len(expected) {
		t.Fatalf("upstream mirror contains %d files, manifest contains %d", len(actual), len(expected))
	}
	for name, expectedDigest := range expected {
		if actual[name] != expectedDigest {
			t.Errorf("upstream protocol drift for %s: got %s, want %s", name, actual[name], expectedDigest)
		}
	}
}
