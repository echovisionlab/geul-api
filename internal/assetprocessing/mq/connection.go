// Package mq owns PGMQ work/result transport and PostgreSQL signals.
package mq

import (
	"context"
	"database/sql"
	"fmt"
	"sync/atomic"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

type Connection struct {
	db     *sql.DB
	closed atomic.Bool
}

var openPostgres = func(dsn string) (*sql.DB, error) { return sql.Open("pgx", dsn) }

func NewConnection(dsn string) (*Connection, error) {
	if dsn == "" {
		return nil, fmt.Errorf("database DSN is required")
	}
	db, err := openPostgres(dsn)
	if err != nil {
		return nil, fmt.Errorf("open PostgreSQL: %w", err)
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	if err := db.PingContext(context.Background()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connect to PostgreSQL: %w", err)
	}
	return &Connection{db: db}, nil
}

func (c *Connection) DB() *sql.DB    { return c.db }
func (c *Connection) IsClosed() bool { return c == nil || c.closed.Load() }
func (c *Connection) Healthy() bool {
	if c.IsClosed() {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return c.db.PingContext(ctx) == nil
}

func (c *Connection) Close() error {
	if c == nil || !c.closed.CompareAndSwap(false, true) {
		return nil
	}
	return c.db.Close()
}
