package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/sergioneiravargas/template-go/internal/platform/mailer"
	"github.com/sergioneiravargas/template-go/internal/platform/queue"
)

func TestPasswordResetRequestedMessageHandler(t *testing.T) {
	newMessage := func(t *testing.T) *queue.Message {
		t.Helper()

		message, err := queue.NewMessage(MessageNamePasswordResetRequested, MessagePasswordResetRequested{
			UserID: testUserID,
			Token:  "reset-token",
		})
		if err != nil {
			t.Fatalf("failed to create message: %v", err)
		}
		return message
	}

	t.Run("handles only its message name", func(t *testing.T) {
		handler := passwordResetRequestedMessageHandler(newTestService(t, &fakeUserRepository{}), newTestLogger())

		if !handler.CanHandleFunc(newMessage(t)) {
			t.Fatal("expected the handler to accept its message")
		}
		other, err := queue.NewMessage("other_message", struct{}{})
		if err != nil {
			t.Fatalf("failed to create message: %v", err)
		}
		if handler.CanHandleFunc(other) {
			t.Fatal("expected the handler to reject other messages")
		}
	})

	t.Run("success sends the email", func(t *testing.T) {
		user := newTestUser(t, "correct password 123")
		repo := &fakeUserRepository{
			GetUserFunc: func(ctx context.Context, id string) (*User, error) {
				return user, nil
			},
		}
		sent := false
		service := newTestServiceWithMailer(t, repo, &fakeMailer{
			SendFunc: func(ctx context.Context, email mailer.Email) error {
				sent = true
				return nil
			},
		})
		handler := passwordResetRequestedMessageHandler(service, newTestLogger())

		if err := handler.HandlerFunc(context.Background(), newMessage(t)); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !sent {
			t.Fatal("expected the email to be sent")
		}
	})

	t.Run("malformed body is dropped", func(t *testing.T) {
		handler := passwordResetRequestedMessageHandler(newTestService(t, &fakeUserRepository{}), newTestLogger())

		message := &queue.Message{Name: MessageNamePasswordResetRequested, Body: []byte("not-json")}
		if err := handler.HandlerFunc(context.Background(), message); err != nil {
			t.Fatalf("expected malformed message to be dropped, got %v", err)
		}
	})

	t.Run("unknown user is dropped", func(t *testing.T) {
		repo := &fakeUserRepository{
			GetUserFunc: func(ctx context.Context, id string) (*User, error) {
				return nil, nil
			},
		}
		handler := passwordResetRequestedMessageHandler(newTestService(t, repo), newTestLogger())

		if err := handler.HandlerFunc(context.Background(), newMessage(t)); err != nil {
			t.Fatalf("expected unknown user to be dropped, got %v", err)
		}
	})

	t.Run("transient failures are retried", func(t *testing.T) {
		user := newTestUser(t, "correct password 123")
		repo := &fakeUserRepository{
			GetUserFunc: func(ctx context.Context, id string) (*User, error) {
				return user, nil
			},
		}
		service := newTestServiceWithMailer(t, repo, &fakeMailer{
			SendFunc: func(ctx context.Context, email mailer.Email) error {
				return errors.New("ses throttled")
			},
		})
		handler := passwordResetRequestedMessageHandler(service, newTestLogger())

		if err := handler.HandlerFunc(context.Background(), newMessage(t)); err == nil {
			t.Fatal("expected an error so the message is retried")
		}
	})
}
