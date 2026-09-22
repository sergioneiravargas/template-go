package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/golang-jwt/jwt/v5"
)

var (
	ErrInvalidHeader         = errors.New("invalid header")
	ErrTokenMalformed        = errors.New("token is malformed")
	ErrTokenExpired          = errors.New("token is expired")
	ErrTokenNotValidYet      = errors.New("token is not valid yet")
	ErrTokenCouldNotBeParsed = errors.New("token could not be parsed")
	ErrInvalidToken          = errors.New("invalid token")
	ErrInvalidTokenClaims    = errors.New("invalid token claims")
)

// JSON Web Token (JWT)
type Token = jwt.Token

// JWT Map Claims
type MapClaims = jwt.MapClaims

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

// Generates an opaque refresh token and the hash it is stored under
func GenerateRefreshToken() (string, string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", fmt.Errorf("failed to generate refresh token: %w", err)
	}

	token := base64.RawURLEncoding.EncodeToString(raw)

	return token, HashRefreshToken(token), nil
}

// Hashes a refresh token for storage and lookup
func HashRefreshToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
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
