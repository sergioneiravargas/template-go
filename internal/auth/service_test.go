package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/sergioneiravargas/template-go/internal/platform/mailer"
	"github.com/sergioneiravargas/template-go/internal/platform/queue"
)

const (
	testUserID  = "0e8dd54c-3a2b-4dd0-a2a3-0d6d3ba46e5e"
	testTokenID = "6f0f8f9a-58d5-4d2c-9a1e-0c9d3f2b1a70"
)

func newTestKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}

	return key
}

func newTestService(t *testing.T, repo UserRepository, opts ...ServiceOption) *Service {
	t.Helper()

	return newTestServiceWithMailer(t, repo, &fakeMailer{
		SendFunc: func(ctx context.Context, email mailer.Email) error { return nil },
	}, opts...)
}

func newTestServiceWithMailer(t *testing.T, repo UserRepository, m Mailer, opts ...ServiceOption) *Service {
	t.Helper()

	key := newTestKey(t)
	conf := Conf{
		PEMCertificate:   PEMCertificate{Private: key, Public: &key.PublicKey},
		PasswordResetURL: "https://example.com/reset-password",
	}

	return NewService(conf, repo, m, opts...)
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

func newTestUser(t *testing.T, password string) *User {
	t.Helper()

	hash, err := HashPassword(password)
	if err != nil {
		t.Fatalf("failed to hash password: %v", err)
	}

	return &User{
		ID:           testUserID,
		Email:        "ada@example.com",
		PasswordHash: hash,
		GivenName:    "Ada",
	}
}

func TestService_Register(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		var created *User
		repo := &fakeUserRepository{
			CreateUserFunc: func(ctx context.Context, user *User) error {
				created = user
				return nil
			},
		}
		service := newTestService(t, repo)

		user, err := service.Register(context.Background(), RegisterInput{
			Email:     "ada@example.com",
			Password:  "long-enough-password",
			GivenName: "Ada",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if created == nil || created != user {
			t.Fatal("expected the created user to be returned")
		}
		if _, err := uuid.Parse(user.ID); err != nil {
			t.Fatalf("expected UUID id, got %q", user.ID)
		}
		if user.Email != "ada@example.com" || user.GivenName != "Ada" {
			t.Fatalf("unexpected user: %+v", user)
		}
		if user.CreatedAt.IsZero() || user.UpdatedAt.IsZero() {
			t.Fatal("expected timestamps to be set")
		}
		match, err := VerifyPassword(user.PasswordHash, "long-enough-password")
		if err != nil || !match {
			t.Fatalf("expected stored hash to verify, got match=%t err=%v", match, err)
		}
	})

	t.Run("duplicate email", func(t *testing.T) {
		repo := &fakeUserRepository{
			CreateUserFunc: func(ctx context.Context, user *User) error {
				return ErrEmailAlreadyExists
			},
		}
		service := newTestService(t, repo)

		_, err := service.Register(context.Background(), RegisterInput{
			Email:    "ada@example.com",
			Password: "long-enough-password",
		})
		if !errors.Is(err, ErrEmailAlreadyExists) {
			t.Fatalf("expected ErrEmailAlreadyExists, got %v", err)
		}
	})

	t.Run("invalid input skips repository", func(t *testing.T) {
		service := newTestService(t, &fakeUserRepository{})

		if _, err := service.Register(context.Background(), RegisterInput{
			Email:    "ada@example.com",
			Password: "short",
		}); err == nil {
			t.Fatal("expected validation error")
		}
	})
}

func TestService_Login(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		user := newTestUser(t, "correct password 123")
		var stored *RefreshToken
		repo := &fakeUserRepository{
			GetUserByEmailFunc: func(ctx context.Context, email string) (*User, error) {
				return user, nil
			},
			CreateRefreshTokenFunc: func(ctx context.Context, token *RefreshToken) error {
				stored = token
				return nil
			},
		}
		service := newTestService(t, repo)

		pair, err := service.Login(context.Background(), LoginInput{
			Email:    "ada@example.com",
			Password: "correct password 123",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if pair.ExpiresIn != 900 {
			t.Fatalf("expected 900s expiry, got %d", pair.ExpiresIn)
		}
		if len(pair.RefreshToken) != 43 {
			t.Fatalf("expected opaque refresh token, got %q", pair.RefreshToken)
		}
		claims, err := service.TokenClaims(pair.AccessToken)
		if err != nil {
			t.Fatalf("access token does not validate: %v", err)
		}
		if sub, _ := claims["sub"].(string); sub != user.ID {
			t.Fatalf("expected sub %q, got %q", user.ID, sub)
		}
		if stored == nil || stored.TokenHash != HashRefreshToken(pair.RefreshToken) {
			t.Fatalf("stored token hash mismatch: %+v", stored)
		}
		if stored.UserID != user.ID || stored.FamilyID == "" {
			t.Fatalf("unexpected stored token: %+v", stored)
		}
		if stored.ExpiresAt.Before(time.Now().Add(RefreshTokenTTL - time.Minute)) {
			t.Fatalf("unexpected refresh expiry: %v", stored.ExpiresAt)
		}
	})

	t.Run("wrong password", func(t *testing.T) {
		user := newTestUser(t, "correct password 123")
		repo := &fakeUserRepository{
			GetUserByEmailFunc: func(ctx context.Context, email string) (*User, error) {
				return user, nil
			},
		}
		service := newTestService(t, repo)

		_, err := service.Login(context.Background(), LoginInput{
			Email:    "ada@example.com",
			Password: "wrong password",
		})
		if !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("expected ErrInvalidCredentials, got %v", err)
		}
	})

	t.Run("unknown email", func(t *testing.T) {
		repo := &fakeUserRepository{
			GetUserByEmailFunc: func(ctx context.Context, email string) (*User, error) {
				return nil, nil
			},
		}
		service := newTestService(t, repo)

		_, err := service.Login(context.Background(), LoginInput{
			Email:    "nobody@example.com",
			Password: "whatever password",
		})
		if !errors.Is(err, ErrInvalidCredentials) {
			t.Fatalf("expected ErrInvalidCredentials, got %v", err)
		}
	})
}

