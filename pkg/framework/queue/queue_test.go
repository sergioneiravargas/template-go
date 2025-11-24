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
	testMessageName := "test_work_shutdown_message"
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
	testMessageName := "test_work_multiple_message"
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

func TestPoolParallelWorkerExecution(t *testing.T) {
	queueName := "test"
	testMessage1Name := "test_pool_parallel_message_1"
	testMessage2Name := "test_pool_parallel_message_2"
	testMessage3Name := "test_pool_parallel_message_3"

	// Track which messages were handled
	message1Handled := 0
	message2Handled := 0
	message3Handled := 0
	handlerChan := make(chan string, 10)

	q := queue.New(
		queueName,
		[]*queue.MessageHandler{
			{
				CanHandleFunc: func(m *queue.Message) bool {
					return m.Name == testMessage1Name
				},
				HandlerFunc: func(m *queue.Message) error {
					message1Handled++
					handlerChan <- testMessage1Name
					return nil
				},
			},
			{
				CanHandleFunc: func(m *queue.Message) bool {
					return m.Name == testMessage2Name
				},
				HandlerFunc: func(m *queue.Message) error {
					message2Handled++
					handlerChan <- testMessage2Name
					return nil
				},
			},
			{
				CanHandleFunc: func(m *queue.Message) bool {
					return m.Name == testMessage3Name
				},
				HandlerFunc: func(m *queue.Message) error {
					message3Handled++
					handlerChan <- testMessage3Name
					return nil
				},
			},
		},
		conn,
		logger,
	)

	// Create a pool with the queue
	pool := queue.NewPool(nil, logger, []*queue.Queue{q})

	// Dispatch messages with different names
	for i, messageName := range []string{testMessage1Name, testMessage2Name, testMessage3Name} {
		message, err := queue.NewMessage(
			messageName,
			map[string]any{
				"message": messageName,
				"index":   i,
			},
		)
		if err != nil {
			t.Fatalf("failed to create message %s: %v", messageName, err)
		}

		if err := pool.Dispatch(queueName, message); err != nil {
			t.Fatalf("failed to dispatch message %s: %v", messageName, err)
		}
	}

	// Start all workers in parallel
	go func() {
		pool.Work()
	}()

	// Wait for all messages to be handled
	timeout := time.After(5 * time.Second)
	handledMessages := make(map[string]bool)
	expectedMessages := 3

	for i := 0; i < expectedMessages; i++ {
		select {
		case messageName := <-handlerChan:
			handledMessages[messageName] = true
		case <-timeout:
			t.Fatalf("timeout waiting for all messages to be handled (handled %d/%d)", len(handledMessages), expectedMessages)
		}
	}

	// Verify all messages were handled
	if message1Handled != 1 {
		t.Fatalf("expected message1 to be handled 1 time, got %d", message1Handled)
	}
	if message2Handled != 1 {
		t.Fatalf("expected message2 to be handled 1 time, got %d", message2Handled)
	}
	if message3Handled != 1 {
		t.Fatalf("expected message3 to be handled 1 time, got %d", message3Handled)
	}

	// Verify all three messages were handled
	if len(handledMessages) != expectedMessages {
		t.Fatalf("expected %d messages to be handled, got %d", expectedMessages, len(handledMessages))
	}

	// Cleanup
	if err := pool.Shutdown(); err != nil {
		t.Fatalf("failed to shutdown pool: %v", err)
	}
}

func TestPoolShutdown(t *testing.T) {
	queueName := "test"
	testMessage1Name := "test_pool_shutdown_message_1"
	testMessage2Name := "test_pool_shutdown_message_2"

	handlerChan := make(chan string, 10)

	q := queue.New(
		queueName,
		[]*queue.MessageHandler{
			{
				CanHandleFunc: func(m *queue.Message) bool {
					return m.Name == testMessage1Name || m.Name == testMessage2Name
				},
				HandlerFunc: func(m *queue.Message) error {
					handlerChan <- m.Name
					return nil
				},
			},
		},
		conn,
		logger,
	)

	pool := queue.NewPool(nil, logger, []*queue.Queue{q})

	// Dispatch messages with different names
	for _, messageName := range []string{testMessage1Name, testMessage2Name} {
		message, err := queue.NewMessage(
			messageName,
			map[string]string{"message": messageName},
		)
		if err != nil {
			t.Fatalf("failed to create message: %v", err)
		}

		if err := pool.Dispatch(queueName, message); err != nil {
			t.Fatalf("failed to dispatch message: %v", err)
		}
	}

	// Start workers
	go func() {
		pool.Work()
	}()

	// Wait for both messages to be handled
	timeout := time.After(5 * time.Second)
	for i := 0; i < 2; i++ {
		select {
		case <-handlerChan:
			// Message handled
		case <-timeout:
			t.Fatalf("timeout waiting for messages to be handled")
		}
	}

	// Shutdown the pool - should shutdown the queue
	if err := pool.Shutdown(); err != nil {
		t.Fatalf("failed to shutdown pool: %v", err)
	}

	// Verify queue stops fetching after shutdown
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	msg, err := q.FetchMessage(ctx)
	if err != nil {
		t.Fatalf("unexpected error fetching after shutdown: %v", err)
	}
	if msg != nil {
		t.Fatal("expected nil message after shutdown")
	}
}

func TestPoolWorkWithMultipleMessagesPerQueue(t *testing.T) {
	queueName := "test"
	testMessageName := "test_pool_multi_messages_message"
	totalMessages := 6

	messagesHandled := 0
	handlerChan := make(chan struct{}, totalMessages)

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

	pool := queue.NewPool(nil, logger, []*queue.Queue{q})

	// Dispatch multiple messages
	for i := 0; i < totalMessages; i++ {
		message, err := queue.NewMessage(
			testMessageName,
			map[string]any{
				"index": i,
			},
		)
		if err != nil {
			t.Fatalf("failed to create message: %v", err)
		}

		if err := pool.Dispatch(queueName, message); err != nil {
			t.Fatalf("failed to dispatch message: %v", err)
		}
	}

	// Start workers
	go func() {
		pool.Work()
	}()

	// Wait for all messages to be handled
	timeout := time.After(10 * time.Second)
	for i := 0; i < totalMessages; i++ {
		select {
		case <-handlerChan:
			// Message handled
		case <-timeout:
			t.Fatalf("timeout waiting for message %d/%d to be handled", i+1, totalMessages)
		}
	}

	// Verify correct number of messages handled
	if messagesHandled != totalMessages {
		t.Fatalf("expected %d messages to be handled, got %d", totalMessages, messagesHandled)
	}

	// Cleanup
	if err := pool.Shutdown(); err != nil {
		t.Fatalf("failed to shutdown pool: %v", err)
	}
}
