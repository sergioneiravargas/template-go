package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/sergioneiravargas/template-go/internal/platform/httpfetch"
)

func newTestService(t *testing.T, opts ...ServiceOption) (*Service, *rsa.PrivateKey) {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}

	conf := Conf{
		UserInfoURL:    "http://idp.local/userinfo",
		PEMCertificate: PEMCertificate{Private: key, Public: &key.PublicKey},
	}

	return NewService(conf, opts...), key
}

func signedToken(t *testing.T, service *Service, claims MapClaims) string {
	t.Helper()

	if _, ok := claims["exp"]; !ok {
		claims["exp"] = time.Now().Add(time.Hour).Unix()
	}
	token, err := service.GenerateToken(claims)
	if err != nil {
		t.Fatalf("failed to generate token: %v", err)
	}
	return token
}

func TestService_UserInfo_FromClaimsWithoutCache(t *testing.T) {
	service, _ := newTestService(t)
	token := signedToken(t, service, MapClaims{"sub": "user-1", "email": "user@example.com"})

	userInfo, err := service.UserInfo(context.Background(), token)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if userInfo.ID != "user-1" || userInfo.Email != "user@example.com" {
		t.Fatalf("unexpected user info: %+v", userInfo)
	}
}

func TestService_UserInfo_RejectsMissingSub(t *testing.T) {
	service, key := newTestService(t)
	token, err := GenerateToken(MapClaims{"exp": time.Now().Add(time.Hour).Unix()}, key)
	if err != nil {
		t.Fatalf("failed to generate token: %v", err)
	}
	if _, err := service.UserInfo(context.Background(), token); !errors.Is(err, ErrInvalidTokenClaims) {
		t.Fatalf("expected ErrInvalidTokenClaims, got %v", err)
	}
}

func TestService_UserInfo_UsesCache(t *testing.T) {
	cache := &fakeCache{values: map[string]*UserInfo{}}
	service, _ := newTestService(t, ServiceWithUserInfoCache(cache))
	token := signedToken(t, service, MapClaims{"sub": "user-1", "email": "user@example.com"})

	if _, err := service.UserInfo(context.Background(), token); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cache.sets != 1 {
		t.Fatalf("expected 1 cache set, got %d", cache.sets)
	}

	cache.values["user-1"].Email = "cached@example.com"
	userInfo, err := service.UserInfo(context.Background(), token)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if userInfo.Email != "cached@example.com" {
		t.Fatalf("expected cached user info, got %+v", userInfo)
	}
}

func TestFetchUserInfo(t *testing.T) {
	fetcher := &fakeFetcher{
		DoFunc: func(ctx context.Context, req *http.Request, opts ...httpfetch.Option) (*httpfetch.Response, error) {
			if got := req.Header.Get("Authorization"); got != "Bearer token" {
				t.Fatalf("unexpected authorization header: %q", got)
			}
			return &httpfetch.Response{Status: 200, Body: []byte(`{"sub":"user-2","email":"two@example.com"}`)}, nil
		},
	}

	userInfo, err := FetchUserInfo(context.Background(), fetcher, "http://idp.local/userinfo", "token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if userInfo.ID != "user-2" || userInfo.Email != "two@example.com" {
		t.Fatalf("unexpected user info: %+v", userInfo)
	}
}

type fakeCache struct {
	values map[string]*UserInfo
	sets   int
}

func (c *fakeCache) Get(key string) (*UserInfo, bool) {
	v, ok := c.values[key]
	return v, ok
}

func (c *fakeCache) Set(key string, value *UserInfo) {
	c.sets++
	c.values[key] = value
}

func (c *fakeCache) Unset(key string) {
	delete(c.values, key)
}
