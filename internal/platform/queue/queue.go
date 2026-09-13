package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/sergioneiravargas/template-go/internal/platform/amqpx"
	"github.com/sergioneiravargas/template-go/internal/platform/log"
	"github.com/sergioneiravargas/template-go/internal/platform/sql"

	amqp "github.com/rabbitmq/amqp091-go"
)

const (
	amqpExchange           string = "queue.messages"
	amqpDelayedExchange    string = "queue.messages.delayed"
	amqpDeadLetterExchange string = "queue.messages.deadletter"
	MaxRetries             int    = 5 // Default maximum number of retries for a message

	defaultOutboxBatchLimit = 10

	// workerErrorBackoff paces the worker loop after a fetch error (e.g. the
	// AMQP connection is down and reconnecting) so it does not spin in a tight
	// loop hammering a dead connection and flooding logs.
	workerErrorBackoff = 1 * time.Second
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
	HandlerFunc   func(ctx context.Context, msg *Message) error
}

type Queue struct {
	name        string
	handlers    []*MessageHandler
	workerCount int

	amqpConn     amqpx.Conn
	logger       *log.Logger
	shutdownChan chan struct{}
}

// declareQueueTopology declares the exchanges, queue, dead-letter queue and
// their bindings. It is idempotent and is re-run on every reconnect when the
// connection is a resilient amqpx.ConnectionManager.
func declareQueueTopology(name string) func(*amqp.Channel) error {
	return func(ch *amqp.Channel) error {
		if err := ch.ExchangeDeclare(
			amqpExchange, // name
			"direct",     // type
			true,         // durable
			false,        // auto-deleted
			false,        // internal
			false,        // no-wait
			nil,          // arguments
		); err != nil {
			return fmt.Errorf("failed to declare exchange: %w", err)
		}

		if err := ch.ExchangeDeclare(
			amqpDelayedExchange,
			"x-delayed-message", // delayed exchange plugin
			true,
			false,
			false,
			false,
			amqp.Table{
				"x-delayed-type": "direct", // behaves like direct exchange
			},
		); err != nil {
			return fmt.Errorf("failed to declare delayed exchange: %w", err)
		}

		if err := ch.ExchangeDeclare(
			amqpDeadLetterExchange, // name
			"direct",               // type
			true,                   // durable
			false,                  // auto-deleted
			false,                  // internal
			false,                  // no-wait
			nil,                    // arguments
		); err != nil {
			return fmt.Errorf("failed to declare dead letter exchange: %w", err)
		}

		if _, err := ch.QueueDeclare(
			name,  // name of the queue
			true,  // durable
			false, // delete when unused
			false, // exclusive
			false, // no-wait
			nil,   // arguments
		); err != nil {
			return fmt.Errorf("failed to declare queue: %w", err)
		}

		if err := ch.QueueBind(
			name,         // queue name
			name,         // routing key
			amqpExchange, // exchange name
			false,        // no-wait
			nil,          // arguments
		); err != nil {
			return fmt.Errorf("failed to bind queue to exchange: %w", err)
		}

		if err := ch.QueueBind(
			name,                // queue name
			name,                // routing key
			amqpDelayedExchange, // exchange name
			false,               // no-wait
			nil,                 // arguments
		); err != nil {
			return fmt.Errorf("failed to bind queue to delayed exchange: %w", err)
		}

		dlqName := name + ".deadletter"
		if _, err := ch.QueueDeclare(
			dlqName, // name
			true,    // durable
			false,   // delete when unused
			false,   // exclusive
			false,   // no-wait
			nil,     // arguments
		); err != nil {
			return fmt.Errorf("failed to declare dead letter queue: %w", err)
		}

		if err := ch.QueueBind(
			dlqName,                // queue name
			name,                   // routing key
			amqpDeadLetterExchange, // exchange name
			false,                  // no-wait
			nil,                    // arguments
		); err != nil {
			return fmt.Errorf("failed to bind dead letter queue to exchange: %w", err)
		}
		return nil
	}
}

