package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/sergioneiravargas/template-go/pkg/framework/log"
	"github.com/sergioneiravargas/template-go/pkg/framework/sql"

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
	tx *sql.Tx,
	queueName string,
	msgs ...*Message,
) error {
	for _, msg := range msgs {
		msgJSON, err := json.Marshal(msg)
		if err != nil {
			return fmt.Errorf("failed to marshal message: %w", err)
		}

		_, err = tx.Exec(
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
	return nil
}

func DeleteOutboxMessage(
	tx *sql.Tx,
	id string,
) error {
	_, err := tx.Exec("DELETE FROM queue_outbox WHERE id = $1", id)
	if err != nil {
		return fmt.Errorf("failed to delete outbox message with id %s: %w", id, err)
	}
	return nil
}

// ConsumeOutboxMessages fetches available outbox messages, dispatches them to their respective queues,
// and deletes them upon success. It processes messages concurrently using up to N goroutines defined by limit.
// If a message fails to dispatch, it updates the retry count and schedules it for retry with exponential backoff.
func ConsumeOutboxMessages(
	db *sql.DB,
	pool *Pool,
	logger *log.Logger,
	limit int,
) {
	if limit <= 0 {
		panic("limit must be greater than 0")
	}

	for {
		messages, err := fetchOutboxMessages(db, limit)
		if err != nil {
			logger.Error("Failed to fetch outbox messages", map[string]any{
				"error": err.Error(),
			})
			time.Sleep(100 * time.Millisecond)
			continue
		}
		if len(messages) == 0 {
			time.Sleep(100 * time.Millisecond)
			continue
		}

		var wg sync.WaitGroup
		for _, outboxMsg := range messages {
			if outboxMsg == nil {
				continue
			}

			wg.Add(1)
			go func(msg *OutboxMessage) {
				defer wg.Done()
				processOutboxMessage(db, pool, logger, msg)
			}(outboxMsg)
		}

		wg.Wait()
	}
}

func processOutboxMessage(
	db *sql.DB,
	pool *Pool,
	logger *log.Logger,
	msg *OutboxMessage,
) {
	if msg.Message == nil {
		return
	}

	if err := pool.Dispatch(msg.QueueName, msg.Message); err != nil {
		logger.Error("Failed to dispatch outbox message", map[string]any{
			"outbox_id":  msg.ID,
			"queue":      msg.QueueName,
			"message_id": msg.Message.ID,
			"error":      err.Error(),
		})
		scheduleOutboxRetry(db, logger, msg, err)
		return
	}

	if err := deleteOutboxMessage(db, msg.ID); err != nil {
		logger.Error("Failed to delete dispatched outbox message", map[string]any{
			"outbox_id":  msg.ID,
			"queue":      msg.QueueName,
			"message_id": msg.Message.ID,
			"error":      err.Error(),
		})
		return
	}

	logger.Info("Outbox message dispatched", map[string]any{
		"outbox_id":  msg.ID,
		"queue":      msg.QueueName,
		"message_id": msg.Message.ID,
	})
}

func deleteOutboxMessage(
	db *sql.DB,
	id string,
) error {
	tx, err := db.BeginTx(context.TODO(), nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}

	if err := DeleteOutboxMessage(tx, id); err != nil {
		if err := tx.Rollback(); err != nil {
			panic(err)
		}
		return fmt.Errorf("failed to delete outbox message: %w", err)
	}

	if err := tx.Commit(); err != nil {
		if err := tx.Rollback(); err != nil {
			panic(err)
		}
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	return nil
}

func scheduleOutboxRetry(
	db *sql.DB,
	logger *log.Logger,
	msg *OutboxMessage,
	dispatchErr error,
) {
	retryCount := msg.RetryCount + 1
	delayMs := calculateExponentialBackoff(retryCount)
	availableAt := time.Now().Add(time.Duration(delayMs) * time.Millisecond)
	lastError := dispatchErr.Error()
	messageID := ""
	if msg.Message != nil {
		messageID = msg.Message.ID
	}

	tx, err := db.BeginTx(context.TODO(), nil)
	if err != nil {
		logger.Error("Failed to begin transaction for outbox retry", map[string]any{
			"outbox_id": msg.ID,
			"queue":     msg.QueueName,
			"error":     err.Error(),
		})
		return
	}
	defer func() {
		if err := tx.Rollback(); err != nil {
			logger.Error("Failed to rollback outbox retry transaction", map[string]any{
				"outbox_id": msg.ID,
				"queue":     msg.QueueName,
				"error":     err.Error(),
			})
		}
	}()

	if _, err := tx.Exec(
		`UPDATE queue_outbox SET retry_count = $1, available_at = $2, last_error = $3 WHERE id = $4`,
		retryCount,
		availableAt,
		lastError,
		msg.ID,
	); err != nil {
		logger.Error("Failed to update outbox message for retry", map[string]any{
			"outbox_id": msg.ID,
			"queue":     msg.QueueName,
			"error":     err.Error(),
		})
		return
	}

	if err := tx.Commit(); err != nil {
		logger.Error("Failed to commit outbox retry transaction", map[string]any{
			"outbox_id": msg.ID,
			"queue":     msg.QueueName,
			"error":     err.Error(),
		})
		return
	}

	msg.RetryCount = retryCount
	msg.AvailableAt = availableAt
	msg.LastError = &lastError

	logger.Warn("Scheduled outbox message retry", map[string]any{
		"outbox_id":  msg.ID,
		"queue":      msg.QueueName,
		"message_id": messageID,
		"retry":      retryCount,
		"delay_ms":   delayMs,
	})

	if retryCount >= MaxRetries {
		logger.Error("Outbox message reached max retries", map[string]any{
			"outbox_id":  msg.ID,
			"queue":      msg.QueueName,
			"message_id": messageID,
			"retry":      retryCount,
		})
	}
}

// fetchOutboxMessages retrieves available outbox messages from the database
func fetchOutboxMessages(db *sql.DB, limit int) ([]*OutboxMessage, error) {
	rows, err := db.Query(`
		SELECT id, queue_name, message, created_at, available_at, retry_count, last_error
		FROM queue_outbox
		WHERE available_at <= $1
		AND $2 > retry_count
		ORDER BY available_at ASC
		LIMIT $3
	`,
		time.Now(),
		MaxRetries,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch outbox messages: %w", err)
	}
	defer rows.Close()

	var messages []*OutboxMessage
	for rows.Next() {
		var msgJSON []byte
		msg := new(OutboxMessage)

		if err := rows.Scan(
			&msg.ID,
			&msg.QueueName,
			&msgJSON,
			&msg.CreatedAt,
			&msg.AvailableAt,
			&msg.RetryCount,
			&msg.LastError,
		); err != nil {
			return nil, fmt.Errorf("failed to scan outbox message: %w", err)
		}

		// Unmarshal the message JSON into msg.Message
		msg.Message = new(Message)
		if err := json.Unmarshal(msgJSON, msg.Message); err != nil {
			return nil, fmt.Errorf("error to unmarshal outbox message: %w", err)
		}
		messages = append(messages, msg)
	}

	return messages, nil
}
