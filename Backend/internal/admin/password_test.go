package admin

import (
	"strings"
	"testing"
)

func TestPasswordHashRoundTrip(t *testing.T) {
	encoded, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(encoded, "$argon2id$") {
		t.Fatalf("password hash = %q", encoded)
	}
	if !VerifyPassword(encoded, "correct horse battery staple") {
		t.Fatal("correct password was rejected")
	}
	if VerifyPassword(encoded, "wrong password") {
		t.Fatal("wrong password was accepted")
	}
	if VerifyPassword("$argon2id$malformed", "correct horse battery staple") {
		t.Fatal("malformed password hash was accepted")
	}
}

func TestPasswordPolicy(t *testing.T) {
	if _, err := HashPassword("too-short"); err == nil {
		t.Fatal("short password was accepted")
	}
	if _, err := HashPassword(strings.Repeat("x", 257)); err == nil {
		t.Fatal("overlong password was accepted")
	}
}
