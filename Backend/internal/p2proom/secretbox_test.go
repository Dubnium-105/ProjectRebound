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
	ciphertext, nonce, keyID, err := box.Seal([]byte("secret"), aad)
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := box.Open(ciphertext, nonce, aad, keyID)
	if err != nil {
		t.Fatal(err)
	}
	if string(plaintext) != "secret" {
		t.Fatalf("plaintext = %q", plaintext)
	}
	if _, err := box.Open(ciphertext, nonce, vntSecretAAD("room_one", 2, "vnt_one", "network_token"), keyID); err == nil {
		t.Fatal("ciphertext decrypted for a different generation")
	}
}

func TestSecretBoxKeyringDecryptsPreviousKeyAndWritesActiveKey(t *testing.T) {
	oldKey := base64.StdEncoding.EncodeToString(bytesOf(1, 32))
	newKey := base64.StdEncoding.EncodeToString(bytesOf(2, 32))
	oldBox, _, err := NewSecretBoxKeyring(oldKey, "vnt-room-old", "", "production")
	if err != nil {
		t.Fatal(err)
	}
	aad := []byte("room-generation-node-kind")
	ciphertext, nonce, oldKeyID, err := oldBox.Seal([]byte("persisted-secret"), aad)
	if err != nil {
		t.Fatal(err)
	}
	rotated, _, err := NewSecretBoxKeyring(newKey, "vnt-room-new", "vnt-room-old="+oldKey, "production")
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := rotated.Open(ciphertext, nonce, aad, oldKeyID)
	if err != nil || string(plaintext) != "persisted-secret" {
		t.Fatalf("old-key decrypt = %q, %v", plaintext, err)
	}
	_, _, writtenKeyID, err := rotated.Seal([]byte("new-secret"), aad)
	if err != nil || writtenKeyID != "vnt-room-new" {
		t.Fatalf("active write key = %q, %v", writtenKeyID, err)
	}
	if _, err := rotated.Open(ciphertext, nonce, aad, "unknown-key"); err == nil {
		t.Fatal("unknown secret key ID was accepted")
	}
}

func bytesOf(value byte, count int) []byte {
	result := make([]byte, count)
	for index := range result {
		result[index] = value
	}
	return result
}

func TestSecretBoxRequiresConfiguredProductionKey(t *testing.T) {
	if _, _, err := NewSecretBox("", "production"); err == nil {
		t.Fatal("missing production key was accepted")
	}
}
