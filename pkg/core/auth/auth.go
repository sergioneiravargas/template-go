package auth

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

var (
	ErrInvalidKeySet                 = errors.New("invalid keyset")
	ErrInvalidHeader                 = errors.New("invalid header")
	ErrTokenMalformed                = errors.New("token is malformed")
	ErrTokenExpired                  = errors.New("token is expired")
	ErrTokenNotValidYet              = errors.New("token is not valid yet")
	ErrTokenCouldNotBeParsed         = errors.New("token could not be parsed")
	ErrModulusCouldNotBeDecoded      = errors.New("modulus could not be decoded")
	ErrExponentCouldNotBeDecoded     = errors.New("exponent could not be decoded")
	ErrInvalidToken                  = errors.New("invalid token")
	ErrInvalidTokenClaims            = errors.New("invalid token claims")
	ErrRSAPublicKeyCouldNotBeDecoded = errors.New("rsa public key could not be decoded")
)

// JSON Web Token (JWT)
type Token = jwt.Token

// JWT Map Claims
type MapClaims = jwt.MapClaims

// JSON Web Key (JWK)
type Key struct {
	Kid string `json:"kid"`
	Alg string `json:"alg"`
	Kty string `json:"kty"`
	E   string `json:"e"`
	N   string `json:"n"`
	Use string `json:"use"`
}

// JSON Web Key Set (JWKS)
type KeySet struct {
	Keys []Key `json:"keys"`
}

// The user information contained in the OIDC claims
type UserInfo struct {
	ID            string `json:"sub"`
	GivenName     string `json:"given_name"`
	FamilyName    string `json:"family_name"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
}

func UserInfoFromClaims(claims MapClaims) (*UserInfo, error) {
	var userInfo UserInfo

	sub, valid := claims["sub"].(string)
	if !valid {
		return nil, fmt.Errorf("invalid claim sub")
	}
	userInfo.ID = sub

	givenName, valid := claims["given_name"].(string)
	if valid {
		userInfo.GivenName = givenName
	}

	familyName, valid := claims["family_name"].(string)
	if valid {
		userInfo.FamilyName = familyName
	}

	email, valid := claims["email"].(string)
	if valid {
		userInfo.Email = email
	}

	emailVerified, _ := claims["email_verified"].(bool)
	if valid {
		userInfo.EmailVerified = emailVerified
	}

	return &userInfo, nil
}

// Fetches UserInfo from the given URL
func FetchUserInfo(
	url string,
	accessToken string,
) (*UserInfo, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Add("Authorization", "Bearer "+accessToken)

	httpClient := &http.Client{}
	res, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}

	var userInfo UserInfo
	if err := json.Unmarshal(body, &userInfo); err != nil {
		return nil, err
	}

	return &userInfo, nil
}

// Fetches the key set from the given URL
func FetchKeySet(url string) (KeySet, error) {
	res, err := http.Get(url)
	if err != nil {
		return KeySet{}, err
	}
	defer res.Body.Close()

	var keySet KeySet
	if err := json.NewDecoder(res.Body).Decode(&keySet); err != nil {
		return KeySet{}, err
	}

	return keySet, nil
}

// Extracts the token from the given header
func TokenFromHeader(header string) (string, error) {
	if len(header) > 7 && strings.ToUpper(header[0:6]) == "BEARER" {
		token := strings.Clone(header[7:])

		return token, nil
	}

	return "", ErrInvalidHeader
}

func ParseTokenWithPEM(token string, key *rsa.PublicKey) (*Token, error) {
	parsedToken, err := jwt.Parse(
		token,
		func(t *Token) (any, error) {
			return key, nil
		},
	)
	if err != nil {
		if errors.Is(err, jwt.ErrTokenMalformed) {
			return nil, ErrTokenMalformed
		} else if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, ErrTokenExpired
		} else if errors.Is(err, jwt.ErrTokenNotValidYet) {
			return nil, ErrTokenNotValidYet
		}

		return nil, ErrTokenCouldNotBeParsed
	}

	return parsedToken, nil
}

// Parses the token using the given JWKS
func ParseTokenWithJWKS(token string, keySet KeySet) (*Token, error) {
	parsedToken, err := jwt.Parse(
		token,
		func(t *Token) (any, error) {
			for _, key := range keySet.Keys {
				if key.Kid != t.Header["kid"] {
					continue
				}

				rsa, err := LoadPublicKeyFromJKWS(key)
				if err != nil {
					return nil, ErrRSAPublicKeyCouldNotBeDecoded
				}

				return rsa, nil
			}

			return nil, ErrInvalidKeySet
		},
	)
	if err != nil {
		if errors.Is(err, jwt.ErrTokenMalformed) {
			return nil, ErrTokenMalformed
		} else if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, ErrTokenExpired
		} else if errors.Is(err, jwt.ErrTokenNotValidYet) {
			return nil, ErrTokenNotValidYet
		}

		return nil, ErrTokenCouldNotBeParsed
	}

	return parsedToken, nil
}

// Extracts the RSA public key from the given JWK
func LoadPublicKeyFromJKWS(key Key) (*rsa.PublicKey, error) {
	nb, err := base64.RawURLEncoding.DecodeString(key.N)
	if err != nil {
		return nil, ErrModulusCouldNotBeDecoded
	}

	eb, err := base64.RawURLEncoding.DecodeString(key.E)
	if err != nil {
		return nil, ErrExponentCouldNotBeDecoded
	}

	return &rsa.PublicKey{
		N: big.NewInt(0).SetBytes(nb),
		E: int(big.NewInt(0).SetBytes(eb).Int64()),
	}, nil
}

type ctxKey uint

const (
	tokenCtxKey ctxKey = iota
	tokenClaimsCtxKey
	userInfoCtxKey
)

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

// Loads the RSA private key from the given data
func LoadPrivateKeyFromPEM(data []byte) (*rsa.PrivateKey, error) {
	privateKey, err := jwt.ParseRSAPrivateKeyFromPEM(data)
	if err != nil {
		return nil, err
	}

	return privateKey, nil
}

// Loads the RSA public key from the given data
func LoadPublicKeyFromPEM(data []byte) (*rsa.PublicKey, error) {
	publicKey, err := jwt.ParseRSAPublicKeyFromPEM(data)
	if err != nil {
		return nil, err
	}

	return publicKey, nil
}

// Generates a JWT token with the given claims
func GenerateToken(claims MapClaims, key *rsa.PrivateKey) (string, error) {
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tokenString, err := token.SignedString(key)
	if err != nil {
		return "", err
	}

	return tokenString, nil
}
