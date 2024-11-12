package sql

import (
	"database/sql"
	"fmt"
)

var (
	ErrConnDone = sql.ErrConnDone
	ErrNoRows   = sql.ErrNoRows
	ErrTxDone   = sql.ErrTxDone
)

type DB = sql.DB

type Conf struct {
	Host     string
	Port     string
	Name     string
	User     string
	Password string
	Driver   string
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

	db, err := sql.Open(conf.Driver, connStr)
	if err != nil {
		panic(err)
	}

	return db
}