func New(
	name string,
	handlers []*MessageHandler,
	conn amqpx.Conn,
	logger *log.Logger,
	opts ...func(*Queue),
) *Queue {
	for _, handler := range handlers {
		if handler.CanHandleFunc == nil {
			panic("handler function cannot be nil")
		}
		if handler.HandlerFunc == nil {
			panic("handler function cannot be nil")
		}
	}

	if err := setupTopology(conn, declareQueueTopology(name)); err != nil {
		panic(err)
	}

	q := &Queue{
		name:         name,
		handlers:     handlers,
		amqpConn:     conn,
		logger:       logger,
		workerCount:  1,
		shutdownChan: make(chan struct{}),
	}
	for _, opt := range opts {
		opt(q)
	}
	return q
}

// setupTopology registers the topology declaration for re-application on every
// reconnect when conn is a resilient amqpx.ConnectionManager. For a plain
// connection (e.g. tests using a raw *amqp.Connection) it declares once.
func setupTopology(conn amqpx.Conn, declare func(*amqp.Channel) error) error {
	if reg, ok := conn.(amqpx.TopologyRegistrar); ok {
		return reg.RegisterTopology(declare)
	}

	ch, err := conn.Channel()
	if err != nil {
		return fmt.Errorf("failed to open a channel: %w", err)
	}
	defer ch.Close()
	return declare(ch)
}

func (q *Queue) GetName() string {
	return q.name
}

func (q *Queue) Shutdown() error {
	q.logger.Info("Shutting down queue", log.Context{
		"queue": q.name,
	})

	// Close the shutdown channel to signal all workers to stop fetching new messages
	// Workers will finish processing their current messages before stopping
	close(q.shutdownChan)

	q.logger.Info("Queue shutdown initiated, workers will finish current messages", log.Context{
		"queue": q.name,
	})

	return nil
}

func PoolWithDB(
	db *sql.DB,
) func(*Pool) {
	return func(p *Pool) {
		p.db = db
	}
}

func PoolWitOutboxBatchLimit(
	limit int,
) func(*Pool) {
	return func(p *Pool) {
		p.outboxBatchLimit = limit
	}
}

type Pool struct {
	queues []*Queue
	// Optional database connection for outbox message processing
	db     *sql.DB
	logger *log.Logger

	outboxBatchLimit int

	// workStarted is closed when Work begins; queuesDone is closed when every
	// queue worker has finished. Shutdown waits on queuesDone only if Work was
	// started. Outbox consumers stop separately via the ctx passed to Work.
	workStarted chan struct{}
	queuesDone  chan struct{}
}

func NewPool(
	db *sql.DB,
	logger *log.Logger,
	queues []*Queue,
	opts ...func(*Pool),
) *Pool {
	if logger == nil {
		panic("logger is required")
	}
	if len(queues) == 0 {
		panic("at least one queue is required")
	}

	p := Pool{
		queues:           queues,
		db:               db,
		logger:           logger,
		outboxBatchLimit: defaultOutboxBatchLimit,
		workStarted:      make(chan struct{}),
		queuesDone:       make(chan struct{}),
	}
	for _, opt := range opts {
		opt(&p)
	}
	return &p
}

func (p *Pool) AddQueue(queues ...*Queue) {
	p.queues = append(p.queues, queues...)
}

func (p *Pool) GetQueues() []*Queue {
	return p.queues
}

func (p *Pool) FindQueue(name string) *Queue {
	for _, q := range p.queues {
		if q.name == name {
			return q
		}
	}
	return nil
}

func (p *Pool) Dispatch(ctx context.Context, queueName string, msg *Message) error {
	q := p.FindQueue(queueName)
	if q == nil {
		return fmt.Errorf("queue not found: %s", queueName)
	}
	return q.Dispatch(ctx, msg)
}

