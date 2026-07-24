package admin

import (
	"encoding/base64"
	"testing"
	"time"
)

func TestTOTPMatchesRFC6238Vector(t *testing.T) {
	const secret = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	at := time.Unix(59, 0)
	if code := totpCode(secret, at.Unix()/totpPeriodSeconds); code != "287082" {
		t.Fatalf("TOTP code = %q", code)
	}
	if !ValidateTOTP(secret, "287082", at) {
		t.Fatal("valid TOTP code was rejected")
	}
	if ValidateTOTP(secret, "287083", at) {
		t.Fatal("invalid TOTP code was accepted")
	}
}

func TestSecretBoxUsesAdministratorAsAssociatedData(t *testing.T) {
	key := make([]byte, 32)
	for index := range key {
		key[index] = byte(index + 1)
	}
	box, ephemeral, err := NewSecretBox(base64.StdEncoding.EncodeToString(key), "production")
	if err != nil {
		t.Fatal(err)
	}
	if ephemeral {
		t.Fatal("configured key was marked ephemeral")
	}
	ciphertext, err := box.Encrypt("adm_one", "TOTPSECRET")
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := box.Decrypt("adm_one", ciphertext)
	if err != nil || plaintext != "TOTPSECRET" {
		t.Fatalf("decrypt = %q, %v", plaintext, err)
	}
	if _, err := box.Decrypt("adm_two", ciphertext); err == nil {
		t.Fatal("ciphertext decrypted for a different administrator")
	}
}

func TestRecoveryCodesAreUniqueAndHashed(t *testing.T) {
	codes, hashes, err := NewRecoveryCodes(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(codes) != 10 || len(hashes) != 10 {
		t.Fatalf("codes/hashes = %d/%d", len(codes), len(hashes))
	}
	seen := make(map[string]struct{})
	for index, code := range codes {
		if _, exists := seen[code]; exists {
			t.Fatalf("duplicate recovery code %q", code)
		}
		seen[code] = struct{}{}
		if string(hashes[index]) != string(HashRecoveryCode(code)) {
			t.Fatalf("recovery code hash %d does not match", index)
		}
	}
}
