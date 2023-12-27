package jwt

import (
	"errors"
	"net/http"
)

func Middleware(
	service *Service,
) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(
			func(w http.ResponseWriter, r *http.Request) {
				header := r.Header.Get("Authorization")
				if header == "" {
					http.Error(w, "missing JWT token", http.StatusUnauthorized)
					return
				}

				token, err := TokenFromHeader(header)
				if err != nil {
					http.Error(w, "invalid JWT token", http.StatusUnauthorized)
					return
				}

				if err = service.ValidateToken(token); err != nil {
					if errors.Is(err, ErrTokenExpired) {
						http.Error(w, "expired JWT token", http.StatusUnauthorized)
					} else if errors.Is(err, ErrTokenNotValidYet) {
						http.Error(w, "JWT token is not valid yet", http.StatusUnauthorized)
					} else {
						http.Error(w, "invalid JWT token", http.StatusUnauthorized)
					}
					return
				}

				next.ServeHTTP(w, r)
			},
		)
	}
}
