package metaserver

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSanitizedGoldenNativePackets(t *testing.T) {
	expected := map[string]string{
		"gate.hex":        "mgt_REDACTED",
		"profile.hex":     "/assets.Assets/GetPlayerArchiveV2",
		"party.hex":       "/party.party/Create",
		"matchmaking.hex": "/matchmaking.Matchmaking/StartUnityMatchmaking",
	}
	for name, path := range expected {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("testdata", "golden", name))
			if err != nil {
				t.Fatal(err)
			}
			packet, err := hex.DecodeString(strings.TrimSpace(string(raw)))
			if err != nil {
				t.Fatal(err)
			}
			wrapper, err := DecodeRequestWrapper(packet)
			if err != nil {
				t.Fatal(err)
			}
			if wrapper.RPCPath != path {
				t.Fatalf("got %q, want %q", wrapper.RPCPath, path)
			}
		})
	}
}
