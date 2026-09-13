package example

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/sergioneiravargas/template-go/internal/platform/queue"

	"github.com/google/uuid"
)

func TestService_CreateMessage(t *testing.T) {
	t.Run("rejects invalid input", func(t *testing.T) {
		repo := &mockMessageRepository{
			CreateMessageFunc: func(ctx context.Context, entry *Message, queueMessages ...*queue.Message) error {
				t.Fatal("repository must not be called")
				return nil
			},
		}
		service := NewService(repo, &mockEventPublisher{}, newTestLogger())

		if _, err := service.CreateMessage(context.Background(), CreateMessageInput{}); err == nil {
			t.Fatal("expected validation error")
		}
	})

	t.Run("persists message with outbox message", func(t *testing.T) {
		var gotMessages []*queue.Message
		var gotEntry *Message
		repo := &mockMessageRepository{
			CreateMessageFunc: func(ctx context.Context, entry *Message, queueMessages ...*queue.Message) error {
				gotEntry = entry
				gotMessages = queueMessages
				return nil
			},
		}
		service := NewService(repo, &mockEventPublisher{}, newTestLogger())

		entry, err := service.CreateMessage(context.Background(), CreateMessageInput{Body: "hello"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if entry.ID == "" || entry.Body != "hello" || entry.CreatedAt.IsZero() {
			t.Fatalf("unexpected message: %+v", entry)
		}
		if gotEntry != entry {
			t.Fatal("repository received a different message")
		}
		if len(gotMessages) != 1 || gotMessages[0].Name != MessageNameMessageCreated {
			t.Fatalf("expected one %s message, got %+v", MessageNameMessageCreated, gotMessages)
		}
		body, ok := queue.DecodeMessage[MessageMessageCreated](gotMessages[0])
		if !ok || body.MessageID != entry.ID {
			t.Fatalf("unexpected message body: %+v", body)
		}
	})

	t.Run("propagates repository error", func(t *testing.T) {
		repoErr := errors.New("boom")
		repo := &mockMessageRepository{
			CreateMessageFunc: func(ctx context.Context, entry *Message, queueMessages ...*queue.Message) error {
				return repoErr
			},
		}
		service := NewService(repo, &mockEventPublisher{}, newTestLogger())

		if _, err := service.CreateMessage(context.Background(), CreateMessageInput{Body: "hello"}); !errors.Is(err, repoErr) {
			t.Fatalf("expected repository error, got %v", err)
		}
	})
}

func TestService_GetMessage(t *testing.T) {
	known := uuid.NewString()
	missing := uuid.NewString()
	repoCalls := 0
	repo := &mockMessageRepository{
		GetMessageFunc: func(ctx context.Context, id string) (*Message, error) {
			repoCalls++
			if id == known {
				return &Message{ID: id, Body: "m"}, nil
			}
			return nil, nil
		},
	}
	service := NewService(repo, &mockEventPublisher{}, newTestLogger())

	if _, err := service.GetMessage(context.Background(), known); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := service.GetMessage(context.Background(), missing); !errors.Is(err, ErrMessageNotFound) {
		t.Fatalf("expected ErrMessageNotFound, got %v", err)
	}
	if _, err := service.GetMessage(context.Background(), "not-a-uuid"); !errors.Is(err, ErrMessageNotFound) {
		t.Fatalf("expected ErrMessageNotFound for malformed id, got %v", err)
	}
	if repoCalls != 2 {
		t.Fatalf("malformed id must not reach the repository, got %d calls", repoCalls)
	}
}

func TestService_BroadcastToRoom(t *testing.T) {
	publisher := &mockEventPublisher{}
	service := NewService(&mockMessageRepository{}, publisher, newTestLogger())

	if err := service.BroadcastToRoom(context.Background(), BroadcastRoomInput{Room: "bad room", UserID: "u", Message: "m"}); err == nil {
		t.Fatal("expected validation error for invalid room")
	}
	if len(publisher.published) != 0 {
		t.Fatal("nothing should be published on validation error")
	}

	if err := service.BroadcastToRoom(context.Background(), BroadcastRoomInput{Room: "lobby", UserID: "u", Message: "m"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(publisher.published) != 1 {
		t.Fatalf("expected one published message, got %d", len(publisher.published))
	}
	if publisher.published[0].Topic != "example:lobby" {
		t.Fatalf("unexpected topic %q", publisher.published[0].Topic)
	}
	var event Event
	if err := json.Unmarshal(publisher.published[0].Body, &event); err != nil {
		t.Fatalf("event body is not JSON: %v", err)
	}
	if event.Type != EventTypeBroadcast {
		t.Fatalf("unexpected event type %q", event.Type)
	}
}

func TestValidateRoom(t *testing.T) {
	tests := []struct {
		room  string
		valid bool
	}{
		{"messages", true},
		{"room_1-A", true},
		{"", false},
		{"has space", false},
		{"slash/room", false},
		{string(make([]byte, 65)), false},
	}
	for _, tt := range tests {
		err := ValidateRoom(tt.room)
		if (err == nil) != tt.valid {
			t.Errorf("ValidateRoom(%q) valid=%v, got err=%v", tt.room, tt.valid, err)
		}
	}
}
