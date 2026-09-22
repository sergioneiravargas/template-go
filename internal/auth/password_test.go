package auth

import (
	"errors"
	"strings"
	"testing"
)

func TestHashAndVerifyPassword(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=65536,t=3,p=1$") {
		t.Fatalf("unexpected hash format: %s", hash)
	}

	ok, err := VerifyPassword(hash, "correct horse battery staple")
	if err != nil || !ok {
		t.Fatalf("expected match, got ok=%t err=%v", ok, err)
	}

	ok, err = VerifyPassword(hash, "wrong password")
	if err != nil || ok {
		t.Fatalf("expected mismatch, got ok=%t err=%v", ok, err)
	}
}

func TestHashPassword_UniqueSalts(t *testing.T) {
	a, err := HashPassword("password-123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, err := HashPassword("password-123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a == b {
		t.Fatal("expected different hashes for the same password")
	}
}

func TestVerifyPassword_MalformedHash(t *testing.T) {
	tests := []struct{ name, hash string }{
		{"empty", ""},
		{"not phc", "plaintext"},
		{"wrong algorithm", "$bcrypt$v=19$m=65536,t=3,p=1$c2FsdA$aGFzaA"},
		{"bad params", "$argon2id$v=19$m=abc$c2FsdA$aGFzaA"},
		{"bad salt encoding", "$argon2id$v=19$m=65536,t=3,p=1$!!!$aGFzaA"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := VerifyPassword(tt.hash, "password"); !errors.Is(err, ErrInvalidPasswordHash) {
				t.Fatalf("expected ErrInvalidPasswordHash, got %v", err)
			}
		})
	}
}