// Shutdown stops every queue from fetching new messages and waits for in-flight
// handlers to finish, bounded by ctx. Outbox consumers are stopped separately by
// cancelling the context passed to Work (after Shutdown returns), so the graceful
// drain of queue handlers is not cut short.
func (p *Pool) Shutdown(ctx context.Context) error {
	for _, q := range p.GetQueues() {
		if err := q.Shutdown(); err != nil {
			q.logger.Error("Failed to shutdown queue", log.Context{
				"queue": q.name,
				"error": err.Error(),
			})
			return fmt.Errorf("failed to shutdown queue %s: %w", q.name, err)
		}
	}

	select {
	case <-p.workStarted:
	default:
		// Work was never started; nothing to wait for.
		return nil
	}
	select {
	case <-p.queuesDone:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("timed out waiting for queue workers to stop: %w", ctx.Err())
	}
}

// Work runs the outbox consumers and the queue workers. Queue workers stop when
// Shutdown is called (graceful) or ctx is cancelled (hard); outbox consumers
// stop when ctx is cancelled. Work returns once all of them have finished.
func (p *Pool) Work(ctx context.Context) {
	close(p.workStarted)

	var outboxWG sync.WaitGroup
	// Start outbox consumers if db is provided
	if p.db != nil {
		outboxWG.Add(1)
		go func() {
			defer outboxWG.Done()
			p.logger.Info("Starting outbox message consume", nil)
			ConsumeOutboxMessages(ctx, p.db, p, p.logger, p.outboxBatchLimit)
		}()
	}

	// Start workers for each queue
	var queueWG sync.WaitGroup
	for _, q := range p.GetQueues() {
		queueWG.Add(1)
		go func(q *Queue) {
			defer queueWG.Done()
			p.logger.Info("Starting queue work", log.Context{
				"queue": q.name,
			})
			q.Work(ctx)
		}(q)
	}
	queueWG.Wait()
	close(p.queuesDone)
	outboxWG.Wait()
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
func (q *Queue) Dispatch(ctx context.Context, message *Message) error {
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

	err = channel.PublishWithContext(
		ctx,
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

	q.logger.Debug("Message dispatched", log.Context{
		"message_id":   message.ID,
		"message_name": message.Name,
		"message_body": message.Body,
		"delay":        message.Delay,
		"queue":        q.name,
	})

	return nil
}

// subscribe opens a channel with the given prefetch and starts a push consumer
// on the queue. The caller owns the channel and must close it.
func (q *Queue) subscribe(consumerTag string, prefetch int) (*amqp.Channel, <-chan amqp.Delivery, error) {
	channel, err := q.amqpConn.Channel()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to open a channel: %w", err)
	}
	if err := channel.Qos(prefetch, 0, false); err != nil {
		channel.Close()
		return nil, nil, fmt.Errorf("failed to set channel prefetch: %w", err)
	}
	deliveries, err := channel.Consume(
		q.name,      // queue
		consumerTag, // consumer
		false,       // auto-ack
		false,       // exclusive
		false,       // no-local
		false,       // no-wait
		nil,         // args
	)
	if err != nil {
		channel.Close()
		return nil, nil, fmt.Errorf("failed to consume messages: %w", err)
	}
	return channel, deliveries, nil
}

// decodeDelivery turns a broker delivery into a Message. A malformed body is
// acknowledged (dropped) because redelivering it can never succeed.
func (q *Queue) decodeDelivery(delivery amqp.Delivery) (*Message, bool) {
	decodedMsg := new(Message)
	if err := json.Unmarshal(delivery.Body, decodedMsg); err != nil {
		q.logger.Error("Dropping malformed message", log.Context{
			"queue": q.name,
			"error": err.Error(),
		})
		_ = delivery.Ack(false)
		return nil, false
	}
	return decodedMsg, true
}

// FetchMessage blocks until one message is delivered, the queue shuts down or
// ctx is cancelled (both return nil, nil). It does not poll: a temporary push
// consumer waits on the broker. The returned message owns the channel, which
// Ack closes.
func (q *Queue) FetchMessage(
	ctx context.Context,
) (*Message, error) {
	consumerTag := fmt.Sprintf("%s.fetch.%s", q.name, uuid.NewString())
	channel, deliveries, err := q.subscribe(consumerTag, 1)
	if err != nil {
		return nil, err
	}

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
			q.logger.Info("Worker stopped fetching messages due to shutdown", log.Context{
				"queue": q.name,
			})
			return nil, nil
		case delivery, ok := <-deliveries:
			if !ok {
				channel.Close()
				return nil, fmt.Errorf("subscription closed while fetching message")
			}
			// Stop the broker from pushing more before handing this one out.
			if err := channel.Cancel(consumerTag, false); err != nil {
				channel.Close()
				return nil, fmt.Errorf("failed to cancel fetch consumer: %w", err)
			}
			decodedMsg, ok := q.decodeDelivery(delivery)
			if !ok {
				channel.Close()
				return nil, fmt.Errorf("failed to unmarshal message")
			}
			decodedMsg.channel = channel
			decodedMsg.delivery = &delivery
			return decodedMsg, nil
		}
	}
}

