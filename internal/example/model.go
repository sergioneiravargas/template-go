package example

import (
	"fmt"
	"regexp"
	"time"
)

const (
	// TopicPrefix namespaces every websocket topic owned by this slice.
	TopicPrefix = "example:"
	// MessagesRoom is the room where message_created events are published.
	MessagesRoom = "messages"

	EventTypeMessageCreated = "message_created"
	EventTypeBroadcast      = "broadcast"

	maxMessageLength = 1024
)

var roomPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Message is the persisted example entity (table example_message).
type Message struct {
	ID        string    `json:"id"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

// Event is the JSON envelope delivered to websocket clients.
type Event struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

type CreateMessageInput struct {
	Body string `json:"body"`
}

func (i CreateMessageInput) Validate() error {
	if i.Body == "" {
		return fmt.Errorf("body cannot be empty")
	}
	if len(i.Body) > maxMessageLength {
		return fmt.Errorf("body cannot exceed %d characters", maxMessageLength)
	}
	return nil
}

type BroadcastRoomInput struct {
	Room    string `json:"-"`
	UserID  string `json:"-"`
	Message string `json:"message"`
}

func (i BroadcastRoomInput) Validate() error {
	if err := ValidateRoom(i.Room); err != nil {
		return err
	}
	if i.UserID == "" {
		return fmt.Errorf("user id cannot be empty")
	}
	if i.Message == "" {
		return fmt.Errorf("message cannot be empty")
	}
	if len(i.Message) > maxMessageLength {
		return fmt.Errorf("message cannot exceed %d characters", maxMessageLength)
	}
	return nil
}

// ValidateRoom rejects room names that cannot be used as a websocket topic.
func ValidateRoom(room string) error {
	if !roomPattern.MatchString(room) {
		return fmt.Errorf("invalid room name")
	}
	return nil
}

// RoomTopic returns the websocket topic for the given room.
func RoomTopic(room string) string {
	return TopicPrefix + room
}
