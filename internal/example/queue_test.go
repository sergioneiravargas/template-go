package example

import (
	"context"
	"errors"
	"testing"

	"github.com/sergioneiravargas/template-go/internal/platform/queue"
	"github.com/sergioneiravargas/template-go/internal/platform/websocket"
)

func TestMessageCreatedMessageHandler(t *testing.T) {
	newMessage := func(t *testing.T, body any) *queue.Message {
		t.Helper()
		m, err := queue.NewMessage(MessageNameMessageCreated, body)
		if err != nil {
			t.Fatalf("failed to build message: %v", err)
		}
		return m
	}

	t.Run("can handle only its message name", func(t *testing.T) {
		handler := messageCreatedMessageHandler(NewService(&mockMessageRepository{}, &mockEventPublisher{}, newTestLogger()), newTestLogger())
		if !handler.CanHandleFunc(&queue.Message{Name: MessageNameMessageCreated}) {
			t.Fatal("expected handler to accept its message")
		}
		if handler.CanHandleFunc(&queue.Message{Name: "other"}) {
			t.Fatal("expected handler to reject other messages")
		}
	})

	t.Run("drops malformed payload", func(t *testing.T) {
		publisher := &mockEventPublisher{}
		handler := messageCreatedMessageHandler(NewService(&mockMessageRepository{}, publisher, newTestLogger()), newTestLogger())

		m := &queue.Message{Name: MessageNameMessageCreated, Body: []byte("not json")}
		if err := handler.HandlerFunc(context.Background(), m); err != nil {
			t.Fatalf("malformed payload must be dropped, got %v", err)
		}
		if len(publisher.published) != 0 {
			t.Fatal("nothing should be published")
		}
	})

	t.Run("retries on repository error", func(t *testing.T) {
		repoErr := errors.New("db down")
		repo := &mockMessageRepository{GetMessageFunc: func(ctx context.Context, id string) (*Message, error) { return nil, repoErr }}
		handler := messageCreatedMessageHandler(NewService(repo, &mockEventPublisher{}, newTestLogger()), newTestLogger())

		if err := handler.HandlerFunc(context.Background(), newMessage(t, MessageMessageCreated{MessageID: "x"})); !errors.Is(err, repoErr) {
			t.Fatalf("expected repository error, got %v", err)
		}
	})

	t.Run("drops missing message", func(t *testing.T) {
		repo := &mockMessageRepository{GetMessageFunc: func(ctx context.Context, id string) (*Message, error) { return nil, nil }}
		publisher := &mockEventPublisher{}
		handler := messageCreatedMessageHandler(NewService(repo, publisher, newTestLogger()), newTestLogger())

		if err := handler.HandlerFunc(context.Background(), newMessage(t, MessageMessageCreated{MessageID: "x"})); err != nil {
			t.Fatalf("missing message must be dropped, got %v", err)
		}
		if len(publisher.published) != 0 {
			t.Fatal("nothing should be published")
		}
	})

	t.Run("publishes message created event", func(t *testing.T) {
		repo := &mockMessageRepository{GetMessageFunc: func(ctx context.Context, id string) (*Message, error) { return &Message{ID: id, Body: "m"}, nil }}
		publisher := &mockEventPublisher{}
		handler := messageCreatedMessageHandler(NewService(repo, publisher, newTestLogger()), newTestLogger())

		if err := handler.HandlerFunc(context.Background(), newMessage(t, MessageMessageCreated{MessageID: "x"})); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(publisher.published) != 1 || publisher.published[0].Topic != RoomTopic(MessagesRoom) {
			t.Fatalf("expected one event on %s, got %+v", RoomTopic(MessagesRoom), publisher.published)
		}
	})

	t.Run("retries when publish fails", func(t *testing.T) {
		repo := &mockMessageRepository{GetMessageFunc: func(ctx context.Context, id string) (*Message, error) { return &Message{ID: id}, nil }}
		pubErr := errors.New("amqp down")
		publisher := &mockEventPublisher{PublishFunc: func(websocket.Message) error { return pubErr }}
		handler := messageCreatedMessageHandler(NewService(repo, publisher, newTestLogger()), newTestLogger())

		if err := handler.HandlerFunc(context.Background(), newMessage(t, MessageMessageCreated{MessageID: "x"})); !errors.Is(err, pubErr) {
			t.Fatalf("expected publish error, got %v", err)
		}
	})
}
