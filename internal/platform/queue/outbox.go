package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sergioneiravargas/template-go/internal/platform/log"
	"github.com/sergioneiravargas/template-go/internal/platform/sql"

	"github.com/google/uuid"
)

type OutboxMessage struct {
	ID        string   `json:"id"`
	QueueName string   `json:"queue_name"`
	Message   *Message `json:"message"`

	CreatedAt   time.Time `json:"created_at"`
	AvailableAt time.Time `json:"available_at"`

	RetryCount int     `json:"retry_count"`
	LastError  *string `json:"last_error,omitempty"`
}

func NewOutboxMessage(
	queueName string,
	msg *Message,
) *OutboxMessage {
	return &OutboxMessage{
		ID:          uuid.NewString(),
		QueueName:   queueName,
		Message:     msg,
		CreatedAt:   time.Now(),
		AvailableAt: time.Now(),
		RetryCount:  0,
	}
}

func CreateOutboxMessage(
	ctx context.Context,
	tx *sql.Tx,
	queueName string,
	msgs ...*Message,
) error {
	for _, msg := range msgs {
		msgJSON, err := json.Marshal(msg)
		if err != nil {
			return fmt.Errorf("failed to marshal message: %w", err)
		}

		_, err = tx.ExecContext(
			ctx,
			"INSERT INTO queue_outbox (id, queue_name, message, created_at, available_at, retry_count) VALUES ($1, $2, $3, $4, $5, $6)",
			uuid.NewString(),
			queueName,
			msgJSON,
			time.Now(),
			time.Now(),
			0,
		)
		if err != nil {
			return fmt.Errorf("failed to insert outbox message: %w", err)
		}
	}

	if len(msgs) > 0 {
		// Delivered on commit, so consumers only wake for visible rows.
		if _, err := tx.ExecContext(ctx, "SELECT pg_notify($1, '')", outboxNotifyChannel); err != nil {
			return fmt.Errorf("failed to notify outbox consumers: %w", err)
		}
	}
	return nil
}

func DeleteOutboxMessage(
	ctx context.Context,
	tx *sql.Tx,
	id string,
) error {
	_, err := tx.ExecContext(ctx, "DELETE FROM queue_outbox WHERE id = $1", id)
	if err != nil {
		return fmt.Errorf("failed to delete outbox message with id %s: %w", id, err)
	}
	return nil
}

const (
	// outboxNotifyChannel is the Postgres NOTIFY channel raised by
	// CreateOutboxMessage on commit so idle consumers wake up immediately.
	outboxNotifyChannel = "queue_outbox"
	// outboxIdleSleep paces consumers after an error and, while the LISTEN
	// connection is down, between idle polls.
	outboxIdleSleep = 100 * time.Millisecond
	// outboxIdlePoll is the safety poll interval while LISTEN is active: it
	// only matters if a notification is lost (e.g. around a reconnect).
	outboxIdlePoll = 5 * time.Second
	// outboxListenBackoff paces re-listen attempts after the connection fails.
	outboxListenBackoff = 1 * time.Second
)

// outboxWaker fans a single NOTIFY out to every idle consumer.
type outboxWaker struct {
	mu        sync.Mutex
	ch        chan struct{}
	listening atomic.Bool
}

func newOutboxWaker() *outboxWaker {
	return &outboxWaker{ch: make(chan struct{})}
}

// wait returns a channel closed by the next wake call.
func (w *outboxWaker) wait() <-chan struct{} {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.ch
}

func (w *outboxWaker) wake() {
	w.mu.Lock()
	defer w.mu.Unlock()
	close(w.ch)
	w.ch = make(chan struct{})
}

// idle returns how long a consumer may sleep with no rows before polling again.
func (w *outboxWaker) idle() time.Duration {
	if w.listening.Load() {
		return outboxIdlePoll
	}
	return outboxIdleSleep
}

