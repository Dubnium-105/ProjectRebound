package relayregistry

import (
	"bytes"
	"testing"
)

func TestParseBootstrapCredentials(t *testing.T) {
	credentials, err := ParseBootstrapCredentials(
		"relay-a=01234567890123456789012345678901;relay-b=abcdefghijklmnopqrstuvwxyzABCDEF",
	)
	if err != nil || len(credentials) != 2 {
		t.Fatalf("credentials = %#v, %v", credentials, err)
	}
	if credentials[0].ID != "relay-a" || !bytes.Equal(credentials[0].Hash, hashToken("01234567890123456789012345678901")) {
		t.Fatalf("unexpected first credential: %#v", credentials[0])
	}
	if bytes.Contains(credentials[0].Hash, []byte("01234567890123456789012345678901")) {
		t.Fatal("bootstrap credential retained plaintext")
	}
}

func TestParseBootstrapCredentialsRejectsUnsafeSets(t *testing.T) {
	for _, value := range []string{
		"missing-separator",
		"relay=short",
		"relay=01234567890123456789012345678901;relay=abcdefghijklmnopqrstuvwxyzABCDEF",
	} {
		if _, err := ParseBootstrapCredentials(value); err == nil {
			t.Fatalf("ParseBootstrapCredentials(%q) accepted an unsafe set", value)
		}
	}
}
