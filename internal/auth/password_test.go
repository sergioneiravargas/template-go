package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestHashAndVerifyPassword(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=65536,t=3,p=1$") {
		t.Fatalf("unexpected hash format: %s", hash)
	}

	ok, err := VerifyPassword(hash, "correct horse battery staple")
	if err != nil || !ok {
		t.Fatalf("expected match, got ok=%t err=%v", ok, err)
	}

	ok, err = VerifyPassword(hash, "wrong password")
	if err != nil || ok {
		t.Fatalf("expected mismatch, got ok=%t err=%v", ok, err)
	}
}

func TestHashPassword_UniqueSalts(t *testing.T) {
	a, err := HashPassword("password-123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	b, err := HashPassword("password-123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if a == b {
		t.Fatal("expected different hashes for the same password")
	}
}

func TestVerifyPassword_MalformedHash(t *testing.T) {
	tests := []struct{ name, hash string }{
		{"empty", ""},
		{"not phc", "plaintext"},
		{"wrong algorithm", "$bcrypt$v=19$m=65536,t=3,p=1$c2FsdA$aGFzaA"},
		{"bad params", "$argon2id$v=19$m=abc$c2FsdA$aGFzaA"},
		{"bad salt encoding", "$argon2id$v=19$m=65536,t=3,p=1$!!!$aGFzaA"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := VerifyPassword(tt.hash, "password"); !errors.Is(err, ErrInvalidPasswordHash) {
				t.Fatalf("expected ErrInvalidPasswordHash, got %v", err)
			}
		})
	}
}

func newSemTestService(maxConcurrency int) *Service {
	return NewService(
		Conf{PasswordHashMaxConcurrency: maxConcurrency},
		&fakeUserRepository{},
		&fakeMailer{},
	)
}

func TestNewService_PasswordSemCapacity(t *testing.T) {
	tests := []struct {
		name           string
		maxConcurrency int
		want           int
	}{
		{name: "zero uses the default", maxConcurrency: 0, want: DefaultPasswordHashMaxConcurrency},
		{name: "explicit value is honored", maxConcurrency: 1, want: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := newSemTestService(tt.maxConcurrency)
			if got := cap(service.passwordSem); got != tt.want {
				t.Fatalf("expected semaphore capacity %d, got %d", tt.want, got)
			}
		})
	}

	t.Run("negative value panics", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Fatal("expected a panic")
			}
		}()
		newSemTestService(-1)
	})
}

func TestHashPassword_HonorsSemaphore(t *testing.T) {
	service := newSemTestService(1)
	service.passwordSem <- struct{}{} // saturate the only slot

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := service.hashPassword(context.Background(), "password-123"); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	}()

	select {
	case <-done:
		t.Fatal("expected hashPassword to block while the semaphore is full")
	case <-time.After(100 * time.Millisecond):
	}

	<-service.passwordSem // free the slot

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("expected hashPassword to finish once a slot was free")
	}

	select {
	case service.passwordSem <- struct{}{}:
	default:
		t.Fatal("expected the slot to be released after hashing")
	}
}

func TestPasswordSem_CancelledContext(t *testing.T) {
	tests := []struct {
		name string
		call func(service *Service, ctx context.Context) error
	}{
		{
			name: "hashPassword",
			call: func(service *Service, ctx context.Context) error {
				_, err := service.hashPassword(ctx, "password-123")
				return err
			},
		},
		{
			name: "verifyPassword",
			call: func(service *Service, ctx context.Context) error {
				_, err := service.verifyPassword(ctx, "irrelevant-hash", "password-123")
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := newSemTestService(1)
			service.passwordSem <- struct{}{} // saturate the only slot

			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			if err := tt.call(service, ctx); !errors.Is(err, context.Canceled) {
				t.Fatalf("expected context.Canceled, got %v", err)
			}
		})
	}
}
