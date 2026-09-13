package sql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

var (
	ErrNoRows = sql.ErrNoRows
	ErrTxDone = sql.ErrTxDone
)

type DB = sql.DB
type Tx = sql.Tx

type Row = sql.Row
type Rows = sql.Rows

type NullString = sql.NullString
type NullTime = sql.NullTime
type NullFloat64 = sql.NullFloat64

type Conf struct {
	Host        string
	Port        string
	Name        string
	User        string
	Password    string
	MaxPoolConn int
}

func NewDB(
	conf Conf,
) *sql.DB {
	connStr := fmt.Sprintf(
		"postgresql://%s:%s@%s:%s/%s?sslmode=disable",
		conf.User,
		conf.Password,
		conf.Host,
		conf.Port,
		conf.Name,
	)

	db, err := sql.Open("pgx", connStr)
	if err != nil {
		panic(err)
	}
	if conf.MaxPoolConn > 0 {
		db.SetMaxOpenConns(conf.MaxPoolConn)
	}

	return db
}

type Filter struct {
	Column   string
	Operator Operator
	Value    any
}

type Sorting struct {
	Column    string
	Direction Direction
}

type Direction string

const (
	DirectionAsc  Direction = "ASC"
	DirectionDesc Direction = "DESC"
)

type Operator string

const (
	OperatorEqual              Operator = "="
	OperatorNotEqual           Operator = "<>"
	OperatorGreaterThan        Operator = ">"
	OperatorGreaterThanOrEqual Operator = ">="
	OperatorLessThan           Operator = "<"
	OperatorLessThanOrEqual    Operator = "<="
	OperatorIn                 Operator = "IN"
	OperatorNotIn              Operator = "NOT IN"
	OperatorLike               Operator = "LIKE"
	OperatorNotLike            Operator = "NOT LIKE"
	OperatorILike              Operator = "ILIKE"
	OperatorNotILike           Operator = "NOT ILIKE"
	OperatorIsNotNull          Operator = "IS NOT NULL"
	OperatorIsNull             Operator = "IS NULL"
)

// WithTx runs fn inside a transaction. It commits when fn returns nil and
// rolls back otherwise. A rollback failure (other than ErrTxDone) is
// surfaced only when fn and commit succeeded.
func WithTx(ctx context.Context, db *DB, fn func(tx *Tx) error) (err error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer func() {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, ErrTxDone) && err == nil {
			err = fmt.Errorf("failed to rollback transaction: %w", rbErr)
		}
	}()

	if err = fn(tx); err != nil {
		return err
	}

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}
	return nil
}

// Listen takes a dedicated connection out of the pool, runs LISTEN on the
// given channel and calls onNotify for every NOTIFY received, until ctx is
// cancelled (returns nil) or the connection fails (returns the error, so the
// caller can re-listen with a backoff). The connection is discarded on return
// instead of going back to the pool, because a pooled connection with an
// active LISTEN would leak notifications into unrelated queries.
func Listen(ctx context.Context, db *DB, channel string, onNotify func()) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("failed to acquire listen connection: %w", err)
	}
	defer conn.Close()

	var result error
	rawErr := conn.Raw(func(driverConn any) error {
		stdConn, ok := driverConn.(*stdlib.Conn)
		if !ok {
			result = fmt.Errorf("unexpected driver connection type %T", driverConn)
			return driver.ErrBadConn
		}
		pgxConn := stdConn.Conn()
		defer pgxConn.Close(context.Background())

		if _, err := pgxConn.Exec(ctx, "LISTEN "+pgx.Identifier{channel}.Sanitize()); err != nil {
			result = fmt.Errorf("failed to listen on %s: %w", channel, err)
			return driver.ErrBadConn
		}

		for {
			if _, err := pgxConn.WaitForNotification(ctx); err != nil {
				if ctx.Err() != nil {
					result = nil
				} else {
					result = fmt.Errorf("failed waiting for notification on %s: %w", channel, err)
				}
				// ErrBadConn makes database/sql drop this connection.
				return driver.ErrBadConn
			}
			onNotify()
		}
	})
	if rawErr != nil && !errors.Is(rawErr, driver.ErrBadConn) {
		return fmt.Errorf("listen connection failed: %w", rawErr)
	}
	return result
}