// consume is the queue's single long-lived subscription, modeled after
// websocket.Hub.ConsumeMessages: it blocks on the broker while idle and hands
// every delivery to a handler goroutine as soon as it arrives, running at most
// workerCount handlers in parallel. Prefetch is set to workerCount so the
// broker never pushes more work than the handlers can take. It returns nil on
// shutdown or ctx cancellation and an error when the subscription is lost.
func (q *Queue) consume(ctx context.Context) error {
	consumerTag := fmt.Sprintf("%s.consumer.%s", q.name, uuid.NewString())
	channel, deliveries, err := q.subscribe(consumerTag, q.workerCount)
	if err != nil {
		return err
	}
	// Closing the channel makes the broker requeue any unacknowledged delivery.
	defer channel.Close()

	// Handlers still running when this function returns must finish before
	// the channel is closed, otherwise their acks would fail.
	var wg sync.WaitGroup
	defer wg.Wait()
	slots := make(chan struct{}, q.workerCount)

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-q.shutdownChan:
			q.logger.Info("Queue stopped fetching messages due to shutdown", log.Context{
				"queue": q.name,
			})
			return nil
		case delivery, ok := <-deliveries:
			if !ok {
				return fmt.Errorf("subscription closed")
			}
			msg, ok := q.decodeDelivery(delivery)
			if !ok {
				continue
			}

			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				// Hand the delivery back so it is picked up after restart.
				_ = delivery.Nack(false, true)
				return nil
			case <-q.shutdownChan:
				_ = delivery.Nack(false, true)
				q.logger.Info("Queue stopped fetching messages due to shutdown", log.Context{
					"queue": q.name,
				})
				return nil
			}

			// The channel stays open across messages; Ack only closes the
			// channel when the message owns one (FetchMessage path).
			msg.delivery = &delivery
			wg.Add(1)
			go func(msg *Message) {
				defer wg.Done()
				defer func() { <-slots }()
				q.HandleMessage(ctx, msg)
			}(msg)
		}
	}
}

