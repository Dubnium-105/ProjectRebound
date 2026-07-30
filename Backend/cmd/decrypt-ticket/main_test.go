package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeTicketLibrary struct {
	decrypted  []byte
	decryptErr error
	steamID    uint64
	appID      uint32
	issueTime  uint32
	vacBanned  bool
	closed     bool
	seenKey    []byte
	seenTicket []byte
}

func (f *fakeTicketLibrary) Decrypt(ticket []byte, key []byte) ([]byte, error) {
	f.seenTicket = append([]byte(nil), ticket...)
	f.seenKey = append([]byte(nil), key...)
	if f.decryptErr != nil {
		return nil, f.decryptErr
	}
	return append([]byte(nil), f.decrypted...), nil
}

func (f *fakeTicketLibrary) SteamID([]byte) (uint64, error) {
	return f.steamID, nil
}

func (f *fakeTicketLibrary) AppID([]byte) (uint32, error) {
	return f.appID, nil
}

func (f *fakeTicketLibrary) IssueTime([]byte) (uint32, error) {
	return f.issueTime, nil
}

func (f *fakeTicketLibrary) VACBanned([]byte) (bool, error) {
	return f.vacBanned, nil
}

func (f *fakeTicketLibrary) Close() error {
	f.closed = true
	return nil
}

func TestRunVerifiesTicketThroughNativeLibrary(t *testing.T) {
	keyHex := strings.Repeat("ab", symmetricKeyBytes)
	library := &fakeTicketLibrary{
		decrypted: []byte{0x10, 0x20}, steamID: 76561198000000001,
		appID: 480, issueTime: 1_785_412_800, vacBanned: false,
	}
	var stdout, stderr bytes.Buffer
	exitCode := run(
		strings.NewReader("0102aB\n"),
		&stdout,
		&stderr,
		mapLookup(map[string]string{
			"STEAM_ENCRYPTED_APP_TICKET_KEY_HEX": keyHex,
			"STEAM_ENCRYPTED_APP_TICKET_LIBRARY": "test-library",
		}),
		func(path string) (nativeTicketLibrary, error) {
			if path != "test-library" {
				t.Fatalf("library path = %q", path)
			}
			return library, nil
		},
	)
	if exitCode != 0 || stderr.Len() != 0 {
		t.Fatalf("exit=%d stderr=%s", exitCode, stderr.String())
	}
	var output verifierOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	if !output.Valid || output.SteamID != "76561198000000001" ||
		output.AppID != 480 || output.IssueTime != 1_785_412_800 ||
		output.VACBanned {
		t.Fatalf("output=%+v", output)
	}
	if !bytes.Equal(library.seenTicket, []byte{0x01, 0x02, 0xab}) ||
		!bytes.Equal(library.seenKey, bytes.Repeat([]byte{0xab}, symmetricKeyBytes)) ||
		!library.closed {
		t.Fatalf("native call ticket=%x key_length=%d closed=%v", library.seenTicket, len(library.seenKey), library.closed)
	}
}

func TestRunRejectsMalformedInputWithoutLoadingLibrary(t *testing.T) {
	tests := []string{"", "0", "not-hex", strings.Repeat("ab", maximumTicketHexCharacters/2+1)}
	for _, input := range tests {
		t.Run(inputName(input), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			loaded := false
			exitCode := run(
				strings.NewReader(input),
				&stdout,
				&stderr,
				mapLookup(map[string]string{
					"STEAM_ENCRYPTED_APP_TICKET_KEY_HEX": strings.Repeat("ab", symmetricKeyBytes),
				}),
				func(string) (nativeTicketLibrary, error) {
					loaded = true
					return &fakeTicketLibrary{}, nil
				},
			)
			if exitCode != 1 || loaded || stdout.Len() != 0 ||
				stderr.String() != "{\"error\":\"invalid_ticket\"}\n" {
				t.Fatalf("exit=%d loaded=%v stdout=%q stderr=%q", exitCode, loaded, stdout.String(), stderr.String())
			}
		})
	}
}

func TestRunDoesNotLeakTicketWhenDecryptFails(t *testing.T) {
	const ticket = "deadbeef"
	var stdout, stderr bytes.Buffer
	exitCode := run(
		strings.NewReader(ticket),
		&stdout,
		&stderr,
		mapLookup(map[string]string{
			"STEAM_ENCRYPTED_APP_TICKET_KEY_HEX": strings.Repeat("ab", symmetricKeyBytes),
		}),
		func(string) (nativeTicketLibrary, error) {
			return &fakeTicketLibrary{decryptErr: errors.New("native failure")}, nil
		},
	)
	if exitCode != 1 || strings.Contains(stderr.String(), ticket) ||
		stderr.String() != "{\"error\":\"invalid_ticket\"}\n" {
		t.Fatalf("exit=%d stderr=%q", exitCode, stderr.String())
	}
}

func TestLoadSymmetricKeyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ticket-key")
	if err := os.WriteFile(path, bytes.Repeat([]byte{0x5a}, symmetricKeyBytes), 0o600); err != nil {
		t.Fatal(err)
	}
	key, err := loadSymmetricKey(mapLookup(map[string]string{
		"STEAM_ENCRYPTED_APP_TICKET_KEY_FILE": path,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(key, bytes.Repeat([]byte{0x5a}, symmetricKeyBytes)) {
		t.Fatalf("key length=%d", len(key))
	}
}

func TestLoadSymmetricKeyRequiresExactlyOneSource(t *testing.T) {
	if _, err := loadSymmetricKey(mapLookup(nil)); err == nil {
		t.Fatal("missing key configuration was accepted")
	}
	if _, err := loadSymmetricKey(mapLookup(map[string]string{
		"STEAM_ENCRYPTED_APP_TICKET_KEY_HEX":  strings.Repeat("ab", symmetricKeyBytes),
		"STEAM_ENCRYPTED_APP_TICKET_KEY_FILE": "key-file",
	})); err == nil {
		t.Fatal("ambiguous key configuration was accepted")
	}
}

func mapLookup(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

func inputName(value string) string {
	if value == "" {
		return "empty"
	}
	if len(value) > 32 {
		return "too_long"
	}
	return value
}
