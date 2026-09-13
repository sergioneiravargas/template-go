package validation

import (
	"testing"
)

func TestValidateEmail(t *testing.T) {
	tests := []struct {
		name    string
		email   string
		wantErr bool
	}{
		{
			name:    "valid email",
			email:   "user@example.com",
			wantErr: false,
		},
		{
			name:    "valid email with subdomain",
			email:   "user@sub.example.com",
			wantErr: false,
		},
		{
			name:    "empty email",
			email:   "",
			wantErr: true,
		},
		{
			name:    "invalid email format",
			email:   "invalid-email",
			wantErr: true,
		},
		{
			name:    "email without domain",
			email:   "user@",
			wantErr: true,
		},
		{
			name:    "email without username",
			email:   "@example.com",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateEmail(tt.email); (err != nil) != tt.wantErr {
				t.Errorf("ValidateEmail() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidatePhone(t *testing.T) {
	tests := []struct {
		name    string
		phone   string
		wantErr bool
	}{
		{
			name:    "valid phone",
			phone:   "+1234567890",
			wantErr: false,
		},
		{
			name:    "valid phone without plus",
			phone:   "56912345678",
			wantErr: false,
		},
		{
			name:    "empty phone",
			phone:   "",
			wantErr: true,
		},
		{
			name:    "too short phone",
			phone:   "123",
			wantErr: true,
		},
		{
			name:    "too long phone",
			phone:   "12345678901234567",
			wantErr: true,
		},
		{
			name:    "phone with letters",
			phone:   "123456789a",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidatePhone(tt.phone); (err != nil) != tt.wantErr {
				t.Errorf("ValidatePhone() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateRUT(t *testing.T) {
	tests := []struct {
		name    string
		rut     string
		wantErr bool
	}{
		{"Valid RUT 1", "30686957-4", false},
		{"Valid RUT 2", "18593452-7", false}, // Corrected check digit
		{"Valid RUT K", "6-K", false},        // Corrected valid K RUT
		{"Valid RUT Clean", "306869574", false},
		{"Valid RUT Clean K", "6K", false},
		{"Valid RUT lowercase k", "6-k", false},
		{"Invalid RUT Check Digit", "30686957-5", true},
		{"Invalid RUT Format", "ABC-1", true},
		{"Invalid RUT Empty", "", true},
		{"Invalid RUT Short", "1", true},
		{"Invalid check digit calculation", "1-9", false},   // 1*2 = 2, 11 - 2 = 9. Correct.
		{"Invalid check digit calculation 2", "11-9", true}, // 1*2 + 1*3 = 5. 11-5=6. Want 6. Got 9.
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateRUT(tt.rut); (err != nil) != tt.wantErr {
				t.Errorf("ValidateRUT(%v) error = %v, wantErr %v", tt.rut, err, tt.wantErr)
			}
		})
	}
}
