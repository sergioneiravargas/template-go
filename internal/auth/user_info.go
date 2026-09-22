package auth

// User information exposed to handlers through the request context
type UserInfo struct {
	ID            string `json:"sub"`
	GivenName     string `json:"given_name"`
	FamilyName    string `json:"family_name"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
}
