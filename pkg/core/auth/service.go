package auth

import (
	"template-go/pkg/cache"
	"time"
)

type Service struct {
	keySet        KeySet
	domainURL     string
	userInfoCache *cache.Cache[string, *UserInfo]
}

type Conf struct {
	KeySetURL string
	DomainURL string
}

func NewService(
	conf Conf,
) *Service {
	keySet, err := FetchKeySet(conf.KeySetURL)
	if err != nil {
		panic(err)
	}

	userInfoCache := cache.New[string, *UserInfo](
		cache.WithTTL[string, *UserInfo](10*time.Minute),
		cache.WithCleanupInterval[string, *UserInfo](30*time.Second),
	)

	return &Service{
		keySet:        keySet,
		domainURL:     conf.DomainURL,
		userInfoCache: userInfoCache,
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

func (s *Service) UserInfo(
	token string,
) (*UserInfo, error) {
	// Check if user info is in cache
	claims, err := s.TokenClaims(token)
	if err != nil {
		return nil, err
	}

	userID, validValue := claims["sub"].(string)
	if !validValue {
		return nil, ErrInvalidTokenClaims
	}

	userInfo, valueFound := s.userInfoCache.Get(userID)
	if valueFound {
		return userInfo, nil
	}

	// Fetch user info
	userInfo, err = FetchUserInfo(s.domainURL+"/oauth2/userInfo", token)
	if err != nil {
		return nil, err
	}
	s.userInfoCache.Set(userInfo.ID, userInfo)

	return userInfo, nil
}