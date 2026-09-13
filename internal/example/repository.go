package example

import (
	"context"
	"errors"
	"fmt"

	"github.com/sergioneiravargas/template-go/internal/platform/queue"
	"github.com/sergioneiravargas/template-go/internal/platform/sql"
)

type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository {
	if db == nil {
		panic("db is required")
	}
	return &Repository{
		db: db,
	}
}

// CreateMessage inserts the example and its outbox messages in one transaction so the
// side effects are published only if the row commits.
func (r *Repository) CreateMessage(ctx context.Context, entry *Message, queueMessages ...*queue.Message) error {
	return sql.WithTx(ctx, r.db, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(
			ctx,
			"INSERT INTO example_message (id, body, created_at) VALUES ($1, $2, $3)",
			entry.ID,
			entry.Body,
			entry.CreatedAt,
		)
		if err != nil {
			return fmt.Errorf("failed to create message: %w", err)
		}

		if len(queueMessages) > 0 {
			if err := queue.CreateOutboxMessage(ctx, tx, QueueName, queueMessages...); err != nil {
				return fmt.Errorf("failed to create outbox messages: %w", err)
			}
		}

		return nil
	})
}

// GetMessage returns (nil, nil) when the message does not exist.
func (r *Repository) GetMessage(ctx context.Context, id string) (*Message, error) {
	var entry Message
	err := r.db.QueryRowContext(
		ctx,
		"SELECT id, body, created_at FROM example_message WHERE id = $1",
		id,
	).Scan(&entry.ID, &entry.Body, &entry.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get message: %w", err)
	}
	return &entry, nil
}