func (q *Queue) HandleMessage(ctx context.Context, m *Message) {
	for _, handler := range q.handlers {
		if handler.CanHandleFunc(m) {
			q.logger.Debug("Handling message", log.Context{
				"message_id":   m.ID,
				"message_name": m.Name,
				"queue":        q.name,
			})
			if err := handler.HandlerFunc(ctx, m); err != nil {
				q.logger.Error("Failed to handle message", log.Context{
					"message_id":   m.ID,
					"message_name": m.Name,
					"queue":        q.name,
					"error":        err.Error(),
				})

				if m.ShouldRetry() {
					if err = q.RetryMessage(ctx, m); err != nil {
						q.logger.Error("Failed to retry message", log.Context{
							"message_id":   m.ID,
							"message_name": m.Name,
							"queue":        q.name,
							"error":        err.Error(),
						})
					}
				} else {
					if dlqErr := q.DeadLetterMessage(ctx, m, err); dlqErr != nil {
						q.logger.Error("Failed to dead-letter message", log.Context{
							"message_id":   m.ID,
							"message_name": m.Name,
							"queue":        q.name,
							"error":        dlqErr.Error(),
						})
					}
				}
			} else {
				q.logger.Debug("Message handled successfully", log.Context{
					"message_id":   m.ID,
					"message_name": m.Name,
					"queue":        q.name,
				})
			}

			if err := m.Ack(); err != nil { // message is acknowledged after either success or failure due to app level retries
				q.logger.Error("Failed to acknowledge message", log.Context{
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

	q.logger.Warn("No handler found for message", log.Context{
		"message_id":   m.ID,
		"message_name": m.Name,
		"queue":        q.name,
	})
}

func (q *Queue) Work(ctx context.Context) {
	if q.workerCount < 1 {
		q.workerCount = 1
	}

	defer q.logger.Info("Queue consumer stopped", log.Context{
		"queue":        q.name,
		"worker_count": q.workerCount,
	})

	for {
		err := q.consume(ctx)
		if err == nil {
			// Shutdown or ctx cancellation: in-flight handlers have already
			// drained inside consume.
			return
		}
		q.logger.Error("Queue subscription lost, resubscribing", log.Context{
			"queue": q.name,
			"error": err.Error(),
		})
		// Back off before retrying so a persistent failure (e.g. the AMQP
		// connection is down and reconnecting) does not spin in a tight
		// loop. Return promptly if shutdown is requested meanwhile.
		select {
		case <-q.shutdownChan:
			return
		case <-ctx.Done():
			return
		case <-time.After(workerErrorBackoff):
		}
	}
}

// RetryMessage retries a message by creating a new message with an exponential backoff delay
// and dispatching it to the queue. It repeats until the maximum retries is reached.
func (q *Queue) RetryMessage(ctx context.Context, m *Message) error {
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
		q.logger.Error("Failed to create retry message", log.Context{
			"message_id":   retryMessage.ID,
			"message_name": retryMessage.Name,
			"queue":        q.name,
			"retry_count":  retryMessage.RetryCount,
			"error":        err.Error(),
		})
		return fmt.Errorf("failed to create retry message: %w", err)
	}

	if err = q.Dispatch(ctx, retryMessage); err != nil {
		q.logger.Error("Failed to dispatch retry message", log.Context{
			"message_id":   retryMessage.ID,
			"message_name": retryMessage.Name,
			"queue":        q.name,
			"retry_count":  retryMessage.RetryCount,
			"error":        err.Error(),
		})
		return fmt.Errorf("failed to dispatch retry message: %w", err)
	}

	q.logger.Debug("Retry message dispatched", log.Context{
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
	return 30000 * int(math.Pow(2, float64(retryCount-1)))
}

// DeadLetterMessage publishes a message that has failed processing max times to the dead-letter exchange
func (q *Queue) DeadLetterMessage(ctx context.Context, message *Message, reason error) error {
	encodedMsg, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}

	channel, err := q.amqpConn.Channel()
	if err != nil {
		return fmt.Errorf("failed to open a channel: %w", err)
	}
	defer channel.Close()

	headers := amqp.Table{}
	if reason != nil {
		headers["x-death-reason"] = reason.Error()
	}

	err = channel.PublishWithContext(
		ctx,
		amqpDeadLetterExchange, // exchange
		q.name,                 // routing key
		true,                   // mandatory
		false,                  // immediate
		amqp.Publishing{
			ContentType:  "application/json",
			Body:         encodedMsg,
			DeliveryMode: amqp.Persistent,
			Headers:      headers,
		},
	)
	if err != nil {
		return fmt.Errorf("failed to publish dead-letter message: %w", err)
	}

	q.logger.Warn("Message dead-lettered", log.Context{
		"message_id":   message.ID,
		"message_name": message.Name,
		"queue":        q.name,
		"error":        reason.Error(),
	})

	return nil
}
