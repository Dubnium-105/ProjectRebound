package auth

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestValidateSteamID(t *testing.T) {
	if err := ValidateSteamID("76561198950613585"); err != nil {
		t.Fatalf("valid SteamID rejected: %v", err)
	}
	for _, value := range []string{"", "7656119 950613585", "123456789012345", "123456789012345678901", "7656119895061358x"} {
		if err := ValidateSteamID(value); err == nil {
			t.Errorf("invalid SteamID %q accepted", value)
		}
	}
}

func TestNormalizePersonaName(t *testing.T) {
	input := " \tA\u0000lice " + strings.Repeat("界", 70)
	value, err := NormalizePersonaName(input, "Player")
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsRune(value, '\x00') || utf8.RuneCountInString(value) != 64 {
		t.Fatalf("normalized value = %q (%d runes)", value, utf8.RuneCountInString(value))
	}
	fallback, err := NormalizePersonaName("\n\t", "Player")
	if err != nil || fallback != "Player" {
		t.Fatalf("fallback = %q, %v", fallback, err)
	}
}
