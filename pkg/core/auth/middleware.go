package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
)

type ctxKey string

const (
	tokenCtxKey       ctxKey = "auth_token"
	tokenClaimsCtxKey ctxKey = "auth_token_claims"
	userInfoCtxKey    ctxKey = "auth_user_info"
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

// Extracts the token from the given header
func TokenFromHeader(header string) (string, error) {
	if len(header) > 7 && strings.ToUpper(header[0:6]) == "BEARER" {
		token := strings.Clone(header[7:])

		return token, nil
	}

	return "", ErrInvalidHeader
}

// Extracts the token from the given URL query parameter
func TokenFromQueryParam(r *http.Request) (string, error) {
	// get token from url query param specified
	token := r.URL.Query().Get("access_token")
	if token == "" {
		return "", ErrInvalidToken
	}

	return token, nil
}

// Returns a shallow copy of the request with the given token in its context
func RequestWithToken(r *http.Request, token string) *http.Request {
	return r.WithContext(
		context.WithValue(
			r.Context(),
			tokenCtxKey,
			token,
		),
	)
}

// Extracts the token from the given request's context
func TokenFromRequest(r *http.Request) (string, bool) {
	token, valid := r.Context().Value(tokenCtxKey).(string)
	if !valid {
		return "", false
	}

	return token, true
}

// Returns a shallow copy of the request with the given token claims in its context
func RequestWithTokenClaims(r *http.Request, claims MapClaims) *http.Request {
	return r.WithContext(
		context.WithValue(
			r.Context(),
			tokenClaimsCtxKey,
			claims,
		),
	)
}

// Extracts the token claims from the given request's context
func TokenClaimsFromRequest(r *http.Request) (MapClaims, bool) {
	claims, valid := r.Context().Value(tokenClaimsCtxKey).(MapClaims)
	if !valid {
		return nil, false
	}

	return claims, true
}

// Returns a shallow copy of the request with the given user information in its context
func RequestWithUserInfo(r *http.Request, userInfo UserInfo) *http.Request {
	return r.WithContext(
		context.WithValue(
			r.Context(),
			userInfoCtxKey,
			userInfo,
		),
	)
}

// Extracts the user information from the given request's context
func UserInfoFromRequest(r *http.Request) (UserInfo, bool) {
	userInfo, valid := r.Context().Value(userInfoCtxKey).(UserInfo)
	if !valid {
		return UserInfo{}, false
	}

	return userInfo, true
}
