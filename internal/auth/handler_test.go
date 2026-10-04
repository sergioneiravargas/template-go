package auth

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sergioneiravargas/template-go/internal/platform/log"
	"github.com/sergioneiravargas/template-go/internal/platform/queue"
)

func newTestLogger() *log.Logger {
	return log.NewLogger("test", slog.NewJSONHandler(io.Discard, nil))
}

func TestRegisterAPIHandler(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		createErr  error
		wantStatus int
		wantBody   string
	}{
		{
			name:       "success",
			body:       `{"email":"ada@example.com","password":"long-enough-password","given_name":"Ada"}`,
			wantStatus: http.StatusCreated,
			wantBody:   `"email":"ada@example.com"`,
		},
		{
			name:       "duplicate email",
			body:       `{"email":"ada@example.com","password":"long-enough-password"}`,
			createErr:  ErrEmailAlreadyExists,
			wantStatus: http.StatusConflict,
			wantBody:   "Email already registered",
		},
		{
			name:       "invalid body",
			body:       `{not json`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "weak password",
			body:       `{"email":"ada@example.com","password":"short"}`,
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &fakeUserRepository{
				CreateUserFunc: func(ctx context.Context, user *User) error {
					return tt.createErr
				},
			}
			service := newTestService(t, repo)

			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "/auth/register", strings.NewReader(tt.body))
			RegisterAPIHandler(newTestLogger(), service)(w, r)

			if w.Code != tt.wantStatus {
				t.Fatalf("expected %d, got %d (%s)", tt.wantStatus, w.Code, w.Body.String())
			}
			if tt.wantBody != "" && !strings.Contains(w.Body.String(), tt.wantBody) {
				t.Fatalf("expected body to contain %q, got %s", tt.wantBody, w.Body.String())
			}
			if w.Code == http.StatusCreated && strings.Contains(strings.ToLower(w.Body.String()), "password") {
				t.Fatalf("response leaks password material: %s", w.Body.String())
			}
		})
	}
}

func TestLoginAPIHandler(t *testing.T) {
	user := newTestUser(t, "correct password 123")
	repo := &fakeUserRepository{
		GetUserByEmailFunc: func(ctx context.Context, email string) (*User, error) {
			return user, nil
		},
		CreateRefreshTokenFunc: func(ctx context.Context, token *RefreshToken) error {
			return nil
		},
	}
	service := newTestService(t, repo)

	t.Run("success", func(t *testing.T) {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(`{"email":"ada@example.com","password":"correct password 123"}`))
		LoginAPIHandler(newTestLogger(), service)(w, r)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d (%s)", w.Code, w.Body.String())
		}
		if body := w.Body.String(); !strings.Contains(body, `"access_token"`) || !strings.Contains(body, `"refresh_token"`) {
			t.Fatalf("expected token pair, got %s", body)
		}
	})

	t.Run("wrong password", func(t *testing.T) {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(`{"email":"ada@example.com","password":"wrong password"}`))
		LoginAPIHandler(newTestLogger(), service)(w, r)

		if w.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d (%s)", w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "Invalid credentials") {
			t.Fatalf("unexpected body: %s", w.Body.String())
		}
	})
}

