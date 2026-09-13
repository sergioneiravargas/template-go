package example

import (
	"context"

	"github.com/sergioneiravargas/template-go/internal/platform/queue"
	"github.com/sergioneiravargas/template-go/internal/platform/websocket"
)

// MessageRepository is the persistence seam consumed by Service.
type MessageRepository interface {
	CreateMessage(ctx context.Context, entry *Message, queueMessages ...*queue.Message) error
	GetMessage(ctx context.Context, id string) (*Message, error)
}

// EventPublisher is the subset of *websocket.Hub the service needs to push
// events to socket clients.
type EventPublisher interface {
	Publish(message websocket.Message) error
}

var (
	_ MessageRepository = (*Repository)(nil)
	_ EventPublisher    = (*websocket.Hub)(nil)
)
