package jwt

import (
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
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

type Token = jwt.Token

type MapClaims = jwt.MapClaims

type KeySet struct {
	Keys []Key `json:"keys"`
}

type Key struct {
	Kid string `json:"kid"`
	Alg string `json:"alg"`
	Kty string `json:"kty"`
	E   string `json:"e"`
	N   string `json:"n"`
	Use string `json:"use"`
}

type Service struct {
	keySet KeySet
}

func NewService(
	keySetURL string,
) *Service {
	keySet, err := FetchKeySet(keySetURL)
	if err != nil {
		panic(err)
	}

	return &Service{
		keySet: keySet,
	}
}

func (s *Service) ValidateToken(token string) error {
	parsedToken, err := ParseToken(token, s.keySet)
	if err != nil {
		return err
	}

	if !parsedToken.Valid {
		return ErrInvalidToken
	}

	return nil
}

func (s *Service) TokenClaims(token string) (MapClaims, error) {
	parsedToken, err := ParseToken(token, s.keySet)
	if err != nil {
		return nil, err
	}

	if !parsedToken.Valid {
		return nil, ErrInvalidToken
	}

	claims, validClaims := parsedToken.Claims.(MapClaims)
	if !validClaims {
		return nil, ErrInvalidTokenClaims
	}

	return claims, nil
}

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

func TokenFromHeader(header string) (string, error) {
	if len(header) > 7 && strings.ToUpper(header[0:6]) == "BEARER" {
		token := strings.Clone(header[7:])

		return token, nil
	}

	return "", ErrInvalidHeader
}

func ParseToken(token string, keySet KeySet) (*Token, error) {
	parsedToken, err := jwt.Parse(
		token,
		func(t *Token) (any, error) {
			for _, key := range keySet.Keys {
				if key.Kid != t.Header["kid"] {
					continue
				}

				rsa, err := RSAPublicKey(key)
				if err != nil {
					return nil, ErrRSAPublicKeyCouldNotBeDecoded
				}

				return &rsa, nil
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

func RSAPublicKey(key Key) (rsa.PublicKey, error) {
	nb, err := base64.RawURLEncoding.DecodeString(key.N)
	if err != nil {
		return rsa.PublicKey{}, ErrModulusCouldNotBeDecoded
	}

	eb, err := base64.RawURLEncoding.DecodeString(key.E)
	if err != nil {
		return rsa.PublicKey{}, ErrExponentCouldNotBeDecoded
	}

	return rsa.PublicKey{
		N: big.NewInt(0).SetBytes(nb),
		E: int(big.NewInt(0).SetBytes(eb).Int64()),
	}, nil
}
