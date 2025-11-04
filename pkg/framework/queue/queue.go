package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/sergioneiravargas/template-go/pkg/framework/log"

	amqp "github.com/rabbitmq/amqp091-go"
)

const (
	amqpExchange        string = "queue.messages"
	amqpDelayedExchange string = "queue.messages.delayed"
	MaxRetries          int    = 3 // Default maximum number of retries for a message
)

type MessageOption func(*Message)

func MessageWithID(id string) MessageOption {
	return func(m *Message) {
		m.ID = id
	}
}

func MessageWithDelay(delayMs int) MessageOption {
	return func(m *Message) {
		m.Delay = delayMs
	}
}

func MessageWithMaxRetries(maxRetries int) MessageOption {
	return func(m *Message) {
		m.MaxRetries = maxRetries
	}
}

func MessageWithRetryCount(retryCount int) MessageOption {
	return func(m *Message) {
		m.RetryCount = retryCount
	}
}

type Message struct {
	// The unique ID of the message
	ID string `json:"id"`
	// The name of the message
	Name string `json:"name"`
	// The JSON encoded content of the message
	Body []byte `json:"body"`
	// The delay in milliseconds before the message is processed
	Delay int `json:"delay"`

	// RetryCount is the number of times the message has been retried
	RetryCount int `json:"retry_count"`
	// MaxRetries is the maximum number of times the message can be retried
	MaxRetries int `json:"max_retries"`

	// The AMQP channel used to fetch the message, it must be closed after acknowledging the message
	channel *amqp.Channel `json:"-"`
	// The AMQP delivery object, it is used to acknowledge the message after processing
	delivery *amqp.Delivery `json:"-"`
}

// Ack acknowledges the message, indicating that it has been successfully processed
// It is important to call this method after processing the message to remove it from the queue
func (m *Message) Ack() error {
	defer func() {
		if m.channel != nil {
			m.channel.Close()
			m.channel = nil
		}
	}()

	if m.delivery == nil {
		return fmt.Errorf("delivery is nil, cannot acknowledge message")
	}

	if err := m.delivery.Ack(false); err != nil {
		return fmt.Errorf("failed to acknowledge message: %w", err)
	}
	return nil
}

func (m *Message) ShouldRetry() bool {
	return m.RetryCount < m.MaxRetries
}

func NewMessage(
	name string,
	body any,
	opts ...MessageOption,
) (*Message, error) {
	encodedBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("failed to encode message body: %w", err)
	}
	msg := Message{
		ID:         uuid.NewString(),
		Name:       name,
		Body:       encodedBody,
		MaxRetries: MaxRetries,
	}
	for _, opt := range opts {
		opt(&msg)
	}

	return &msg, nil
}

func DecodeMessage[T any](m *Message) (T, bool) {
	var decoded T
	err := json.Unmarshal(m.Body, &decoded)
	if err != nil {
		var zero T
		return zero, false
	}
	return decoded, true
}

type MessageHandler struct {
	CanHandleFunc func(msg *Message) bool
	HandlerFunc   func(msg *Message) error
}

type Queue struct {
	name        string
	handlers    []*MessageHandler
	workerCount int

	amqpConn     *amqp.Connection
	logger       *log.Logger
	shutdownChan chan struct{}
}

func New(
	name string,
	handlers []*MessageHandler,
	amqpConn *amqp.Connection,
	logger *log.Logger,
) *Queue {
	ch, err := amqpConn.Channel()
	if err != nil {
		panic(fmt.Errorf("failed to open a channel: %w", err))
	}

	for _, handler := range handlers {
		if handler.CanHandleFunc == nil {
			panic("handler function cannot be nil")
		}
		if handler.HandlerFunc == nil {
			panic("handler function cannot be nil")
		}
	}

	err = ch.ExchangeDeclare(
		amqpExchange, // name
		"direct",     // type
		true,         // durable
		false,        // auto-deleted
		false,        // internal
		false,        // no-wait
		nil,          // arguments
	)
	if err != nil {
		panic(fmt.Errorf("failed to declare exchange: %w", err))
	}

	err = ch.ExchangeDeclare(
		amqpDelayedExchange,
		"x-delayed-message", // delayed exchange plugin
		true,
		false,
		false,
		false,
		amqp.Table{
			"x-delayed-type": "direct", // behaves like direct exchange
		},
	)
	if err != nil {
		panic(fmt.Errorf("failed to declare delayed exchange: %w", err))
	}

	_, err = ch.QueueDeclare(
		name,  // name of the queue
		true,  // durable
		false, // delete when unused
		false, // exclusive
		false, // no-wait
		nil,   // arguments
	)
	if err != nil {
		panic(fmt.Errorf("failed to declare queue: %w", err))
	}

	err = ch.QueueBind(
		name,         // queue name
		name,         // routing key
		amqpExchange, // exchange name
		false,        // no-wait
		nil,          // arguments
	)
	if err != nil {
		panic(fmt.Errorf("failed to bind queue to exchange: %w", err))
	}

	err = ch.QueueBind(
		name,                // queue name
		name,                // routing key
		amqpDelayedExchange, // exchange name
		false,               // no-wait
		nil,                 // arguments
	)
	if err != nil {
		panic(fmt.Errorf("failed to bind queue to delayed exchange: %w", err))
	}

	return &Queue{
		name:         name,
		handlers:     handlers,
		amqpConn:     amqpConn,
		logger:       logger,
		workerCount:  1,
		shutdownChan: make(chan struct{}),
	}
}

