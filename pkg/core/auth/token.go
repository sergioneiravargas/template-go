package auth

import (
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"

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

// Parses the token using the given RSA public key
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
