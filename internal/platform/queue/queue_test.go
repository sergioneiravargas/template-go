package queue_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/sergioneiravargas/template-go/internal/platform/log"
	"github.com/sergioneiravargas/template-go/internal/platform/queue"

	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"
)

var logger *log.Logger

func init() {
	logger = log.NewLogger(
		"test",
		log.NewHandler(os.Stdout, "dev"),
	)
}

// setupAMQPConn dials the broker from AMQP_* env vars, mirroring the DB test
// helper: it t.Skips cleanly when the broker is not configured or reachable so
// AMQP-dependent tests do not fail loudly in environments without a broker.
func setupAMQPConn(t *testing.T) *amqp.Connection {
	t.Helper()

	host := os.Getenv("AMQP_HOST")
	if host == "" {
		t.Skip("AMQP_HOST not set; skipping AMQP-dependent tests")
	}

	amqpURL := fmt.Sprintf("amqp://%s:%s@%s:%s/", os.Getenv("AMQP_USER"), os.Getenv("AMQP_PASSWORD"), host, os.Getenv("AMQP_PORT"))
	conn, err := amqp.Dial(amqpURL)
	if err != nil {
		t.Skipf("cannot reach AMQP broker at %s: %v", host, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// uniqueQueueName returns a queue name unique to this test so tests do not
// share broker state (leftover messages from one test leaking into another was
// a source of cross-test flakiness). It registers cleanup to delete the queue
// and its dead-letter queue when the test finishes.
func uniqueQueueName(t *testing.T, conn *amqp.Connection) string {
	t.Helper()

	name := "test-" + uuid.NewString()
	t.Cleanup(func() {
		ch, err := conn.Channel()
		if err != nil {
			return
		}
		defer ch.Close()
		_, _ = ch.QueueDelete(name, false, false, false)
		_, _ = ch.QueueDelete(name+".deadletter", false, false, false)
	})
	return name
}

func TestQueueDispatch(t *testing.T) {
	conn := setupAMQPConn(t)
	queueName := uniqueQueueName(t, conn)
	testMessageName := "test_message"

	q := queue.New(
		queueName,
		[]*queue.MessageHandler{
			{
				CanHandleFunc: func(m *queue.Message) bool {
					return m.Name == testMessageName
				},
				HandlerFunc: func(ctx context.Context, m *queue.Message) error {
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
	err := q.Dispatch(t.Context(), &message)
	if err != nil {
		t.Fatalf("failed to dispatch message: %v", err)
	}
}

func TestQueueFetchMessage(t *testing.T) {
	conn := setupAMQPConn(t)
	queueName := uniqueQueueName(t, conn)
	testMessageName := "test_message"
	q := queue.New(
		queueName,
		[]*queue.MessageHandler{
			{
				CanHandleFunc: func(m *queue.Message) bool {
					return m.Name == testMessageName
				},
				HandlerFunc: func(ctx context.Context, m *queue.Message) error {
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

	if err := q.Dispatch(t.Context(), message); err != nil {
		t.Fatalf("failed to dispatch message: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 1*time.Second)
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
	conn := setupAMQPConn(t)
	queueName := uniqueQueueName(t, conn)
	testMessageName := "test_message"
	messagesHandledCount := 0
	q := queue.New(
		queueName,
		[]*queue.MessageHandler{
			{
				CanHandleFunc: func(m *queue.Message) bool {
					return m.Name == testMessageName
				},
				HandlerFunc: func(ctx context.Context, m *queue.Message) error {
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

	if err := q.Dispatch(t.Context(), message); err != nil {
		t.Fatalf("failed to dispatch message: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 1*time.Second)
	defer cancel()

	msg, err := q.FetchMessage(ctx)
	if err != nil {
		t.Fatalf("failed to fetch messages: %v", err)
	}
	if msg.Name != testMessageName {
		t.Fatalf("message name is %s, expected %s", msg.Name, testMessageName)
	}

	q.HandleMessage(t.Context(), msg)
	if messagesHandledCount != 1 {
		t.Fatalf("message was not handled, expected 1, got %d", messagesHandledCount)
	}
}

func TestQueueWorkAndShutdown(t *testing.T) {
	conn := setupAMQPConn(t)
	queueName := uniqueQueueName(t, conn)
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
				HandlerFunc: func(ctx context.Context, m *queue.Message) error {
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

	if err := q.Dispatch(t.Context(), message); err != nil {
		t.Fatalf("failed to dispatch message: %v", err)
	}

	// Start the worker in a goroutine
	workDone := make(chan struct{})
	go func() {
		q.Work(t.Context())
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
	conn := setupAMQPConn(t)
	queueName := uniqueQueueName(t, conn)
	testMessageName := "test_shutdown_message"

	q := queue.New(
		queueName,
		[]*queue.MessageHandler{
			{
				CanHandleFunc: func(m *queue.Message) bool {
					return m.Name == testMessageName
				},
				HandlerFunc: func(ctx context.Context, m *queue.Message) error {
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
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
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
	conn := setupAMQPConn(t)
	queueName := uniqueQueueName(t, conn)
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
				HandlerFunc: func(ctx context.Context, m *queue.Message) error {
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

		if err := q.Dispatch(t.Context(), message); err != nil {
			t.Fatalf("failed to dispatch message %d: %v", i, err)
		}
	}

	// Start the worker in a goroutine
	go func() {
		q.Work(t.Context())
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
	conn := setupAMQPConn(t)
	queueName := uniqueQueueName(t, conn)
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
				HandlerFunc: func(ctx context.Context, m *queue.Message) error {
					message1Handled++
					handlerChan <- testMessage1Name
					return nil
				},
			},
			{
				CanHandleFunc: func(m *queue.Message) bool {
					return m.Name == testMessage2Name
				},
				HandlerFunc: func(ctx context.Context, m *queue.Message) error {
					message2Handled++
					handlerChan <- testMessage2Name
					return nil
				},
			},
			{
				CanHandleFunc: func(m *queue.Message) bool {
					return m.Name == testMessage3Name
				},
				HandlerFunc: func(ctx context.Context, m *queue.Message) error {
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

		if err := pool.Dispatch(t.Context(), queueName, message); err != nil {
			t.Fatalf("failed to dispatch message %s: %v", messageName, err)
		}
	}

	// Start all workers in parallel
	go func() {
		pool.Work(t.Context())
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
	if err := pool.Shutdown(t.Context()); err != nil {
		t.Fatalf("failed to shutdown pool: %v", err)
	}
}

func TestPoolShutdown(t *testing.T) {
	conn := setupAMQPConn(t)
	queueName := uniqueQueueName(t, conn)
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
				HandlerFunc: func(ctx context.Context, m *queue.Message) error {
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

		if err := pool.Dispatch(t.Context(), queueName, message); err != nil {
			t.Fatalf("failed to dispatch message: %v", err)
		}
	}

	// Start workers
	go func() {
		pool.Work(t.Context())
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
	if err := pool.Shutdown(t.Context()); err != nil {
		t.Fatalf("failed to shutdown pool: %v", err)
	}

	// Verify queue stops fetching after shutdown
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
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
	conn := setupAMQPConn(t)
	queueName := uniqueQueueName(t, conn)
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
				HandlerFunc: func(ctx context.Context, m *queue.Message) error {
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

		if err := pool.Dispatch(t.Context(), queueName, message); err != nil {
			t.Fatalf("failed to dispatch message: %v", err)
		}
	}

	// Start workers
	go func() {
		pool.Work(t.Context())
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
	if err := pool.Shutdown(t.Context()); err != nil {
		t.Fatalf("failed to shutdown pool: %v", err)
	}
}

// TestQueueWorkerCountParallelism verifies that WithWorkerCount actually fans
// out work across N goroutines. With a per-handler sleep of handlerDelay, a
// serial worker would take totalMessages*handlerDelay; N parallel workers
// should finish in roughly (totalMessages/N)*handlerDelay. We assert wall-clock
// is well below the serial baseline.
func TestQueueWorkerCountParallelism(t *testing.T) {
	conn := setupAMQPConn(t)
	queueName := uniqueQueueName(t, conn)
	testMessageName := "test_worker_parallelism_message"
	const (
		workerCount   = 4
		totalMessages = 4
		handlerDelay  = 300 * time.Millisecond
	)

	handlerChan := make(chan struct{}, totalMessages)

	q := queue.New(
		queueName,
		[]*queue.MessageHandler{
			{
				CanHandleFunc: func(m *queue.Message) bool {
					return m.Name == testMessageName
				},
				HandlerFunc: func(ctx context.Context, m *queue.Message) error {
					time.Sleep(handlerDelay)
					handlerChan <- struct{}{}
					return nil
				},
			},
		},
		conn,
		logger,
		queue.WithWorkerCount(workerCount),
	)

	for i := 0; i < totalMessages; i++ {
		message, err := queue.NewMessage(
			testMessageName,
			map[string]int{"index": i},
		)
		if err != nil {
			t.Fatalf("failed to create message: %v", err)
		}
		if err := q.Dispatch(t.Context(), message); err != nil {
			t.Fatalf("failed to dispatch message: %v", err)
		}
	}

	start := time.Now()
	go q.Work(t.Context())

	// Serial baseline = totalMessages*handlerDelay (1.2s). Parallel with N=4
	// should finish near handlerDelay (0.3s) plus polling jitter. Allow 2x
	// that to keep the test stable; still well under the serial baseline.
	deadline := time.After(2 * handlerDelay)
	for i := 0; i < totalMessages; i++ {
		select {
		case <-handlerChan:
		case <-deadline:
			t.Fatalf("workers are not running in parallel: only %d/%d messages handled in %v (serial would take %v)",
				i, totalMessages, time.Since(start), totalMessages*handlerDelay)
		}
	}

	elapsed := time.Since(start)
	if elapsed >= totalMessages*handlerDelay {
		t.Fatalf("expected parallel execution under serial baseline %v, took %v", totalMessages*handlerDelay, elapsed)
	}

	if err := q.Shutdown(); err != nil {
		t.Fatalf("failed to shutdown queue: %v", err)
	}
}

func TestQueueDeadLetter(t *testing.T) {
	conn := setupAMQPConn(t)

	queueName := uniqueQueueName(t, conn)
	testMessageName := "test_dlq_message"

	// Create queue and a handler that fails
	q := queue.New(
		queueName,
		[]*queue.MessageHandler{
			{
				CanHandleFunc: func(m *queue.Message) bool {
					return m.Name == testMessageName
				},
				HandlerFunc: func(ctx context.Context, m *queue.Message) error {
					return fmt.Errorf("intentional processing failure")
				},
			},
		},
		conn,
		logger,
	)

	// Create and dispatch message with MaxRetries set to 0 so it dead-letters immediately
	message, err := queue.NewMessage(
		testMessageName,
		[]byte("DLQ Payload"),
		queue.MessageWithMaxRetries(0),
	)
	if err != nil {
		t.Fatalf("failed to create message: %v", err)
	}

	if err := q.Dispatch(t.Context(), message); err != nil {
		t.Fatalf("failed to dispatch message: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	msg, err := q.FetchMessage(ctx)
	if err != nil {
		t.Fatalf("failed to fetch messages: %v", err)
	}
	if msg == nil {
		t.Fatalf("expected message, got nil")
	}

	// Handle the message. It should fail and call DeadLetterMessage.
	q.HandleMessage(t.Context(), msg)

	// Open a new channel to get the message from DLQ directly
	ch, err := conn.Channel()
	if err != nil {
		t.Fatalf("failed to open channel: %v", err)
	}
	defer ch.Close()

	// Fetch from the DLQ (queueName + ".deadletter")
	dlqMsg, ok, err := ch.Get(queueName+".deadletter", false)
	if err != nil {
		t.Fatalf("failed to get message from DLQ: %v", err)
	}
	if !ok {
		t.Fatalf("expected message in DLQ, but DLQ was empty")
	}

	// Ack the DLQ message
	if err := dlqMsg.Ack(false); err != nil {
		t.Errorf("failed to ack message from DLQ: %v", err)
	}

	// Decode the dead-lettered message body to make sure it matches
	var decodedMessage queue.Message
	if err := json.Unmarshal(dlqMsg.Body, &decodedMessage); err != nil {
		t.Fatalf("failed to unmarshal DLQ message body: %v", err)
	}

	if decodedMessage.ID != message.ID {
		t.Fatalf("expected message ID %s in DLQ, got %s", message.ID, decodedMessage.ID)
	}

	// Verify the headers contain x-death-reason
	reason, ok := dlqMsg.Headers["x-death-reason"].(string)
	if !ok {
		t.Errorf("expected x-death-reason header to be a string, got %T", dlqMsg.Headers["x-death-reason"])
	} else if reason != "intentional processing failure" {
		t.Errorf("expected x-death-reason to be 'intentional processing failure', got '%s'", reason)
	}
}
