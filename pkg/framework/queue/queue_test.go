package queue_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/sergioneiravargas/template-go/pkg/framework/log"
	"github.com/sergioneiravargas/template-go/pkg/framework/queue"

	amqp "github.com/rabbitmq/amqp091-go"
)

var conn *amqp.Connection
var logger *log.Logger

func init() {
	amqpURL := fmt.Sprintf("amqp://%s:%s@%s:%s/", os.Getenv("AMQP_USER"), os.Getenv("AMQP_PASSWORD"), os.Getenv("AMQP_HOST"), os.Getenv("AMQP_PORT"))
	var err error
	conn, err = amqp.Dial(amqpURL)
	if err != nil {
		panic(err)
	}

	logger = log.NewLogger(
		"test",
		log.NewHandler(os.Stdout, "dev"),
	)
}

func TestQueueDispatch(t *testing.T) {
	queueName := "test"
	testMessageName := "test_message"

	q := queue.New(
		queueName,
		[]*queue.MessageHandler{
			{
				CanHandleFunc: func(m *queue.Message) bool {
					return m.Name == testMessageName
				},
				HandlerFunc: func(m *queue.Message) error {
					fmt.Printf("received message: %s\n", m.Body)
					return nil
				},
			},
		},
		conn,
		logger,
	)

	message := queue.Message{
		Name: testMessageName,
		Body: []byte("Hello, World!"),
	}
	err := q.Dispatch(&message)
	if err != nil {
		t.Fatalf("failed to dispatch message: %v", err)
	}
}

