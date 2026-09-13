package websocket

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/sergioneiravargas/template-go/internal/platform/amqpx"
	"github.com/sergioneiravargas/template-go/internal/platform/log"
)

const (
	amqpExchange   string = "websocket.messages.broadcast"
	amqpQueue      string = "websocket.messages.queue"
	amqpRoutingKey string = "broadcast"
)

// Message types for websocket communication
const (
	TextMessage = websocket.TextMessage
	PingMessage = websocket.PingMessage
	PongMessage = websocket.PongMessage
)

// Close codes
const (
	CloseNormalClosure   = websocket.CloseNormalClosure
	CloseGoingAway       = websocket.CloseGoingAway
	CloseAbnormalClosure = websocket.CloseAbnormalClosure
)

type Conn = websocket.Conn

type Upgrader = websocket.Upgrader

type Message struct {
	Topic string `json:"topic"`
	Body  []byte `json:"body"`
}

// Client represents a websocket connection
type Client struct {
	Topic string

	Conn *Conn
}

// gorilla/websocket allows a single concurrent writer per connection, but three
// goroutines write to a client: the handler replies from OnMessage, the ping
// keepalive, and the hub broadcast. connLocks serializes them per connection
// so every write path (WriteMessage, SendXMessage, SafeWriteMessage) shares
// one lock keyed by the connection itself, not by the caller's handle to it.
var connLocks sync.Map // *Conn -> *sync.Mutex

func connLock(conn *Conn) *sync.Mutex {
	mu, _ := connLocks.LoadOrStore(conn, &sync.Mutex{})
	return mu.(*sync.Mutex)
}

func releaseConnLock(conn *Conn) {
	connLocks.Delete(conn)
}

// Hub manages clients, topics and messages delivery
type Hub struct {
	Topics map[string]map[*Client]bool // topic -> clients
	mu     sync.Mutex

	AMQPConn amqpx.Conn
}

// declareBroadcastTopology declares the broadcast exchange, queue and binding.
// It is idempotent and re-run on every reconnect when the connection is a
// resilient amqpx.ConnectionManager. This matters especially because the
// broadcast queue is non-durable and does not survive a broker restart.
func declareBroadcastTopology(ch *amqp.Channel) error {
	if err := ch.ExchangeDeclare(
		amqpExchange, // name
		"topic",      // type
		true,         // durable
		false,        // auto-deleted
		false,        // internal
		false,        // no-wait
		nil,          // arguments
	); err != nil {
		return fmt.Errorf("failed to declare exchange: %w", err)
	}

	if _, err := ch.QueueDeclare(
		amqpQueue, // name of the queue
		false,     // durable
		false,     // delete when unused
		false,     // exclusive
		false,     // no-wait
		nil,       // arguments
	); err != nil {
		return fmt.Errorf("failed to declare queue: %w", err)
	}

	if err := ch.QueueBind(
		amqpQueue,      // queue name
		amqpRoutingKey, // routing key
		amqpExchange,   // exchange name
		false,          // no-wait
		nil,            // arguments
	); err != nil {
		return fmt.Errorf("failed to bind queue: %w", err)
	}
	return nil
}

// setupTopology registers the topology declaration for re-application on every
// reconnect when conn is a resilient amqpx.ConnectionManager. For a plain
// connection it declares once.
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

func NewHub(
	amqpConn amqpx.Conn,
) *Hub {
	if err := setupTopology(amqpConn, declareBroadcastTopology); err != nil {
		panic(err.Error())
	}

	return &Hub{
		Topics:   make(map[string]map[*Client]bool),
		AMQPConn: amqpConn,
	}
}

// Sends a message to all clients subscribed to the given topic
func (hub *Hub) BroadcastMessage(message Message) error {
	hub.mu.Lock()
	clients, exists := hub.Topics[message.Topic]
	if !exists {
		hub.mu.Unlock()
		return nil
	}

	// Create a slice of clients to avoid holding the lock during broadcast
	clientList := make([]*Client, 0, len(clients))
	for client := range clients {
		clientList = append(clientList, client)
	}
	hub.mu.Unlock()

	var wg sync.WaitGroup
	ch := make(chan struct{}, 4)
	clientsToRemove := make(chan *Client, len(clientList))

	for _, client := range clientList {
		wg.Add(1)
		ch <- struct{}{} // Acquire a slot
		go func(client *Client) {
			defer wg.Done()
			defer func() {
				<-ch // Release the slot
			}()

			if err := client.SafeWriteMessage(TextMessage, message.Body); err != nil {
				client.Conn.Close()
				clientsToRemove <- client
			}
		}(client)
	}
	wg.Wait()
	close(ch)
	close(clientsToRemove)

	// Remove failed clients after all goroutines complete
	hub.mu.Lock()
	for client := range clientsToRemove {
		if hub.Topics[message.Topic] != nil {
			delete(hub.Topics[message.Topic], client)
		}
	}
	hub.mu.Unlock()

	return nil
}