func (q *Queue) Shutdown() error {
	q.logger.Info("Shutting down queue", map[string]any{
		"queue": q.name,
	})

	// Close the shutdown channel to signal all workers to stop fetching new messages
	// Workers will finish processing their current messages before stopping
	close(q.shutdownChan)

	q.logger.Info("Queue shutdown initiated, workers will finish current messages", map[string]any{
		"queue": q.name,
	})

	return nil
}

type Pool []*Queue

func NewPool(
	queues ...*Queue,
) *Pool {
	p := Pool(queues)
	return &p
}

func (p *Pool) AddQueue(q *Queue) {
	*p = append(*p, q)
}

func (p *Pool) GetQueues() []*Queue {
	return []*Queue(*p)
}

func (p *Pool) FindQueue(name string) *Queue {
	for _, q := range *p {
		if q.name == name {
			return q
		}
	}
	return nil
}

func (p *Pool) Dispatch(queueName string, msg *Message) error {
	q := p.FindQueue(queueName)
	if q == nil {
		return fmt.Errorf("queue not found: %s", queueName)
	}
	return q.Dispatch(msg)
}

func (p *Pool) Shutdown() error {
	for _, q := range p.GetQueues() {
		if err := q.Shutdown(); err != nil {
			q.logger.Error("Failed to shutdown queue", map[string]any{
				"queue": q.name,
				"error": err.Error(),
			})
			return fmt.Errorf("failed to shutdown queue %s: %w", q.name, err)
		}
	}
	return nil
}

func (p *Pool) Work() {
	var wg sync.WaitGroup
	for _, q := range p.GetQueues() {
		wg.Add(1)
		go func(q *Queue) {
			defer wg.Done()
			q.Work()
		}(q)
	}
	wg.Wait()
}

func WithMessageHandlers(
	handlers ...*MessageHandler,
) func(*Queue) {
	return func(q *Queue) {
		q.handlers = handlers
	}
}

func WithWorkerCount(
	count int,
) func(*Queue) {
	return func(q *Queue) {
		q.workerCount = count
	}
}

// Dispatch sends a message to the messages queue
func (q *Queue) Dispatch(message *Message) error {
	encodedMsg, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}

	channel, err := q.amqpConn.Channel()
	if err != nil {
		return fmt.Errorf("failed to open a channel: %w", err)
	}
	defer channel.Close()

	exchange := amqpExchange
	if message.Delay > 0 {
		exchange = amqpDelayedExchange
	}

	headers := amqp.Table{}
	if message.Delay > 0 {
		headers["x-delay"] = message.Delay
	}

	err = channel.Publish(
		exchange, // exchange
		q.name,   // routing key
		true,     // mandatory
		false,    // immediate
		amqp.Publishing{
			ContentType:  "application/json",
			Body:         encodedMsg,
			DeliveryMode: amqp.Persistent,
			Headers:      headers,
		},
	)
	if err != nil {
		return fmt.Errorf("failed to publish a message: %w", err)
	}

	q.logger.Info("Message dispatched", map[string]any{
		"message_id":   message.ID,
		"message_name": message.Name,
		"message_body": message.Body,
		"delay":        message.Delay,
		"queue":        q.name,
	})

	return nil
}

