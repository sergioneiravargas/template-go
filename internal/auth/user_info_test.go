package auth

import "testing"

func TestUserInfoFromClaims(t *testing.T) {
	tests := []struct {
		name    string
		claims  MapClaims
		want    UserInfo
		wantErr bool
	}{
		{
			name: "full claims",
			claims: MapClaims{
				"sub":            "user-1",
				"given_name":     "Ada",
				"family_name":    "Lovelace",
				"email":          "ada@example.com",
				"email_verified": true,
			},
			want: UserInfo{ID: "user-1", GivenName: "Ada", FamilyName: "Lovelace", Email: "ada@example.com", EmailVerified: true},
		},
		{
			name:    "missing sub",
			claims:  MapClaims{"email": "ada@example.com"},
			wantErr: true,
		},
		{
			name:   "email_verified without email",
			claims: MapClaims{"sub": "user-2", "email_verified": true},
			want:   UserInfo{ID: "user-2", EmailVerified: true},
		},
		{
			name:   "email_verified absent",
			claims: MapClaims{"sub": "user-3", "email": "x@example.com"},
			want:   UserInfo{ID: "user-3", Email: "x@example.com"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := UserInfoFromClaims(tt.claims)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if *got != tt.want {
				t.Fatalf("got %+v, want %+v", *got, tt.want)
			}
		})
	}
}
