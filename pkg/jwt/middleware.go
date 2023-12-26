package jwt

import (
	"net/http"
)

func NewMiddleware(
	service *Service,
) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(
			func(w http.ResponseWriter, r *http.Request) {
				header := r.Header.Get("Authorization")
				if header == "" {
					http.Error(w, "missing JWT", http.StatusUnauthorized)
					return
				}

				token, err := TokenFromHeader(header)
				if err != nil {
					http.Error(w, "invalid header", http.StatusUnauthorized)
					return
				}

				if err = service.ValidateToken(token); err != nil {
					http.Error(w, "invalid JWT", http.StatusUnauthorized)
					return
				}

				next.ServeHTTP(w, r)
			},
		)
	}
}
