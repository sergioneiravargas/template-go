package websocket

import (
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/sergioneiravargas/template-go/internal/platform/log"
)

// MessageHandler defines a function that can handle incoming websocket messages
type MessageHandler func(conn *Conn, messageBytes []byte) error

// startPingHandler starts a ping handler goroutine
func startPingHandler(conn *websocket.Conn, pingPeriod time.Duration, done <-chan struct{}) {
	ticker := time.NewTicker(pingPeriod)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := WriteMessage(conn, PingMessage, nil); err != nil {
				return
			}
		case <-done:
			return
		}
	}
}

// shouldLogError determines if an error should be logged
func shouldLogError(err error) bool {
	if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
		return false
	}
	// Filter out "use of closed network connection" errors
	errStr := err.Error()
	return !strings.Contains(errStr, "use of closed network connection")
}

// setupConnection configures connection timeouts and handlers
func setupConnection(conn *websocket.Conn) (time.Duration, time.Duration) {
	pongWait := 60 * time.Second
	pingPeriod := (pongWait * 9) / 10

	conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	return pongWait, pingPeriod
}

// registerClient adds a client to the hub
func registerClient(hub *Hub, topic string, client *Client) {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if hub.Topics[topic] == nil {
		hub.Topics[topic] = make(map[*Client]bool)
	}
	hub.Topics[topic][client] = true
}

// cleanupClient removes a client from the hub and closes connection
func cleanupClient(hub *Hub, topic string, client *Client, logger *log.Logger) {
	defer func() {
		if r := recover(); r != nil {
			logger.Error("Panic in websocket handler", log.Context{
				"panic": r,
			})
		}
		hub.mu.Lock()
		if hub.Topics[topic] != nil {
			delete(hub.Topics[topic], client)
		}
		hub.mu.Unlock()
		client.Conn.Close()
		releaseConnLock(client.Conn)
	}()
}

func Handler(
	topic string,
	upgrader *Upgrader,
	hub *Hub,
	logger *log.Logger,
) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		// Upgrade to websocket connection
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			logger.Error("Error upgrading to websocket connection", log.Context{
				"error": err.Error(),
			})
			return
		}

		client := &Client{
			Conn:  conn,
			Topic: topic,
		}

		// Ensure cleanup always happens
		defer cleanupClient(hub, topic, client, logger)

		// Setup connection timeouts and handlers
		_, pingPeriod := setupConnection(conn)

		// Register client with hub
		registerClient(hub, topic, client)

		// Start ping handler
		done := make(chan struct{})
		defer close(done)
		go startPingHandler(conn, pingPeriod, done)

		// Main read loop
		for {
			_, _, err := conn.ReadMessage()
			if err != nil {
				if shouldLogError(err) {
					logger.Error("Error reading websocket message", log.Context{
						"error": err.Error(),
					})
				}
				break
			}
		}
	}
}

// handleMessageLoop processes incoming messages in a loop
func handleMessageLoop(conn *Conn, messageHandler MessageHandler, logger *log.Logger) {
	for {
		_, messageBytes, err := conn.ReadMessage()
		if err != nil {
			if shouldLogError(err) {
				logger.Error("Error reading websocket message", log.Context{
					"error": err.Error(),
				})
			}
			break
		}

		// Process the message using the provided handler
		if messageHandler != nil {
			go func(msgBytes []byte) {
				if err := messageHandler(conn, msgBytes); err != nil {
					logger.Error("Error handling websocket message", log.Context{
						"error": err.Error(),
					})
				}
			}(messageBytes)
		}
	}
}
