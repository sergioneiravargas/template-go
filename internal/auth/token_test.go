package auth

import "testing"

func TestGenerateRefreshToken(t *testing.T) {
	token, hash, err := GenerateRefreshToken()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(token) != 43 {
		t.Fatalf("expected 43-char base64url token, got %d chars", len(token))
	}
	if hash != HashRefreshToken(token) {
		t.Fatal("returned hash does not match HashRefreshToken")
	}
	if len(hash) != 64 {
		t.Fatalf("expected 64-char sha256 hex, got %d", len(hash))
	}

	token2, _, err := GenerateRefreshToken()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token == token2 {
		t.Fatal("expected unique tokens")
	}
}