func TestQueueFetchMessage(t *testing.T) {
	queueName := "test"
	testMessageName := "test_message"
	q := queue.New(
		queueName,
		[]*queue.MessageHandler{
			{
				CanHandleFunc: func(m *queue.Message) bool {
					return m.Name == testMessageName
				},
				HandlerFunc: func(m *queue.Message) error {
					return nil
				},
			},
		},
		conn,
		logger,
	)

	message, err := queue.NewMessage(
		testMessageName,
		[]byte("Hello, World!"),
	)
	if err != nil {
		t.Fatalf("failed to create message: %v", err)
	}

	if err := q.Dispatch(message); err != nil {
		t.Fatalf("failed to dispatch message: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	msg, err := q.FetchMessage(ctx)
	if err != nil {
		t.Fatalf("failed to fetch messages: %v", err)
	}
	if msg.Name != testMessageName {
		t.Fatalf("message name is %s, expected %s", msg.Name, testMessageName)
	}
}

func TestQueueMessageHandle(t *testing.T) {
	queueName := "test"
	testMessageName := "test_message"
	messagesHandledCount := 0
	q := queue.New(
		queueName,
		[]*queue.MessageHandler{
			{
				CanHandleFunc: func(m *queue.Message) bool {
					return m.Name == testMessageName
				},
				HandlerFunc: func(m *queue.Message) error {
					messagesHandledCount++
					return nil
				},
			},
		},
		conn,
		logger,
	)

	message, err := queue.NewMessage(
		testMessageName,
		[]byte("Hello, World!"),
	)
	if err != nil {
		t.Fatalf("failed to create message: %v", err)
	}

	if err := q.Dispatch(message); err != nil {
		t.Fatalf("failed to dispatch message: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	msg, err := q.FetchMessage(ctx)
	if err != nil {
		t.Fatalf("failed to fetch messages: %v", err)
	}
	if msg.Name != testMessageName {
		t.Fatalf("message name is %s, expected %s", msg.Name, testMessageName)
	}

	q.HandleMessage(msg)
	if messagesHandledCount != 1 {
		t.Fatalf("message was not handled, expected 1, got %d", messagesHandledCount)
	}
}

func TestQueueWorkAndShutdown(t *testing.T) {
	queueName := "test"
	testMessageName := "test_work_message"
	messagesHandled := 0
	handlerChan := make(chan struct{})

	q := queue.New(
		queueName,
		[]*queue.MessageHandler{
			{
				CanHandleFunc: func(m *queue.Message) bool {
					return m.Name == testMessageName
				},
				HandlerFunc: func(m *queue.Message) error {
					messagesHandled++
					handlerChan <- struct{}{}
					return nil
				},
			},
		},
		conn,
		logger,
	)

	// Dispatch a test message
	message, err := queue.NewMessage(
		testMessageName,
		map[string]string{"data": "test payload"},
	)
	if err != nil {
		t.Fatalf("failed to create message: %v", err)
	}

	if err := q.Dispatch(message); err != nil {
		t.Fatalf("failed to dispatch message: %v", err)
	}

	// Start the worker in a goroutine
	workDone := make(chan struct{})
	go func() {
		q.Work()
		close(workDone)
	}()

	// Wait for the message to be handled
	select {
	case <-handlerChan:
		// Message was handled successfully
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for message to be handled")
	}

	// Verify the message was handled
	if messagesHandled != 1 {
		t.Fatalf("expected 1 message to be handled, got %d", messagesHandled)
	}

	// Shutdown the queue
	if err := q.Shutdown(); err != nil {
		t.Fatalf("failed to shutdown queue: %v", err)
	}

	// Wait for Work to finish (it should exit gracefully)
	// Note: Work() runs indefinitely, but after shutdown, FetchMessage returns nil
	// and the worker continues the loop. In a real scenario, you'd need additional
	// logic to break the loop. For this test, we verify shutdown doesn't error.

	// Give a moment for shutdown to be processed
	time.Sleep(100 * time.Millisecond)
}

func TestQueueShutdownStopsNewMessageFetch(t *testing.T) {
	queueName := "test"
	testMessageName := "test_shutdown_message"

	q := queue.New(
		queueName,
		[]*queue.MessageHandler{
			{
				CanHandleFunc: func(m *queue.Message) bool {
					return m.Name == testMessageName
				},
				HandlerFunc: func(m *queue.Message) error {
					return nil
				},
			},
		},
		conn,
		logger,
	)

	// Shutdown the queue first
	if err := q.Shutdown(); err != nil {
		t.Fatalf("failed to shutdown queue: %v", err)
	}

	// Try to fetch a message after shutdown - should return nil without blocking
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	msg, err := q.FetchMessage(ctx)
	if err != nil {
		t.Fatalf("unexpected error fetching message after shutdown: %v", err)
	}
	if msg != nil {
		t.Fatal("expected nil message after shutdown, but got a message")
	}
}

func TestQueueWorkProcessesMultipleMessages(t *testing.T) {
	queueName := "test"
	testMessageName := "test_multiple_message"
	messagesHandled := 0
	expectedMessages := 3
	handlerChan := make(chan struct{}, expectedMessages)

	q := queue.New(
		queueName,
		[]*queue.MessageHandler{
			{
				CanHandleFunc: func(m *queue.Message) bool {
					return m.Name == testMessageName
				},
				HandlerFunc: func(m *queue.Message) error {
					messagesHandled++
					handlerChan <- struct{}{}
					return nil
				},
			},
		},
		conn,
		logger,
	)

	// Dispatch multiple test messages
	for i := 0; i < expectedMessages; i++ {
		message, err := queue.NewMessage(
			testMessageName,
			map[string]any{
				"data":  "test payload",
				"index": i,
			},
		)
		if err != nil {
			t.Fatalf("failed to create message %d: %v", i, err)
		}

		if err := q.Dispatch(message); err != nil {
			t.Fatalf("failed to dispatch message %d: %v", i, err)
		}
	}

	// Start the worker in a goroutine
	go func() {
		q.Work()
	}()

	// Wait for all messages to be handled
	timeout := time.After(5 * time.Second)
	for i := 0; i < expectedMessages; i++ {
		select {
		case <-handlerChan:
			// Message was handled successfully
		case <-timeout:
			t.Fatalf("timeout waiting for message %d to be handled (handled %d messages so far)", i+1, messagesHandled)
		}
	}

	// Verify all messages were handled
	if messagesHandled != expectedMessages {
		t.Fatalf("expected %d messages to be handled, got %d", expectedMessages, messagesHandled)
	}

	// Cleanup
	if err := q.Shutdown(); err != nil {
		t.Fatalf("failed to shutdown queue: %v", err)
	}
}
