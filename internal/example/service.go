package example

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/sergioneiravargas/template-go/internal/platform/log"
	"github.com/sergioneiravargas/template-go/internal/platform/queue"
	"github.com/sergioneiravargas/template-go/internal/platform/websocket"

	"github.com/google/uuid"
)

var ErrMessageNotFound = errors.New("message not found")

type Service struct {
	repository MessageRepository
	hub        EventPublisher
	logger     *log.Logger
}

func NewService(repository MessageRepository, hub EventPublisher, logger *log.Logger) *Service {
	if repository == nil {
		panic("repository is required")
	}
	if hub == nil {
		panic("hub is required")
	}
	if logger == nil {
		panic("logger is required")
	}
	return &Service{
		repository: repository,
		hub:        hub,
		logger:     logger,
	}
}

// CreateMessage persists the example and enqueues the message_created message through
// the outbox, in the same transaction.
func (s *Service) CreateMessage(ctx context.Context, input CreateMessageInput) (*Message, error) {
	if err := input.Validate(); err != nil {
		return nil, err
	}

	entry := &Message{
		ID:        uuid.NewString(),
		Body:      input.Body,
		CreatedAt: time.Now(),
	}

	message, err := queue.NewMessage(MessageNameMessageCreated, MessageMessageCreated{MessageID: entry.ID})
	if err != nil {
		return nil, fmt.Errorf("failed to build message created queue message: %w", err)
	}

	if err := s.repository.CreateMessage(ctx, entry, message); err != nil {
		return nil, err
	}

	return entry, nil
}

func (s *Service) GetMessage(ctx context.Context, id string) (*Message, error) {
	// IDs are UUIDs; anything else cannot exist, and letting it reach the
	// database would surface a cast error as a 500 instead of a 404.
	if err := uuid.Validate(id); err != nil {
		return nil, ErrMessageNotFound
	}

	entry, err := s.repository.GetMessage(ctx, id)
	if err != nil {
		return nil, err
	}
	if entry == nil {
		return nil, ErrMessageNotFound
	}
	return entry, nil
}

// BroadcastToRoom pushes a message to every socket client subscribed to the room.
func (s *Service) BroadcastToRoom(ctx context.Context, input BroadcastRoomInput) error {
	if err := input.Validate(); err != nil {
		return err
	}

	return s.publishEvent(RoomTopic(input.Room), Event{
		Type: EventTypeBroadcast,
		Data: map[string]any{
			"room":    input.Room,
			"user_id": input.UserID,
			"message": input.Message,
			"sent_at": time.Now(),
		},
	})
}

// NotifyMessageCreated pushes the message to the clients subscribed to the messages room.
func (s *Service) NotifyMessageCreated(ctx context.Context, entry *Message) error {
	if entry == nil {
		return fmt.Errorf("message is required")
	}

	return s.publishEvent(RoomTopic(MessagesRoom), Event{
		Type: EventTypeMessageCreated,
		Data: entry,
	})
}

// publishEvent is context-free because Hub.Publish is; the ctx accepted by
// the public methods above keeps the service signature uniform.
func (s *Service) publishEvent(topic string, event Event) error {
	body, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("failed to encode event: %w", err)
	}

	if err := s.hub.Publish(websocket.Message{Topic: topic, Body: body}); err != nil {
		return fmt.Errorf("failed to publish event: %w", err)
	}

	return nil
}
