package auth

import "testing"

func TestDevelopmentTrustedSteamIDRequiresExactAllowlistMatch(t *testing.T) {
	allowlist := []string{"76561198000000001", "76561198000000002"}
	if !developmentTrustedSteamID(allowlist, "76561198000000002") {
		t.Fatal("exact allowlisted SteamID was rejected")
	}
	if developmentTrustedSteamID(allowlist, "76561198000000003") {
		t.Fatal("non-allowlisted SteamID was trusted")
	}
	if developmentTrustedSteamID(allowlist, " 76561198000000002") {
		t.Fatal("non-canonical SteamID was trusted")
	}
}