// runOutboxListener keeps a LISTEN connection open and wakes the consumers on
// every notification. It re-listens with a backoff when the connection fails
// and wakes the consumers after each (re)subscription so rows inserted while
// the listener was down are picked up right away.
func runOutboxListener(ctx context.Context, db *sql.DB, waker *outboxWaker, logger *log.Logger) {
	for {
		waker.listening.Store(true)
		waker.wake()
		err := sql.Listen(ctx, db, outboxNotifyChannel, waker.wake)
		waker.listening.Store(false)
		if ctx.Err() != nil {
			return
		}
		logger.Error("Outbox listener stopped, falling back to polling until it reconnects", log.Context{
			"error": errString(err),
		})
		select {
		case <-ctx.Done():
			return
		case <-time.After(outboxListenBackoff):
		}
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// outboxDispatcher is the subset of *Pool used by the outbox consumer.
// Defined as an interface so tests can inject a mock without a live AMQP
// connection. *Pool satisfies it implicitly.
type outboxDispatcher interface {
	Dispatch(ctx context.Context, queueName string, msg *Message) error
}

// ConsumeOutboxMessages runs up to `concurrency` independent consumer loops.
// Each loop claims one row at a time using SELECT ... FOR UPDATE SKIP LOCKED
// and finalizes (DELETE on dispatch success, UPDATE for retry on failure)
// inside the same transaction. The row lock is held across the AMQP publish
// so concurrent consumers — including peer worker pods — cannot dispatch the
// same row twice, and crashes between publish and commit are recovered as
// at-least-once redelivery rather than message loss.
func ConsumeOutboxMessages(
	ctx context.Context,
	db *sql.DB,
	pool *Pool,
	logger *log.Logger,
	concurrency int,
) {
	if concurrency <= 0 {
		panic("concurrency must be greater than 0")
	}

	waker := newOutboxWaker()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		runOutboxListener(ctx, db, waker, logger)
	}()
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runOutboxConsumer(ctx, db, pool, waker, logger)
		}()
	}
	wg.Wait()
}

// runOutboxConsumer loops until ctx is cancelled. While rows are available it
// processes them back to back; with no rows it blocks until the listener wakes
// it (or the safety poll fires) instead of hammering the database. Cancellation
// mid-dispatch is safe: the outbox row stays locked inside its transaction and
// is redelivered on the next run (at-least-once semantics).
func runOutboxConsumer(
	ctx context.Context,
	db *sql.DB,
	dispatcher outboxDispatcher,
	waker *outboxWaker,
	logger *log.Logger,
) {
	for {
		select {
		case <-ctx.Done():
			logger.Info("Outbox consumer stopped", nil)
			return
		default:
		}

		// Grab the wake channel before querying so a NOTIFY that lands between
		// the empty query and the wait below is not lost.
		wake := waker.wait()

		err := processNextOutboxMessage(ctx, db, dispatcher, logger)
		if err == nil {
			continue
		}

		pause := outboxIdleSleep
		if errors.Is(err, sql.ErrNoRows) {
			pause = waker.idle()
		} else if !errors.Is(err, context.Canceled) {
			logger.Error("Failed to process outbox message", log.Context{
				"error": err.Error(),
			})
		}

		select {
		case <-ctx.Done():
			logger.Info("Outbox consumer stopped", nil)
			return
		case <-wake:
		case <-time.After(pause):
		}
	}
}