func (q *Queue) FetchMessage(
	ctx context.Context,
) (*Message, error) {
	channel, err := q.amqpConn.Channel()
	if err != nil {
		return nil, fmt.Errorf("failed to open a channel: %w", err)
	}

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			channel.Close()
			return nil, nil
		case <-q.shutdownChan:
			// Shutdown initiated, stop fetching new messages
			if err := channel.Close(); err != nil {
				return nil, fmt.Errorf("failed to close channel during shutdown: %w", err)
			}
			q.logger.Info("Worker stopped fetching messages due to shutdown", map[string]any{
				"queue": q.name,
			})
			return nil, nil
		case <-ticker.C:
			msg, ok, err := channel.Get(q.name, false)
			if err != nil {
				return nil, fmt.Errorf("failed to get message: %w", err)
			}
			if !ok {
				continue
			}
			decodedMsg := new(Message)
			if err := json.Unmarshal(msg.Body, decodedMsg); err != nil {
				return nil, fmt.Errorf("failed to unmarshal message: %w", err)
			}

			decodedMsg.channel = channel
			decodedMsg.delivery = &msg
			return decodedMsg, nil
		}
	}
}

func (q *Queue) HandleMessage(m *Message) {
	for _, handler := range q.handlers {
		if handler.CanHandleFunc(m) {
			q.logger.Info("Handling message", map[string]any{
				"message_id":   m.ID,
				"message_name": m.Name,
				"queue":        q.name,
			})
			if err := handler.HandlerFunc(m); err != nil {
				q.logger.Error("Failed to handle message", map[string]any{
					"message_id":   m.ID,
					"message_name": m.Name,
					"queue":        q.name,
					"error":        err.Error(),
				})

				if m.ShouldRetry() {
					if err = q.RetryMessage(m); err != nil {
						q.logger.Error("Failed to retry message", map[string]any{
							"message_id":   m.ID,
							"message_name": m.Name,
							"queue":        q.name,
							"error":        err.Error(),
						})
					}
				}
			} else {
				q.logger.Info("Message handled successfully", map[string]any{
					"message_id":   m.ID,
					"message_name": m.Name,
					"queue":        q.name,
				})
			}

			if err := m.Ack(); err != nil { // message is acknowledged after either success or failure due to app level retries
				q.logger.Error("Failed to acknowledge message", map[string]any{
					"message_id":   m.ID,
					"message_name": m.Name,
					"queue":        q.name,
					"error":        err.Error(),
				})
				return
			}
			return
		}
	}

	q.logger.Warn("No handler found for message", map[string]any{
		"message_id":   m.ID,
		"message_name": m.Name,
		"queue":        q.name,
	})
}

func (q *Queue) Work() {
	for {
		msg, err := q.FetchMessage(context.Background())
		if err != nil {
			q.logger.Error("Error fetching messages", map[string]any{
				"error": err.Error(),
			})
			continue
		}
		if msg != nil {
			q.HandleMessage(msg)
		}
	}
}

// RetryMessage retries a message by creating a new message with an exponential backoff delay
// and dispatching it to the queue. It repeats until the maximum retries is reached.
func (q *Queue) RetryMessage(m *Message) error {
	retryCount := m.RetryCount + 1
	delay := calculateExponentialBackoff(retryCount)

	retryMessage, err := NewMessage(
		m.Name,
		m.Body,
		MessageWithID(m.ID),
		MessageWithDelay(delay),
		MessageWithRetryCount(retryCount),
		MessageWithMaxRetries(m.MaxRetries),
	)
	if err != nil {
		q.logger.Error("Failed to create retry message", map[string]any{
			"message_id":   retryMessage.ID,
			"message_name": retryMessage.Name,
			"queue":        q.name,
			"retry_count":  retryMessage.RetryCount,
			"error":        err.Error(),
		})
		return fmt.Errorf("failed to create retry message: %w", err)
	}

	if err = q.Dispatch(retryMessage); err != nil {
		q.logger.Error("Failed to dispatch retry message", map[string]any{
			"message_id":   retryMessage.ID,
			"message_name": retryMessage.Name,
			"queue":        q.name,
			"retry_count":  retryMessage.RetryCount,
			"error":        err.Error(),
		})
		return fmt.Errorf("failed to dispatch retry message: %w", err)
	}

	q.logger.Info("Retry message dispatched", map[string]any{
		"message_id":   retryMessage.ID,
		"message_name": retryMessage.Name,
		"queue":        q.name,
		"retry_count":  retryMessage.RetryCount,
		"delay":        delay,
	})

	return nil
}

// Return the delay in milliseconds for the exponential backoff based on the retry count
func calculateExponentialBackoff(retryCount int) int {
	if retryCount <= 0 {
		return 0
	}
	return 100 * int(math.Pow(2, float64(retryCount-1)))
}
