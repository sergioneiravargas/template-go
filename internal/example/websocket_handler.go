package example

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/sergioneiravargas/template-go/internal/auth"
	"github.com/sergioneiravargas/template-go/internal/platform/log"
	"github.com/sergioneiravargas/template-go/internal/platform/websocket"

	"github.com/go-chi/chi/v5"
)

// WebsocketMessage is the client -> server frame format.
type WebsocketMessage struct {
	ID   string          `json:"id"`
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

const (
	WebsocketMessageTypePing      = "ping"
	WebsocketMessageTypeEcho      = "echo"
	WebsocketMessageTypeBroadcast = "broadcast"
)

// RoomWebsocketConnectionHandler handles websocket connections for example rooms.
type RoomWebsocketConnectionHandler struct {
	service *Service
	logger  *log.Logger
}

func NewRoomWebsocketConnectionHandler(service *Service, logger *log.Logger) *RoomWebsocketConnectionHandler {
	return &RoomWebsocketConnectionHandler{
		service: service,
		logger:  logger,
	}
}

// OnConnect is called once the client is registered on the room topic.
func (h *RoomWebsocketConnectionHandler) OnConnect(conn *websocket.Conn, topic string, userContext any) error {
	userInfo, ok := userContext.(auth.UserInfo)
	if !ok {
		h.logger.Error("Invalid user context type on connect", log.Context{
			"topic":             topic,
			"user_context_type": fmt.Sprintf("%T", userContext),
		})
		return fmt.Errorf("invalid user context")
	}

	room, err := roomFromTopic(topic)
	if err != nil {
		return err
	}

	h.logger.Debug("Websocket client connected", log.Context{
		"room":    room,
		"user_id": userInfo.ID,
	})

	return nil
}

// OnDisconnect is called when the connection is torn down.
func (h *RoomWebsocketConnectionHandler) OnDisconnect(conn *websocket.Conn, topic string, userContext any) error {
	userInfo, ok := userContext.(auth.UserInfo)
	if !ok {
		return nil
	}

	h.logger.Debug("Websocket client disconnected", log.Context{
		"topic":   topic,
		"user_id": userInfo.ID,
	})

	return nil
}

// OnMessage dispatches client -> server frames.
func (h *RoomWebsocketConnectionHandler) OnMessage(conn *websocket.Conn, messageBytes []byte, topic string, userContext any) error {
	// websocket.ConnectionHandler carries no request context; connection
	// callbacks act as their own roots.
	ctx := context.Background()

	userInfo, ok := userContext.(auth.UserInfo)
	if !ok {
		h.logger.Error("Invalid user context type", log.Context{
			"topic":             topic,
			"user_context_type": fmt.Sprintf("%T", userContext),
		})
		return websocket.SendErrorMessage(conn, "invalid_context", "Invalid user context")
	}

	room, err := roomFromTopic(topic)
	if err != nil {
		return websocket.SendErrorMessage(conn, "invalid_context", "Invalid topic")
	}

	var msg WebsocketMessage
	if err := json.Unmarshal(messageBytes, &msg); err != nil {
		h.logger.Error("Error parsing websocket message", log.Context{
			"error":   err.Error(),
			"message": string(messageBytes),
		})
		return websocket.SendErrorMessage(conn, "invalid_message", "Invalid message format")
	}

	switch msg.Type {
	case WebsocketMessageTypePing:
		return websocket.SendSuccessMessage(conn, "pong", nil)
	case WebsocketMessageTypeEcho:
		return websocket.SendSuccessMessage(conn, WebsocketMessageTypeEcho, msg.Data)
	case WebsocketMessageTypeBroadcast:
		return h.handleBroadcast(ctx, conn, msg.Data, room, userInfo)
	default:
		h.logger.Warn("Unknown websocket message type", log.Context{
			"type":    msg.Type,
			"room":    room,
			"user_id": userInfo.ID,
		})
		return websocket.SendErrorMessage(conn, "unknown_message_type", "Unknown message type")
	}
}

func (h *RoomWebsocketConnectionHandler) handleBroadcast(
	ctx context.Context,
	conn *websocket.Conn,
	data json.RawMessage,
	room string,
	userInfo auth.UserInfo,
) error {
	var input BroadcastRoomInput
	if len(data) > 0 {
		if err := json.Unmarshal(data, &input); err != nil {
			h.logger.Error("Error parsing broadcast data", log.Context{"error": err.Error()})
			return websocket.SendErrorMessage(conn, "invalid_data", "Invalid broadcast data")
		}
	}

	input.Room = room
	input.UserID = userInfo.ID

	if err := input.Validate(); err != nil {
		return websocket.SendErrorMessage(conn, "validation_error", err.Error())
	}

	if err := h.service.BroadcastToRoom(ctx, input); err != nil {
		h.logger.Error("Broadcast to room failed", log.Context{
			"error":   err.Error(),
			"room":    room,
			"user_id": userInfo.ID,
		})
		return websocket.SendErrorMessage(conn, "server_error", "Failed to broadcast message")
	}

	// The broadcast itself reaches the sender through the hub like any other client.
	return websocket.SendSuccessMessage(conn, WebsocketMessageTypeBroadcast, nil)
}

func roomFromTopic(topic string) (string, error) {
	room, found := strings.CutPrefix(topic, TopicPrefix)
	if !found || room == "" {
		return "", fmt.Errorf("invalid topic format")
	}
	return room, nil
}

// NewRoomWebsocketHandler mounts the room websocket endpoint on top of the
// platform GenericHandler. The route must sit behind auth.Middleware so the
// user context provider can read the authenticated user.
func NewRoomWebsocketHandler(
	upgrader *websocket.Upgrader,
	hub *websocket.Hub,
	service *Service,
	logger *log.Logger,
) http.HandlerFunc {
	handler := NewRoomWebsocketConnectionHandler(service, logger)

	userContextProvider := func(r *http.Request) (any, error) {
		userInfo, found := auth.UserInfoFromRequest(r)
		if !found {
			return nil, fmt.Errorf("unauthorized")
		}
		return userInfo, nil
	}

	return func(w http.ResponseWriter, r *http.Request) {
		room := chi.URLParam(r, "room")
		if err := ValidateRoom(room); err != nil {
			httpError(w, err.Error(), http.StatusBadRequest)
			return
		}

		websocket.GenericHandler(
			RoomTopic(room),
			upgrader,
			hub,
			handler,
			userContextProvider,
			logger,
		)(w, r)
	}
}