func TestRefreshAPIHandler(t *testing.T) {
	repo := &fakeUserRepository{
		GetRefreshTokenByHashFunc: func(ctx context.Context, tokenHash string) (*RefreshToken, error) {
			return nil, nil
		},
	}
	service := newTestService(t, repo)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/auth/refresh", strings.NewReader(`{"refresh_token":"unknown"}`))
	RefreshAPIHandler(newTestLogger(), service)(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d (%s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "Invalid refresh token") {
		t.Fatalf("unexpected body: %s", w.Body.String())
	}
}

func TestForgotPasswordAPIHandler(t *testing.T) {
	t.Run("always answers 202", func(t *testing.T) {
		user := newTestUser(t, "correct password 123")
		tests := []struct {
			name string
			user *User
		}{
			{name: "known email", user: user},
			{name: "unknown email", user: nil},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				repo := &fakeUserRepository{
					GetUserByEmailFunc: func(ctx context.Context, email string) (*User, error) {
						return tt.user, nil
					},
					CreatePasswordResetTokenFunc: func(ctx context.Context, token *PasswordResetToken, queueMessages ...*queue.Message) error {
						return nil
					},
				}
				service := newTestService(t, repo)

				w := httptest.NewRecorder()
				r := httptest.NewRequest(http.MethodPost, "/auth/forgot-password", strings.NewReader(`{"email":"ada@example.com"}`))
				ForgotPasswordAPIHandler(newTestLogger(), service)(w, r)

				if w.Code != http.StatusAccepted {
					t.Fatalf("expected 202, got %d (%s)", w.Code, w.Body.String())
				}
			})
		}
	})

	t.Run("invalid email", func(t *testing.T) {
		service := newTestService(t, &fakeUserRepository{})

		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/auth/forgot-password", strings.NewReader(`{"email":"not-an-email"}`))
		ForgotPasswordAPIHandler(newTestLogger(), service)(w, r)

		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d (%s)", w.Code, w.Body.String())
		}
	})
}

func TestResetPasswordAPIHandler(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		row := &PasswordResetToken{
			ID:        testTokenID,
			UserID:    testUserID,
			TokenHash: HashPasswordResetToken("reset-token"),
			ExpiresAt: time.Now().Add(30 * time.Minute),
			CreatedAt: time.Now(),
		}
		repo := &fakeUserRepository{
			GetPasswordResetTokenByHashFunc: func(ctx context.Context, tokenHash string) (*PasswordResetToken, error) {
				return row, nil
			},
			ConsumePasswordResetTokenFunc: func(ctx context.Context, token *PasswordResetToken, passwordHash string) error {
				return nil
			},
		}
		service := newTestService(t, repo)

		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/auth/reset-password", strings.NewReader(`{"token":"reset-token","password":"brand new password"}`))
		ResetPasswordAPIHandler(newTestLogger(), service)(w, r)

		if w.Code != http.StatusNoContent {
			t.Fatalf("expected 204, got %d (%s)", w.Code, w.Body.String())
		}
	})

	t.Run("invalid token", func(t *testing.T) {
		repo := &fakeUserRepository{
			GetPasswordResetTokenByHashFunc: func(ctx context.Context, tokenHash string) (*PasswordResetToken, error) {
				return nil, nil
			},
		}
		service := newTestService(t, repo)

		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/auth/reset-password", strings.NewReader(`{"token":"unknown","password":"brand new password"}`))
		ResetPasswordAPIHandler(newTestLogger(), service)(w, r)

		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d (%s)", w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "Invalid password reset token") {
			t.Fatalf("unexpected body: %s", w.Body.String())
		}
	})

	t.Run("weak password", func(t *testing.T) {
		service := newTestService(t, &fakeUserRepository{})

		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/auth/reset-password", strings.NewReader(`{"token":"reset-token","password":"short"}`))
		ResetPasswordAPIHandler(newTestLogger(), service)(w, r)

		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d (%s)", w.Code, w.Body.String())
		}
	})
}

func TestLogoutAPIHandler(t *testing.T) {
	t.Run("unknown token is idempotent", func(t *testing.T) {
		repo := &fakeUserRepository{
			GetRefreshTokenByHashFunc: func(ctx context.Context, tokenHash string) (*RefreshToken, error) {
				return nil, nil
			},
		}
		service := newTestService(t, repo)

		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/auth/logout", strings.NewReader(`{"refresh_token":"unknown"}`))
		LogoutAPIHandler(newTestLogger(), service)(w, r)

		if w.Code != http.StatusNoContent {
			t.Fatalf("expected 204, got %d (%s)", w.Code, w.Body.String())
		}
	})

	t.Run("empty token", func(t *testing.T) {
		service := newTestService(t, &fakeUserRepository{})

		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/auth/logout", strings.NewReader(`{}`))
		LogoutAPIHandler(newTestLogger(), service)(w, r)

		if w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400, got %d (%s)", w.Code, w.Body.String())
		}
	})
}
