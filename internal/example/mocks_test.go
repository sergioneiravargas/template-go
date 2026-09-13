package example

import (
	"context"
	"io"

	"github.com/sergioneiravargas/template-go/internal/platform/log"
	"github.com/sergioneiravargas/template-go/internal/platform/queue"
	"github.com/sergioneiravargas/template-go/internal/platform/websocket"
)

type mockMessageRepository struct {
	CreateMessageFunc func(ctx context.Context, entry *Message, queueMessages ...*queue.Message) error
	GetMessageFunc    func(ctx context.Context, id string) (*Message, error)
}

func (m *mockMessageRepository) CreateMessage(ctx context.Context, entry *Message, queueMessages ...*queue.Message) error {
	return m.CreateMessageFunc(ctx, entry, queueMessages...)
}

func (m *mockMessageRepository) GetMessage(ctx context.Context, id string) (*Message, error) {
	return m.GetMessageFunc(ctx, id)
}

type mockEventPublisher struct {
	PublishFunc func(message websocket.Message) error
	published   []websocket.Message
}

func (m *mockEventPublisher) Publish(message websocket.Message) error {
	m.published = append(m.published, message)
	if m.PublishFunc != nil {
		return m.PublishFunc(message)
	}
	return nil
}

func newTestLogger() *log.Logger {
	return log.NewLogger("test", log.NewHandler(io.Discard, "dev"))
}
