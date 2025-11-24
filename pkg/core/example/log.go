package example

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/sergioneiravargas/template-go/pkg/framework/queue"
	"github.com/sergioneiravargas/template-go/pkg/framework/sql"
)

type Log struct {
	ID      string `json:"id"`
	Message string `json:"message"`
}

type CreateLogInput struct {
	Message string
}

func (i CreateLogInput) Validate() error {
	if i.Message == "" {
		return fmt.Errorf("message cannot be empty")
	}
	return nil
}

func CreateLog(
	db *sql.DB,
	input CreateLogInput,
) (*Log, error) {
	if err := input.Validate(); err != nil {
		return nil, err
	}

	log := &Log{
		ID:      uuid.NewString(),
		Message: input.Message,
	}

	tx, err := db.BeginTx(context.TODO(), nil)
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(
		"INSERT INTO example_log (id, message) VALUES ($1, $2)",
		log.ID,
		log.Message,
	)
	if err != nil {
		if err := tx.Rollback(); err != nil {
			panic(err)
		}
		return nil, err
	}

	message, err := queue.NewMessage(
		QueueMessageLogCreated,
		log,
	)
	if err != nil {
		if err := tx.Rollback(); err != nil {
			panic(err)
		}
		return nil, fmt.Errorf("error creating message: %w", err)
	}

	err = queue.CreateOutboxMessage(
		tx,
		QueueExample,
		message,
	)
	if err != nil {
		if err := tx.Rollback(); err != nil {
			panic(err)
		}
		return nil, fmt.Errorf("error creating outbox message: %w", err)
	}

	if err := tx.Commit(); err != nil {
		if err := tx.Rollback(); err != nil {
			panic(err)
		}
		return nil, err
	}

	return log, nil
}