// Publish sends a message to the messages queue
func (hub *Hub) Publish(message Message) error {
	channel, err := hub.AMQPConn.Channel()
	if err != nil {
		return fmt.Errorf("failed to open a channel: %w", err)
	}
	defer channel.Close()

	encodedMsg, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}

	err = channel.Publish(
		amqpExchange,   // exchange
		amqpRoutingKey, // routing key
		false,          // mandatory
		false,          // immediate
		amqp.Publishing{
			ContentType: "application/json",
			Body:        encodedMsg,
		},
	)
	if err != nil {
		return fmt.Errorf("failed to publish a message: %w", err)
	}

	return nil
}

// ConsumeMessages subscribes to the broadcast queue and hands every message
// to handle as soon as it arrives, running at most concurrency handlers in
// parallel. The consumer blocks on the broker while idle (no polling), and
// prefetch is set to concurrency so the broker never pushes more work than
// the handlers can take: a slow fan-out applies backpressure instead of
// buffering in memory. Deliveries are acknowledged after handle returns,
// whatever the outcome, because broadcasts are best effort.
//
// It returns nil when ctx is cancelled and an error when the subscription is
// lost (channel closed, broker reconnecting); callers re-subscribe with a
// backoff. Unacknowledged deliveries are requeued by the broker on return.
func (hub *Hub) ConsumeMessages(
	ctx context.Context,
	concurrency int,
	logger *log.Logger,
	handle func(message Message),
) error {
	if concurrency <= 0 {
		return fmt.Errorf("concurrency must be greater than 0")
	}

	channel, err := hub.AMQPConn.Channel()
	if err != nil {
		return fmt.Errorf("failed to open a channel: %w", err)
	}
	defer channel.Close()

	if err := channel.Qos(concurrency, 0, false); err != nil {
		return fmt.Errorf("failed to set channel prefetch: %w", err)
	}

	deliveries, err := channel.Consume(
		amqpQueue, // queue
		"",        // consumer
		false,     // auto-ack
		false,     // exclusive
		false,     // no-local
		false,     // no-wait
		nil,       // args
	)
	if err != nil {
		return fmt.Errorf("failed to consume messages: %w", err)
	}

	// Handlers still running when this function returns must finish before
	// the channel is closed, otherwise their acks would fail.
	var wg sync.WaitGroup
	defer wg.Wait()
	slots := make(chan struct{}, concurrency)

	for {
		select {
		case <-ctx.Done():
			return nil
		case delivery, ok := <-deliveries:
			if !ok {
				return fmt.Errorf("broadcast subscription closed")
			}

			var message Message
			if err := json.Unmarshal(delivery.Body, &message); err != nil {
				logger.Error("Dropping malformed broadcast message", log.Context{
					"error": err.Error(),
				})
				_ = delivery.Ack(false)
				continue
			}

			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				// Hand the delivery back so another consumer (or this one
				// after restart) picks it up.
				_ = delivery.Nack(false, true)
				return nil
			}

			wg.Add(1)
			go func(delivery amqp.Delivery, message Message) {
				defer wg.Done()
				defer func() { <-slots }()

				handle(message)

				if err := delivery.Ack(false); err != nil {
					logger.Error("Failed to acknowledge broadcast message", log.Context{
						"error": err.Error(),
						"topic": message.Topic,
					})
				}
			}(delivery, message)
		}
	}
}

// SendJSONMessage sends a JSON message through a websocket connection
func SendJSONMessage(conn *Conn, data any) error {
	messageBytes, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}

	if err := WriteMessage(conn, TextMessage, messageBytes); err != nil {
		return fmt.Errorf("failed to send websocket message: %w", err)
	}

	return nil
}

// SendErrorMessage sends an error message through the websocket connection
func SendErrorMessage(conn *Conn, errorType, message string) error {
	response := map[string]any{
		"type":  errorType,
		"error": message,
	}

	responseBytes, err := json.Marshal(response)
	if err != nil {
		return err
	}

	return WriteMessage(conn, TextMessage, responseBytes)
}

// SendSuccessMessage sends a success message through the websocket connection
func SendSuccessMessage(conn *Conn, messageType string, data any) error {
	response := map[string]any{
		"type": messageType,
		"data": data,
	}

	responseBytes, err := json.Marshal(response)
	if err != nil {
		return err
	}

	return WriteMessage(conn, TextMessage, responseBytes)
}

// IsCloseError checks if the error is a close error with any of the given codes
func IsCloseError(err error, codes ...int) bool {
	return websocket.IsCloseError(err, codes...)
}

// WriteMessage writes a message to the connection with a write deadline,
// serialized with every other writer to the same connection.
func WriteMessage(conn *Conn, messageType int, data []byte) error {
	mu := connLock(conn)
	mu.Lock()
	defer mu.Unlock()
	// Set write deadline to prevent hanging connections
	conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return conn.WriteMessage(messageType, data)
}