func processNextOutboxMessage(
	ctx context.Context,
	db *sql.DB,
	dispatcher outboxDispatcher,
	logger *log.Logger,
) (err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin outbox transaction: %w", err)
	}
	defer func() {
		// Rollback is a no-op if the tx was already committed.
		// Surface a rollback failure only if we don't already have one.
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) && err == nil {
			err = fmt.Errorf("failed to rollback outbox transaction: %w", rbErr)
		}
	}()

	msg, malformed, lockErr := lockNextOutboxMessage(ctx, tx)
	if lockErr != nil {
		return lockErr
	}

	if malformed {
		if _, execErr := tx.ExecContext(ctx, "DELETE FROM queue_outbox WHERE id = $1", msg.ID); execErr != nil {
			return fmt.Errorf("failed to delete malformed outbox message: %w", execErr)
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return fmt.Errorf("failed to commit malformed outbox delete: %w", commitErr)
		}
		logger.Warn("Dropped malformed outbox message", log.Context{
			"outbox_id": msg.ID,
			"queue":     msg.QueueName,
		})
		return nil
	}

	if dispatchErr := dispatcher.Dispatch(ctx, msg.QueueName, msg.Message); dispatchErr != nil {
		logger.Error("Failed to dispatch outbox message", log.Context{
			"outbox_id":  msg.ID,
			"queue":      msg.QueueName,
			"message_id": msg.Message.ID,
			"error":      dispatchErr.Error(),
		})
		return scheduleOutboxRetryTx(ctx, tx, logger, msg, dispatchErr)
	}

	if _, execErr := tx.ExecContext(ctx, "DELETE FROM queue_outbox WHERE id = $1", msg.ID); execErr != nil {
		return fmt.Errorf("failed to delete dispatched outbox message: %w", execErr)
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return fmt.Errorf("failed to commit outbox delete: %w", commitErr)
	}

	logger.Debug("Outbox message dispatched", log.Context{
		"outbox_id":  msg.ID,
		"queue":      msg.QueueName,
		"message_id": msg.Message.ID,
	})
	return nil
}

// lockNextOutboxMessage claims one due outbox row with FOR UPDATE SKIP LOCKED.
// The returned malformed flag indicates the row's payload could not be decoded;
// caller is expected to delete it rather than dispatch.
//
// The locking is done in a subquery so the SKIP LOCKED + ORDER BY + LIMIT
// combination isn't subject to plan-dependent edge cases (where PG could
// otherwise apply LIMIT before SKIP LOCKED and return zero rows even when
// unlocked rows exist).
func lockNextOutboxMessage(ctx context.Context, tx *sql.Tx) (*OutboxMessage, bool, error) {
	row := tx.QueryRowContext(ctx, `
		SELECT id, queue_name, message, created_at, available_at, retry_count, last_error
		FROM queue_outbox
		WHERE id = (
			SELECT id
			FROM queue_outbox
			WHERE available_at <= $1
			AND $2 > retry_count
			ORDER BY available_at ASC
			LIMIT 1
			FOR UPDATE SKIP LOCKED
		)
	`,
		time.Now(),
		MaxRetries,
	)

	var msgJSON []byte
	msg := new(OutboxMessage)
	if err := row.Scan(
		&msg.ID,
		&msg.QueueName,
		&msgJSON,
		&msg.CreatedAt,
		&msg.AvailableAt,
		&msg.RetryCount,
		&msg.LastError,
	); err != nil {
		return nil, false, err
	}

	msg.Message = new(Message)
	if err := json.Unmarshal(msgJSON, msg.Message); err != nil {
		return msg, true, nil
	}
	return msg, false, nil
}

func scheduleOutboxRetryTx(
	ctx context.Context,
	tx *sql.Tx,
	logger *log.Logger,
	msg *OutboxMessage,
	dispatchErr error,
) error {
	retryCount := msg.RetryCount + 1
	delayMs := calculateExponentialBackoff(retryCount)
	availableAt := time.Now().Add(time.Duration(delayMs) * time.Millisecond)
	lastError := dispatchErr.Error()

	if _, err := tx.ExecContext(
		ctx,
		`UPDATE queue_outbox SET retry_count = $1, available_at = $2, last_error = $3 WHERE id = $4`,
		retryCount,
		availableAt,
		lastError,
		msg.ID,
	); err != nil {
		return fmt.Errorf("failed to update outbox message for retry: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit outbox retry: %w", err)
	}

	logger.Warn("Scheduled outbox message retry", log.Context{
		"outbox_id":  msg.ID,
		"queue":      msg.QueueName,
		"message_id": msg.Message.ID,
		"retry":      retryCount,
		"delay_ms":   delayMs,
	})

	if retryCount >= MaxRetries {
		logger.Error("Outbox message reached max retries", log.Context{
			"outbox_id":  msg.ID,
			"queue":      msg.QueueName,
			"message_id": msg.Message.ID,
			"retry":      retryCount,
		})
	}
	return nil
}
