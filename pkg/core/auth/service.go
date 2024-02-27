package auth

type UserInfoCache interface {
	Get(key string) (value *UserInfo, found bool)
	Set(key string, value *UserInfo)
	Unset(key string)
}

// Service for auth operations
type Service struct {
	keySet        KeySet
	domainURL     string
	userInfoCache UserInfoCache
}

// Auth service configuration
type Conf struct {
	KeySet    KeySet
	DomainURL string
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
		keySet:    conf.KeySet,
		domainURL: conf.DomainURL,
	}

	for _, opt := range opts {
		opt(&service)
	}

	return &service
}

// Validates the given token
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

// Retrieves the claims from the given token
func (s *Service) TokenClaims(token string) (MapClaims, error) {
	parsedToken, err := ParseToken(token, s.keySet)
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

	// Fetch the user information
	userInfo, err := FetchUserInfo(s.domainURL+"/userinfo", token)
	if err != nil {
		return nil, err
	}

	if s.userInfoCache != nil {
		// Add the user information to cache
		s.userInfoCache.Set(userID, userInfo)
	}

	return userInfo, nil
}