// SafeWriteMessage writes a message to a client connection, serialized with
// every other writer to that connection.
func (client *Client) SafeWriteMessage(messageType int, data []byte) error {
	return WriteMessage(client.Conn, messageType, data)
}

// SafeSendJSONMessage safely sends a JSON message through a client connection
func (client *Client) SafeSendJSONMessage(data any) error {
	messageBytes, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}
	return client.SafeWriteMessage(TextMessage, messageBytes)
}

// ReadMessage reads a message from the connection
func ReadMessage(conn *Conn) (messageType int, p []byte, err error) {
	return conn.ReadMessage()
}

// ConnectionHandler defines the interface for handling websocket connections
type ConnectionHandler interface {
	OnConnect(conn *Conn, topic string, userContext any) error
	OnDisconnect(conn *Conn, topic string, userContext any) error
	OnMessage(conn *Conn, messageBytes []byte, topic string, userContext any) error
}

// registerClient adds a client to the hub
func (hub *Hub) registerClient(topic string, client *Client) {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if hub.Topics[topic] == nil {
		hub.Topics[topic] = make(map[*Client]bool)
	}
	hub.Topics[topic][client] = true
}

// unregisterClient removes a client from the hub
func (hub *Hub) unregisterClient(topic string, client *Client) {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if hub.Topics[topic] != nil {
		delete(hub.Topics[topic], client)
	}
}

// GenericHandler creates a websocket handler that uses a ConnectionHandler
func GenericHandler(
	topic string,
	upgrader *Upgrader,
	hub *Hub,
	handler ConnectionHandler,
	userContextProvider func(r *http.Request) (any, error),
	logger *log.Logger,
) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		userContext, conn, client, err := setupWebsocketConnection(w, r, topic, upgrader, userContextProvider, logger)
		if err != nil {
			return
		}

		hub.registerClient(topic, client)
		defer hub.cleanupConnection(topic, client, conn, handler, userContext, logger)

		if err := handler.OnConnect(conn, topic, userContext); err != nil {
			logger.Error("Error in connect handler", log.Context{"error": err.Error()})
			return
		}

		handleConnection(conn, topic, userContext, handler, logger)
	}
}

// setupWebsocketConnection handles the initial websocket setup
func setupWebsocketConnection(
	w http.ResponseWriter,
	r *http.Request,
	topic string,
	upgrader *Upgrader,
	userContextProvider func(r *http.Request) (any, error),
	logger *log.Logger,
) (any, *Conn, *Client, error) {
	userContext, err := userContextProvider(r)
	if err != nil {
		logger.Error("Error getting user context", log.Context{"error": err.Error()})
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return nil, nil, nil, err
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		logger.Error("Error upgrading to websocket connection", log.Context{"error": err.Error()})
		return nil, nil, nil, err
	}

	client := &Client{Conn: conn, Topic: topic}
	return userContext, conn, client, nil
}

// cleanupConnection handles connection cleanup
func (hub *Hub) cleanupConnection(
	topic string,
	client *Client,
	conn *Conn,
	handler ConnectionHandler,
	userContext any,
	logger *log.Logger,
) {
	if r := recover(); r != nil {
		logger.Error("Panic in websocket handler", log.Context{"panic": r})
	}
	if err := handler.OnDisconnect(conn, topic, userContext); err != nil {
		logger.Error("Error in disconnect handler", log.Context{"error": err.Error()})
	}
	hub.unregisterClient(topic, client)
	client.Conn.Close()
	releaseConnLock(client.Conn)
}

// handleConnection manages the websocket connection lifecycle
func handleConnection(
	conn *Conn,
	topic string,
	userContext any,
	handler ConnectionHandler,
	logger *log.Logger,
) {
	_, pingPeriod := setupConnection(conn)

	done := make(chan struct{})
	defer close(done)
	go startPingHandler(conn, pingPeriod, done)

	for {
		_, messageBytes, err := ReadMessage(conn)
		if err != nil {
			if IsCloseError(err, CloseNormalClosure, CloseGoingAway, CloseAbnormalClosure) {
				logger.Debug("Websocket connection closed", log.Context{"topic": topic})
			} else {
				logger.Error("Error reading websocket message", log.Context{"error": err.Error()})
			}
			break
		}

		go func(msgBytes []byte) {
			if err := handler.OnMessage(conn, msgBytes, topic, userContext); err != nil {
				logger.Error("Error handling websocket message", log.Context{"error": err.Error()})
			}
		}(messageBytes)
	}
}

func (hub *Hub) Close() error {
	hub.mu.Lock()
	defer hub.mu.Unlock()

	for topics, clients := range hub.Topics {
		for client := range clients {
			err := client.Conn.Close()
			if err != nil {
				return err
			}
		}
		delete(hub.Topics, topics)
	}

	// The AMQP connection is shared and owned by the ConnectionManager; it is
	// closed separately during shutdown, not here.
	return nil
}
