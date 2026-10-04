package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/sergioneiravargas/template-go/internal/platform/amqpx"
	"github.com/sergioneiravargas/template-go/internal/platform/log"
	"github.com/sergioneiravargas/template-go/internal/platform/queue"
)

const QueueName = "auth.users.queue"

// NewQueue declares the auth queue topology. Handlers are attached later by
// the mains (queue.WithMessageHandlers) because they need the Service, which
// in turn is built after the queue pool.
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

// MessageHandlers returns every handler bound to the auth queue.
func MessageHandlers(
	service *Service,
	logger *log.Logger,
) []*queue.MessageHandler {
	return []*queue.MessageHandler{
		passwordResetRequestedMessageHandler(service, logger),
	}
}

func passwordResetRequestedMessageHandler(service *Service, logger *log.Logger) *queue.MessageHandler {
	return &queue.MessageHandler{
		CanHandleFunc: func(m *queue.Message) bool {
			return m.Name == MessageNamePasswordResetRequested
		},
		HandlerFunc: func(ctx context.Context, m *queue.Message) error {
			msgBody, ok := queue.DecodeMessage[MessagePasswordResetRequested](m)
			if !ok {
				logger.Error("Failed to decode password reset requested queue message", log.Context{
					"queue_message_id": m.ID,
					"error":            "invalid message body",
				})
				return nil
			}

			err := service.SendPasswordResetEmail(ctx, SendPasswordResetEmailInput{
				UserID: msgBody.UserID,
				Token:  msgBody.Token,
			})
			if errors.Is(err, ErrUserNotFound) {
				logger.Warn("User not found, dropping queue message", log.Context{
					"queue_message_id": m.ID,
					"user_id":          msgBody.UserID,
				})
				return nil
			}
			if err != nil {
				return fmt.Errorf("failed to send password reset email: %w", err)
			}
			return nil
		},
	}
}

const MessageNamePasswordResetRequested = "password_reset_requested"

// MessagePasswordResetRequested carries the raw reset token besides the user
// id: only the token's hash is persisted, so the handler cannot re-derive
// the email link from stored state.
type MessagePasswordResetRequested struct {
	UserID string `json:"user_id"`
	Token  string `json:"token"`
}
