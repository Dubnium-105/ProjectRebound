package gameserver

import (
	"testing"
)

func TestServerTokenHasHighEntropyHash(t *testing.T) {
	token, hash, err := newServerToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(token) < 64 || len(hash) != 32 || string(hash) != string(hashServerToken(token)) {
		t.Fatalf("unexpected token/hash shape")
	}
}
