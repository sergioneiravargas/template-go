package example

import (
	"encoding/json"
	"fmt"

	"github.com/sergioneiravargas/template-go/pkg/framework/log"
	"github.com/sergioneiravargas/template-go/pkg/framework/queue"

	amqp "github.com/rabbitmq/amqp091-go"
)

const (
	QueueExample           = "queue_example"
	QueueMessageLogCreated = "example_log_created"
)

func NewQueue(
	workerCount int,
	logger *log.Logger,
	conn *amqp.Connection,
) *queue.Queue {
	if conn == nil {
		panic("amqp connection is nil")
	}
	if logger == nil {
		panic("logger is nil")
	}

	if workerCount <= 0 {
		panic("worker count must be greater than 0")
	}

	return queue.New(
		QueueExample,
		[]*queue.MessageHandler{
			{
				CanHandleFunc: func(msg *queue.Message) bool {
					// Logic to determine if this handler can process the message
					return msg.Name == QueueMessageLogCreated
				},
				HandlerFunc: func(msg *queue.Message) error {
					// Logic to process the message
					var log Log
					if err := json.Unmarshal(msg.Body, &log); err != nil {
						return fmt.Errorf("failed to decode message payload: %w", err)
					}
					logger.Info("Processing example log created message", map[string]any{
						"log": log,
					})
					return nil
				},
			},
		},
		conn,
		logger,
		queue.WithWorkerCount(workerCount),
	)
}
