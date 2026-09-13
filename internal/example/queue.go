package example

import (
	"context"
	"fmt"

	"github.com/sergioneiravargas/template-go/internal/platform/amqpx"
	"github.com/sergioneiravargas/template-go/internal/platform/log"
	"github.com/sergioneiravargas/template-go/internal/platform/queue"
)

const (
	QueueName = "example.messages.queue"
)

// NewQueue declares the example queue topology. Handlers are attached later
// by the mains (queue.WithMessageHandlers) because they need the Service,
// which in turn is built after the queue pool.
func NewQueue(
	workerCount int,
	logger *log.Logger,
	conn amqpx.Conn,
) *queue.Queue {
	if conn == nil {
		panic("amqp connection is nil")
	}
	if logger == nil {
		panic("logger is nil")
	}

	return queue.New(
		QueueName,
		[]*queue.MessageHandler{},
		conn,
		logger,
		queue.WithWorkerCount(workerCount),
	)
}

// MessageHandlers returns every handler bound to the example queue.
func MessageHandlers(
	service *Service,
	logger *log.Logger,
) []*queue.MessageHandler {
	return []*queue.MessageHandler{
		messageCreatedMessageHandler(service, logger),
	}
}

const MessageNameMessageCreated = "message_created"

// Payloads carry IDs only; handlers re-fetch current state so stale or
// duplicated deliveries are harmless.
type MessageMessageCreated struct {
	MessageID string `json:"message_id"`
}

func messageCreatedMessageHandler(
	service *Service,
	logger *log.Logger,
) *queue.MessageHandler {
	return &queue.MessageHandler{
		CanHandleFunc: func(m *queue.Message) bool {
			return m.Name == MessageNameMessageCreated
		},
		HandlerFunc: func(ctx context.Context, m *queue.Message) error {
			msgBody, ok := queue.DecodeMessage[MessageMessageCreated](m)
			if !ok {
				logger.Error("Failed to decode message created queue message", log.Context{
					"queue_message_id": m.ID,
					"error":            "invalid message body",
				})
				return nil
			}

			entry, err := service.repository.GetMessage(ctx, msgBody.MessageID)
			if err != nil {
				logger.Error("Failed to get message", log.Context{
					"queue_message_id": m.ID,
					"message_id":       msgBody.MessageID,
					"error":            err.Error(),
				})
				return fmt.Errorf("failed to get message: %w", err)
			}
			if entry == nil {
				logger.Warn("Message not found, dropping queue message", log.Context{
					"queue_message_id": m.ID,
					"message_id":       msgBody.MessageID,
				})
				return nil
			}

			logger.Info("Message created", log.Context{
				"message_id": entry.ID,
				"body":       entry.Body,
			})

			if err := service.NotifyMessageCreated(ctx, entry); err != nil {
				logger.Error("Failed to notify message created", log.Context{
					"queue_message_id": m.ID,
					"message_id":       entry.ID,
					"error":            err.Error(),
				})
				return fmt.Errorf("failed to notify message created: %w", err)
			}

			return nil
		},
	}
}