func liveRefreshToken(tokenString string) *RefreshToken {
	return &RefreshToken{
		ID:        testTokenID,
		UserID:    testUserID,
		TokenHash: HashRefreshToken(tokenString),
		FamilyID:  "3c9c1f52-6c2a-4b6e-8a34-2b8f1f6f7d11",
		ExpiresAt: time.Now().Add(time.Hour),
		CreatedAt: time.Now().Add(-time.Hour),
	}
}

func TestService_Refresh(t *testing.T) {
	t.Run("success rotates within the family", func(t *testing.T) {
		user := newTestUser(t, "irrelevant password")
		refreshToken := liveRefreshToken("old-refresh-token")
		var rotatedOldID string
		var next *RefreshToken
		repo := &fakeUserRepository{
			GetRefreshTokenByHashFunc: func(ctx context.Context, tokenHash string) (*RefreshToken, error) {
				if tokenHash != refreshToken.TokenHash {
					return nil, nil
				}
				return refreshToken, nil
			},
			GetUserFunc: func(ctx context.Context, id string) (*User, error) {
				return user, nil
			},
			RotateRefreshTokenFunc: func(ctx context.Context, oldID string, n *RefreshToken) error {
				rotatedOldID = oldID
				next = n
				return nil
			},
		}
		service := newTestService(t, repo)

		pair, err := service.Refresh(context.Background(), RefreshInput{RefreshToken: "old-refresh-token"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if rotatedOldID != refreshToken.ID {
			t.Fatalf("expected rotation of %q, got %q", refreshToken.ID, rotatedOldID)
		}
		if next == nil || next.FamilyID != refreshToken.FamilyID {
			t.Fatalf("expected same family, got %+v", next)
		}
		if next.TokenHash != HashRefreshToken(pair.RefreshToken) {
			t.Fatal("next token hash does not match returned refresh token")
		}
		if claims, err := service.TokenClaims(pair.AccessToken); err != nil {
			t.Fatalf("access token does not validate: %v", err)
		} else if sub, _ := claims["sub"].(string); sub != testUserID {
			t.Fatalf("unexpected sub %q", sub)
		}
	})

	t.Run("reuse of revoked token revokes the family", func(t *testing.T) {
		refreshToken := liveRefreshToken("stolen-token")
		revokedAt := time.Now().Add(-time.Minute)
		refreshToken.RevokedAt = &revokedAt
		var revokedFamily string
		repo := &fakeUserRepository{
			GetRefreshTokenByHashFunc: func(ctx context.Context, tokenHash string) (*RefreshToken, error) {
				return refreshToken, nil
			},
			RevokeRefreshTokenFamilyFunc: func(ctx context.Context, familyID string) error {
				revokedFamily = familyID
				return nil
			},
		}
		service := newTestService(t, repo)

		_, err := service.Refresh(context.Background(), RefreshInput{RefreshToken: "stolen-token"})
		if !errors.Is(err, ErrInvalidRefreshToken) {
			t.Fatalf("expected ErrInvalidRefreshToken, got %v", err)
		}
		if revokedFamily != refreshToken.FamilyID {
			t.Fatalf("expected family %q revoked, got %q", refreshToken.FamilyID, revokedFamily)
		}
	})

	t.Run("rotation race revokes the family", func(t *testing.T) {
		user := newTestUser(t, "irrelevant password")
		refreshToken := liveRefreshToken("raced-token")
		var revokedFamily string
		repo := &fakeUserRepository{
			GetRefreshTokenByHashFunc: func(ctx context.Context, tokenHash string) (*RefreshToken, error) {
				return refreshToken, nil
			},
			GetUserFunc: func(ctx context.Context, id string) (*User, error) {
				return user, nil
			},
			RotateRefreshTokenFunc: func(ctx context.Context, oldID string, n *RefreshToken) error {
				return ErrRefreshTokenRotated
			},
			RevokeRefreshTokenFamilyFunc: func(ctx context.Context, familyID string) error {
				revokedFamily = familyID
				return nil
			},
		}
		service := newTestService(t, repo)

		_, err := service.Refresh(context.Background(), RefreshInput{RefreshToken: "raced-token"})
		if !errors.Is(err, ErrInvalidRefreshToken) {
			t.Fatalf("expected ErrInvalidRefreshToken, got %v", err)
		}
		if revokedFamily != refreshToken.FamilyID {
			t.Fatalf("expected family %q revoked, got %q", refreshToken.FamilyID, revokedFamily)
		}
	})

	t.Run("expired token", func(t *testing.T) {
		refreshToken := liveRefreshToken("expired-token")
		refreshToken.ExpiresAt = time.Now().Add(-time.Minute)
		repo := &fakeUserRepository{
			GetRefreshTokenByHashFunc: func(ctx context.Context, tokenHash string) (*RefreshToken, error) {
				return refreshToken, nil
			},
		}
		service := newTestService(t, repo)

		if _, err := service.Refresh(context.Background(), RefreshInput{RefreshToken: "expired-token"}); !errors.Is(err, ErrInvalidRefreshToken) {
			t.Fatalf("expected ErrInvalidRefreshToken, got %v", err)
		}
	})

	t.Run("unknown token", func(t *testing.T) {
		repo := &fakeUserRepository{
			GetRefreshTokenByHashFunc: func(ctx context.Context, tokenHash string) (*RefreshToken, error) {
				return nil, nil
			},
		}
		service := newTestService(t, repo)

		if _, err := service.Refresh(context.Background(), RefreshInput{RefreshToken: "unknown-token"}); !errors.Is(err, ErrInvalidRefreshToken) {
			t.Fatalf("expected ErrInvalidRefreshToken, got %v", err)
		}
	})
}

func TestService_Logout(t *testing.T) {
	t.Run("revokes the token family", func(t *testing.T) {
		refreshToken := liveRefreshToken("session-token")
		var revokedFamily string
		repo := &fakeUserRepository{
			GetRefreshTokenByHashFunc: func(ctx context.Context, tokenHash string) (*RefreshToken, error) {
				return refreshToken, nil
			},
			RevokeRefreshTokenFamilyFunc: func(ctx context.Context, familyID string) error {
				revokedFamily = familyID
				return nil
			},
		}
		service := newTestService(t, repo)

		if err := service.Logout(context.Background(), LogoutInput{RefreshToken: "session-token"}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if revokedFamily != refreshToken.FamilyID {
			t.Fatalf("expected family %q revoked, got %q", refreshToken.FamilyID, revokedFamily)
		}
	})

	t.Run("unknown token is a no-op", func(t *testing.T) {
		repo := &fakeUserRepository{
			GetRefreshTokenByHashFunc: func(ctx context.Context, tokenHash string) (*RefreshToken, error) {
				return nil, nil
			},
		}
		service := newTestService(t, repo)

		if err := service.Logout(context.Background(), LogoutInput{RefreshToken: "unknown-token"}); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestService_UserInfo(t *testing.T) {
	user := &User{
		ID:            testUserID,
		Email:         "ada@example.com",
		GivenName:     "Ada",
		FamilyName:    "Lovelace",
		EmailVerified: true,
	}

	t.Run("from repository", func(t *testing.T) {
		repo := &fakeUserRepository{
			GetUserFunc: func(ctx context.Context, id string) (*User, error) {
				if id != testUserID {
					return nil, nil
				}
				return user, nil
			},
		}
		service := newTestService(t, repo)
		token := signedToken(t, service, MapClaims{"sub": testUserID})

		userInfo, err := service.UserInfo(context.Background(), token)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := UserInfo{ID: testUserID, GivenName: "Ada", FamilyName: "Lovelace", Email: "ada@example.com", EmailVerified: true}
		if *userInfo != want {
			t.Fatalf("unexpected user info: %+v", userInfo)
		}
	})

	t.Run("unknown user", func(t *testing.T) {
		repo := &fakeUserRepository{
			GetUserFunc: func(ctx context.Context, id string) (*User, error) {
				return nil, nil
			},
		}
		service := newTestService(t, repo)
		token := signedToken(t, service, MapClaims{"sub": "b2b7f5a0-9f6a-4dbb-8f5b-6a4d3c2e1f00"})

		if _, err := service.UserInfo(context.Background(), token); !errors.Is(err, ErrUserNotFound) {
			t.Fatalf("expected ErrUserNotFound, got %v", err)
		}
	})

	t.Run("non-uuid sub skips repository", func(t *testing.T) {
		service := newTestService(t, &fakeUserRepository{})
		token := signedToken(t, service, MapClaims{"sub": "user-1"})

		if _, err := service.UserInfo(context.Background(), token); !errors.Is(err, ErrUserNotFound) {
			t.Fatalf("expected ErrUserNotFound, got %v", err)
		}
	})

	t.Run("missing sub", func(t *testing.T) {
		service := newTestService(t, &fakeUserRepository{})
		token := signedToken(t, service, MapClaims{"email": "ada@example.com"})

		if _, err := service.UserInfo(context.Background(), token); !errors.Is(err, ErrInvalidTokenClaims) {
			t.Fatalf("expected ErrInvalidTokenClaims, got %v", err)
		}
	})

	t.Run("cache hit skips repository", func(t *testing.T) {
		cached := &UserInfo{ID: testUserID, Email: "cached@example.com"}
		cache := &fakeCache{values: map[string]*UserInfo{testUserID: cached}}
		service := newTestService(t, &fakeUserRepository{}, ServiceWithUserInfoCache(cache))
		token := signedToken(t, service, MapClaims{"sub": testUserID})

		userInfo, err := service.UserInfo(context.Background(), token)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if userInfo != cached {
			t.Fatalf("expected cached value, got %+v", userInfo)
		}
	})

	t.Run("cache miss populates the cache", func(t *testing.T) {
		cache := &fakeCache{values: map[string]*UserInfo{}}
		repo := &fakeUserRepository{
			GetUserFunc: func(ctx context.Context, id string) (*User, error) {
				return user, nil
			},
		}
		service := newTestService(t, repo, ServiceWithUserInfoCache(cache))
		token := signedToken(t, service, MapClaims{"sub": testUserID})

		if _, err := service.UserInfo(context.Background(), token); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cache.sets != 1 {
			t.Fatalf("expected 1 cache set, got %d", cache.sets)
		}
	})
}

func TestService_ValidateToken_PEMOnlyErrors(t *testing.T) {
	service := newTestService(t, &fakeUserRepository{})

	tests := []struct {
		name    string
		token   func(t *testing.T) string
		wantErr error
	}{
		{
			name: "expired token",
			token: func(t *testing.T) string {
				return signedToken(t, service, MapClaims{"sub": testUserID, "exp": time.Now().Add(-time.Hour).Unix()})
			},
			wantErr: ErrTokenExpired,
		},
		{
			name: "token not valid yet",
			token: func(t *testing.T) string {
				return signedToken(t, service, MapClaims{"sub": testUserID, "nbf": time.Now().Add(time.Hour).Unix()})
			},
			wantErr: ErrTokenNotValidYet,
		},
		{
			name: "malformed token",
			token: func(t *testing.T) string {
				return "not-a-token"
			},
			wantErr: ErrTokenMalformed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token := tt.token(t)
			if err := service.ValidateToken(token); !errors.Is(err, tt.wantErr) {
				t.Fatalf("ValidateToken: expected %v, got %v", tt.wantErr, err)
			}
			if _, err := service.TokenClaims(token); !errors.Is(err, tt.wantErr) {
				t.Fatalf("TokenClaims: expected %v, got %v", tt.wantErr, err)
			}
		})
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

func TestService_ForgotPassword(t *testing.T) {
	t.Run("stores a token and queues the reset email", func(t *testing.T) {
		user := newTestUser(t, "correct password 123")
		var stored *PasswordResetToken
		var messages []*queue.Message
		repo := &fakeUserRepository{
			GetUserByEmailFunc: func(ctx context.Context, email string) (*User, error) {
				return user, nil
			},
			CreatePasswordResetTokenFunc: func(ctx context.Context, token *PasswordResetToken, queueMessages ...*queue.Message) error {
				stored = token
				messages = queueMessages
				return nil
			},
		}
		service := newTestService(t, repo)

		err := service.ForgotPassword(context.Background(), ForgotPasswordInput{Email: "ada@example.com"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if stored == nil {
			t.Fatal("expected a reset token to be stored")
		}
		if _, err := uuid.Parse(stored.ID); err != nil {
			t.Fatalf("expected UUID id, got %q", stored.ID)
		}
		if stored.UserID != user.ID {
			t.Fatalf("unexpected user id: %q", stored.UserID)
		}
		if !stored.ExpiresAt.After(stored.CreatedAt) {
			t.Fatalf("expected expiry after creation: %+v", stored)
		}
		if len(messages) != 1 || messages[0].Name != MessageNamePasswordResetRequested {
			t.Fatalf("expected one password reset queue message, got %+v", messages)
		}
		msgBody, ok := queue.DecodeMessage[MessagePasswordResetRequested](messages[0])
		if !ok {
			t.Fatal("failed to decode queue message")
		}
		if msgBody.UserID != user.ID {
			t.Fatalf("unexpected message user id: %q", msgBody.UserID)
		}
		if HashPasswordResetToken(msgBody.Token) != stored.TokenHash {
			t.Fatal("expected the queued token to match the stored hash")
		}
	})

	t.Run("unknown email is a silent no-op", func(t *testing.T) {
		created := false
		repo := &fakeUserRepository{
			GetUserByEmailFunc: func(ctx context.Context, email string) (*User, error) {
				return nil, nil
			},
			CreatePasswordResetTokenFunc: func(ctx context.Context, token *PasswordResetToken, queueMessages ...*queue.Message) error {
				created = true
				return nil
			},
		}
		service := newTestService(t, repo)

		err := service.ForgotPassword(context.Background(), ForgotPasswordInput{Email: "ghost@example.com"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if created {
			t.Fatal("expected no reset token for an unknown email")
		}
	})

	t.Run("invalid input skips repository", func(t *testing.T) {
		service := newTestService(t, &fakeUserRepository{})

		if err := service.ForgotPassword(context.Background(), ForgotPasswordInput{Email: "not-an-email"}); err == nil {
			t.Fatal("expected validation error")
		}
	})
}

func TestService_ResetPassword(t *testing.T) {
	validRow := func() *PasswordResetToken {
		return &PasswordResetToken{
			ID:        testTokenID,
			UserID:    testUserID,
			TokenHash: HashPasswordResetToken("reset-token"),
			ExpiresAt: time.Now().Add(30 * time.Minute),
			CreatedAt: time.Now(),
		}
	}

	t.Run("success", func(t *testing.T) {
		row := validRow()
		var consumed *PasswordResetToken
		var newHash string
		repo := &fakeUserRepository{
			GetPasswordResetTokenByHashFunc: func(ctx context.Context, tokenHash string) (*PasswordResetToken, error) {
				if tokenHash != row.TokenHash {
					t.Fatalf("unexpected token hash lookup: %q", tokenHash)
				}
				return row, nil
			},
			ConsumePasswordResetTokenFunc: func(ctx context.Context, token *PasswordResetToken, passwordHash string) error {
				consumed = token
				newHash = passwordHash
				return nil
			},
		}
		service := newTestService(t, repo)

		err := service.ResetPassword(context.Background(), ResetPasswordInput{
			Token:    "reset-token",
			Password: "brand new password",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if consumed != row {
			t.Fatal("expected the fetched token to be consumed")
		}
		match, err := VerifyPassword(newHash, "brand new password")
		if err != nil || !match {
			t.Fatalf("expected new hash to verify, got match=%t err=%v", match, err)
		}
	})

	t.Run("invalid tokens map to the sentinel", func(t *testing.T) {
		used := time.Now()
		tests := []struct {
			name string
			row  *PasswordResetToken
		}{
			{name: "unknown token", row: nil},
			{name: "used token", row: func() *PasswordResetToken { r := validRow(); r.UsedAt = &used; return r }()},
			{name: "expired token", row: func() *PasswordResetToken { r := validRow(); r.ExpiresAt = time.Now().Add(-time.Minute); return r }()},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				repo := &fakeUserRepository{
					GetPasswordResetTokenByHashFunc: func(ctx context.Context, tokenHash string) (*PasswordResetToken, error) {
						return tt.row, nil
					},
				}
				service := newTestService(t, repo)

				err := service.ResetPassword(context.Background(), ResetPasswordInput{
					Token:    "reset-token",
					Password: "brand new password",
				})
				if !errors.Is(err, ErrInvalidPasswordResetToken) {
					t.Fatalf("expected ErrInvalidPasswordResetToken, got %v", err)
				}
			})
		}
	})

	t.Run("concurrent consumption maps to the sentinel", func(t *testing.T) {
		repo := &fakeUserRepository{
			GetPasswordResetTokenByHashFunc: func(ctx context.Context, tokenHash string) (*PasswordResetToken, error) {
				return validRow(), nil
			},
			ConsumePasswordResetTokenFunc: func(ctx context.Context, token *PasswordResetToken, passwordHash string) error {
				return ErrPasswordResetTokenUsed
			},
		}
		service := newTestService(t, repo)

		err := service.ResetPassword(context.Background(), ResetPasswordInput{
			Token:    "reset-token",
			Password: "brand new password",
		})
		if !errors.Is(err, ErrInvalidPasswordResetToken) {
			t.Fatalf("expected ErrInvalidPasswordResetToken, got %v", err)
		}
	})

	t.Run("invalid input skips repository", func(t *testing.T) {
		service := newTestService(t, &fakeUserRepository{})

		if err := service.ResetPassword(context.Background(), ResetPasswordInput{
			Token:    "reset-token",
			Password: "short",
		}); err == nil {
			t.Fatal("expected validation error")
		}
	})
}

func TestService_SendPasswordResetEmail(t *testing.T) {
	t.Run("sends the reset link to the user", func(t *testing.T) {
		user := newTestUser(t, "correct password 123")
		repo := &fakeUserRepository{
			GetUserFunc: func(ctx context.Context, id string) (*User, error) {
				if id != user.ID {
					t.Fatalf("unexpected user lookup: %q", id)
				}
				return user, nil
			},
		}
		var sent *mailer.Email
		service := newTestServiceWithMailer(t, repo, &fakeMailer{
			SendFunc: func(ctx context.Context, email mailer.Email) error {
				sent = &email
				return nil
			},
		})

		err := service.SendPasswordResetEmail(context.Background(), SendPasswordResetEmailInput{
			UserID: user.ID,
			Token:  "reset-token",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if sent == nil {
			t.Fatal("expected an email to be sent")
		}
		if len(sent.To) != 1 || sent.To[0] != user.Email {
			t.Fatalf("unexpected recipients: %v", sent.To)
		}
		link := "https://example.com/reset-password?token=reset-token"
		if !strings.Contains(sent.TextBody, link) || !strings.Contains(sent.HTMLBody, link) {
			t.Fatalf("expected both bodies to contain %q", link)
		}
		if !strings.Contains(sent.TextBody, user.GivenName) {
			t.Fatal("expected the greeting to use the given name")
		}
	})

	t.Run("unknown user maps to ErrUserNotFound", func(t *testing.T) {
		repo := &fakeUserRepository{
			GetUserFunc: func(ctx context.Context, id string) (*User, error) {
				return nil, nil
			},
		}
		service := newTestService(t, repo)

		err := service.SendPasswordResetEmail(context.Background(), SendPasswordResetEmailInput{
			UserID: testUserID,
			Token:  "reset-token",
		})
		if !errors.Is(err, ErrUserNotFound) {
			t.Fatalf("expected ErrUserNotFound, got %v", err)
		}
	})

	t.Run("mailer errors are propagated", func(t *testing.T) {
		user := newTestUser(t, "correct password 123")
		repo := &fakeUserRepository{
			GetUserFunc: func(ctx context.Context, id string) (*User, error) {
				return user, nil
			},
		}
		sendErr := errors.New("ses throttled")
		service := newTestServiceWithMailer(t, repo, &fakeMailer{
			SendFunc: func(ctx context.Context, email mailer.Email) error {
				return sendErr
			},
		})

		err := service.SendPasswordResetEmail(context.Background(), SendPasswordResetEmailInput{
			UserID: user.ID,
			Token:  "reset-token",
		})
		if !errors.Is(err, sendErr) {
			t.Fatalf("expected the mailer error to be wrapped, got %v", err)
		}
	})

	t.Run("invalid input skips repository", func(t *testing.T) {
		service := newTestService(t, &fakeUserRepository{})

		if err := service.SendPasswordResetEmail(context.Background(), SendPasswordResetEmailInput{
			UserID: testUserID,
		}); err == nil {
			t.Fatal("expected validation error")
		}
	})
}
