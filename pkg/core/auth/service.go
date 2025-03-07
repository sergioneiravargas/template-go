package auth

import "crypto/rsa"

type UserInfoCache interface {
	Get(key string) (value *UserInfo, found bool)
	Set(key string, value *UserInfo)
	Unset(key string)
}

// Service for auth operations
type Service struct {
	conf          Conf
	userInfoCache UserInfoCache
}

// Auth service configuration
type Conf struct {
	KeySet      KeySet
	UserInfoURL string

	PEMCertificate struct {
		Private *rsa.PrivateKey
		Public  *rsa.PublicKey
	}
}

// Service option
type ServiceOption func(*Service)

// Service option to set the user info cache
func ServiceWithUserInfoCache(cache UserInfoCache) ServiceOption {
	return func(s *Service) {
		s.userInfoCache = cache
	}
}

// Creates a new auth service
func NewService(
	conf Conf,
	opts ...ServiceOption,
) *Service {
	service := Service{
		conf: conf,
	}

	for _, opt := range opts {
		opt(&service)
	}

	return &service
}

// Validates the given token
func (s *Service) ValidateToken(token string) error {
	if err := ValidateTokenWithPEM(token, s.conf.PEMCertificate.Public); err == nil {
		return nil
	}

	return ValidateTokenWithJWKS(token, s.conf.KeySet)
}

func ValidateTokenWithPEM(token string, key *rsa.PublicKey) error {
	parsedToken, err := ParseTokenWithPEM(token, key)
	if err != nil {
		return err
	}

	if !parsedToken.Valid {
		return ErrInvalidToken
	}

	return nil
}

func ValidateTokenWithJWKS(token string, keySet KeySet) error {
	parsedToken, err := ParseTokenWithJWKS(token, keySet)
	if err != nil {
		return err
	}

	if !parsedToken.Valid {
		return ErrInvalidToken
	}

	return nil
}

// Retrieves the claims from the given token
func (s *Service) TokenClaims(token string) (MapClaims, error) {
	claims, err := TokenClaimsFromPEM(token, s.conf.PEMCertificate.Public)
	if err == nil {
		return claims, nil
	}

	return TokenClaimsFromJWKS(token, s.conf.KeySet)
}

func TokenClaimsFromPEM(token string, key *rsa.PublicKey) (MapClaims, error) {
	parsedToken, err := ParseTokenWithPEM(token, key)
	if err != nil {
		return nil, err
	}

	if !parsedToken.Valid {
		return nil, ErrInvalidToken
	}

	claims, valid := parsedToken.Claims.(MapClaims)
	if !valid {
		return nil, ErrInvalidTokenClaims
	}

	return claims, nil
}

func TokenClaimsFromJWKS(token string, keySet KeySet) (MapClaims, error) {
	parsedToken, err := ParseTokenWithJWKS(token, keySet)
	if err != nil {
		return nil, err
	}

	if !parsedToken.Valid {
		return nil, ErrInvalidToken
	}

	claims, valid := parsedToken.Claims.(MapClaims)
	if !valid {
		return nil, ErrInvalidTokenClaims
	}

	return claims, nil
}

// Retrieves the user information from the given access token
func (s *Service) UserInfo(
	token string,
) (*UserInfo, error) {
	// Check if the user information is in cache
	claims, err := s.TokenClaims(token)
	if err != nil {
		return nil, err
	}

	userID, valid := claims["sub"].(string)
	if !valid {
		return nil, ErrInvalidTokenClaims
	}

	if s.userInfoCache != nil {
		//  Check if the user information is in cache and return it if found
		userInfo, found := s.userInfoCache.Get(userID)
		if found {
			return userInfo, nil
		}
	}

	userInfo, err := UserInfoFromClaims(claims)
	if err == nil && userInfo != nil {
		// Add the user information to cache
		s.userInfoCache.Set(userID, userInfo)

		return userInfo, nil
	}

	// Fetch the user information
	userInfo, err = FetchUserInfo(s.conf.UserInfoURL, token)
	if err != nil {

		return nil, err
	}

	if s.userInfoCache != nil {
		// Add the user information to cache
		s.userInfoCache.Set(userID, userInfo)
	}

	return userInfo, nil
}

func (s *Service) GenerateToken(claims MapClaims) (string, error) {
	return GenerateToken(claims, s.conf.PEMCertificate.Private)
}
