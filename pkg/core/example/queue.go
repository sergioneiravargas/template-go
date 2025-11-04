package example

import (
	"time"

	"github.com/sergioneiravargas/template-go/pkg/framework/log"
	"github.com/sergioneiravargas/template-go/pkg/framework/queue"

	amqp "github.com/rabbitmq/amqp091-go"
)

const (
	QueueExample        = "queue_example"
	QueueMessageExample = "example_message"
)

func NewQueue(
	logger *log.Logger,
	conn *amqp.Connection,
) *queue.Queue {
	if conn == nil {
		panic("amqp connection is nil")
	}
	if logger == nil {
		panic("logger is nil")
	}

	return queue.New(
		QueueExample,
		[]*queue.MessageHandler{
			&queue.MessageHandler{
				CanHandleFunc: func(msg *queue.Message) bool {
					// Logic to determine if this handler can process the message
					return msg.Name == QueueMessageExample
				},
				HandlerFunc: func(msg *queue.Message) error {
					// Logic to process the message
					logger.Info("Processing example message", map[string]any{
						"message_body": string(msg.Body),
					})
					time.Sleep(2 * time.Second) // Simulate processing time
					return nil
				},
			},
		},
		conn,
		logger,
	)
}
