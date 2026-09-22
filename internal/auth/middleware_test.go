package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMiddleware(t *testing.T) {
	user := &User{ID: testUserID, GivenName: "Ada", Email: "ada@example.com"}
	repo := &fakeUserRepository{
		GetUserFunc: func(ctx context.Context, id string) (*User, error) {
			if id != user.ID {
				return nil, nil
			}
			return user, nil
		},
	}
	service := newTestService(t, repo)

	tests := []struct {
		name       string
		request    func(t *testing.T) *http.Request
		wantStatus int
		wantBody   string
	}{
		{
			name: "known user with bearer token",
			request: func(t *testing.T) *http.Request {
				r := httptest.NewRequest(http.MethodGet, "/", nil)
				r.Header.Set("Authorization", "Bearer "+signedToken(t, service, MapClaims{"sub": user.ID}))
				return r
			},
			wantStatus: http.StatusOK,
		},
		{
			name: "known user with query param token",
			request: func(t *testing.T) *http.Request {
				return httptest.NewRequest(http.MethodGet, "/?access_token="+signedToken(t, service, MapClaims{"sub": user.ID}), nil)
			},
			wantStatus: http.StatusOK,
		},
		{
			name: "unknown user",
			request: func(t *testing.T) *http.Request {
				r := httptest.NewRequest(http.MethodGet, "/", nil)
				r.Header.Set("Authorization", "Bearer "+signedToken(t, service, MapClaims{"sub": "b2b7f5a0-9f6a-4dbb-8f5b-6a4d3c2e1f00"}))
				return r
			},
			wantStatus: http.StatusUnauthorized,
			wantBody:   "Unknown user",
		},
		{
			name: "expired token",
			request: func(t *testing.T) *http.Request {
				r := httptest.NewRequest(http.MethodGet, "/", nil)
				r.Header.Set("Authorization", "Bearer "+signedToken(t, service, MapClaims{"sub": user.ID, "exp": time.Now().Add(-time.Hour).Unix()}))
				return r
			},
			wantStatus: http.StatusUnauthorized,
			wantBody:   "Expired JWT token",
		},
		{
			name: "missing token",
			request: func(t *testing.T) *http.Request {
				return httptest.NewRequest(http.MethodGet, "/", nil)
			},
			wantStatus: http.StatusUnauthorized,
			wantBody:   "Missing JWT token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nextCalled := false
			var gotUserInfo UserInfo
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				nextCalled = true
				gotUserInfo, _ = UserInfoFromRequest(r)
			})

			recorder := httptest.NewRecorder()
			Middleware(service)(next).ServeHTTP(recorder, tt.request(t))

			if recorder.Code != tt.wantStatus {
				t.Fatalf("expected status %d, got %d (%s)", tt.wantStatus, recorder.Code, recorder.Body.String())
			}
			if tt.wantBody != "" && !strings.Contains(recorder.Body.String(), tt.wantBody) {
				t.Fatalf("expected body to contain %q, got %q", tt.wantBody, recorder.Body.String())
			}
			if wantNext := tt.wantStatus == http.StatusOK; nextCalled != wantNext {
				t.Fatalf("expected next called %t, got %t", wantNext, nextCalled)
			}
			if nextCalled && gotUserInfo != (UserInfo{ID: user.ID, GivenName: "Ada", Email: "ada@example.com"}) {
				t.Fatalf("unexpected user info: %+v", gotUserInfo)
			}
		})
	}
}
