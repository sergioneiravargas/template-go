package auth

import (
	"errors"
	"net/http"
)

// Middleware for JWT based user authentication
func Middleware(
	service *Service,
) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(
			func(w http.ResponseWriter, r *http.Request) {
				var err error
				var token string

				header := r.Header.Get("Authorization")
				if header != "" {
					headerToken, err := TokenFromHeader(header)
					if err == nil {
						token = headerToken
					}
				}

				if token == "" {
					token, err = TokenFromQueryParam(r)
					if err != nil {
						http.Error(w, "Missing JWT token", http.StatusUnauthorized)
						return
					}
				}

				if err = service.ValidateToken(token); err != nil {
					if errors.Is(err, ErrTokenExpired) {
						http.Error(w, "Expired JWT token", http.StatusUnauthorized)
					} else if errors.Is(err, ErrTokenNotValidYet) {
						http.Error(w, "JWT token is not valid yet", http.StatusUnauthorized)
					} else {
						http.Error(w, "Invalid JWT token", http.StatusUnauthorized)
					}
					return
				}

				// Add the access token to the request's context
				r = RequestWithToken(r, token)

				// Add the token claims to the request's context
				claims, err := service.TokenClaims(token)
				if err != nil {
					http.Error(w, "Invalid JWT token", http.StatusUnauthorized)
					return
				}
				r = RequestWithTokenClaims(r, claims)

				// Add the user information to the request's context
				userInfo, err := service.UserInfo(token)
				if err != nil {
					http.Error(w, "Internal server error", http.StatusInternalServerError)
					return
				}
				r = RequestWithUserInfo(r, *userInfo)

				next.ServeHTTP(w, r)
			},
		)
	}
}
