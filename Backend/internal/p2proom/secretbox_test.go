package p2proom

import (
	"encoding/base64"
	"testing"
)

func TestSecretBoxBindsCiphertextToRoomGenerationNodeAndKind(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	box, ephemeral, err := NewSecretBox(key, "production")
	if err != nil {
		t.Fatal(err)
	}
	if ephemeral {
		t.Fatal("configured key reported as ephemeral")
	}
	aad := vntSecretAAD("room_one", 1, "vnt_one", "network_token")
	ciphertext, nonce, _, err := box.Seal([]byte("secret"), aad)
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := box.Open(ciphertext, nonce, aad)
	if err != nil {
		t.Fatal(err)
	}
	if string(plaintext) != "secret" {
		t.Fatalf("plaintext = %q", plaintext)
	}
	if _, err := box.Open(ciphertext, nonce, vntSecretAAD("room_one", 2, "vnt_one", "network_token")); err == nil {
		t.Fatal("ciphertext decrypted for a different generation")
	}
}

func TestSecretBoxRequiresConfiguredProductionKey(t *testing.T) {
	if _, _, err := NewSecretBox("", "production"); err == nil {
		t.Fatal("missing production key was accepted")
	}
}
