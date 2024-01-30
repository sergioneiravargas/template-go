package jwt

import (
	"context"
	"errors"
	"net/http"
)

type ctxKey string

const (
	TokenKey       ctxKey = "jwtToken"
	TokenClaimsKey ctxKey = "jwtTokenClaims"
)

func Middleware(
	service *Service,
) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(
			func(w http.ResponseWriter, r *http.Request) {
				header := r.Header.Get("Authorization")
				if header == "" {
					http.Error(w, "Missing JWT token", http.StatusUnauthorized)
					return
				}

				token, err := TokenFromHeader(header)
				if err != nil {
					http.Error(w, "Invalid JWT token", http.StatusUnauthorized)
					return
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

				// Add token to request context
				r = r.WithContext(
					context.WithValue(
						r.Context(),
						TokenKey,
						token,
					),
				)

				// Add token claims to request context
				claims, err := service.TokenClaims(token)
				if err != nil {
					http.Error(w, "Invalid JWT token", http.StatusUnauthorized)
					return
				}
				r = r.WithContext(
					context.WithValue(
						r.Context(),
						TokenClaimsKey,
						claims,
					),
				)

				next.ServeHTTP(w, r)
			},
		)
	}
}
