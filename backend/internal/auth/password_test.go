package auth

import (
	"strings"
	"testing"
)

func TestPasswordRoundTrip(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(hash, "correct horse battery staple") {
		t.Fatal("valid password was rejected")
	}
	if VerifyPassword(hash, "not the password") {
		t.Fatal("invalid password was accepted")
	}
}

func TestUnicodePasswordPolicy(t *testing.T) {
	password := strings.Repeat("星", 12)
	hash, err := HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(hash, password) {
		t.Fatal("valid Unicode password was rejected")
	}
	if _, err := HashPassword(strings.Repeat("星", 129)); err == nil {
		t.Fatal("password exceeding the character limit was accepted")
	}
}

func TestVerifyPasswordRejectsHostileHashParameters(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	hostile := strings.Replace(hash, "m=19456", "m=4294967295", 1)
	if VerifyPassword(hostile, "correct horse battery staple") {
		t.Fatal("hash with unexpected resource parameters was accepted")
	}
	if VerifyPassword(hash, strings.Repeat("x", MaximumPasswordBytes+1)) {
		t.Fatal("oversized password candidate was accepted")
	}
}

func TestTokenHash(t *testing.T) {
	plain, hash, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if plain == "" || string(hash) != string(HashToken(plain)) {
		t.Fatal("token hash mismatch")
	}
}
