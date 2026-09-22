package auth

import "testing"

func TestRegisterInput_Validate(t *testing.T) {
	valid := RegisterInput{Email: "ada@example.com", Password: "long-enough-password", GivenName: "Ada"}
	tests := []struct {
		name    string
		mutate  func(*RegisterInput)
		wantErr bool
	}{
		{"valid", func(i *RegisterInput) {}, false},
		{"empty email", func(i *RegisterInput) { i.Email = "" }, true},
		{"malformed email", func(i *RegisterInput) { i.Email = "not-an-email" }, true},
		{"short password", func(i *RegisterInput) { i.Password = "seven77" }, true},
		{"long password", func(i *RegisterInput) { i.Password = string(make([]byte, 513)) }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := valid
			tt.mutate(&input)
			if err := input.Validate(); (err != nil) != tt.wantErr {
				t.Fatalf("wantErr=%t, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestLoginInput_Validate(t *testing.T) {
	if err := (LoginInput{Email: "ada@example.com", Password: "x"}).Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := (LoginInput{Email: "ada@example.com"}).Validate(); err == nil {
		t.Fatal("expected error for empty password")
	}
	if err := (LoginInput{Email: "bad", Password: "x"}).Validate(); err == nil {
		t.Fatal("expected error for malformed email")
	}
}

func TestRefreshAndLogoutInput_Validate(t *testing.T) {
	if err := (RefreshInput{RefreshToken: "tok"}).Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := (RefreshInput{}).Validate(); err == nil {
		t.Fatal("expected error for empty refresh token")
	}
	if err := (LogoutInput{RefreshToken: "tok"}).Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := (LogoutInput{}).Validate(); err == nil {
		t.Fatal("expected error for empty refresh token")
	}
}
