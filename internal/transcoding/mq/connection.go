// Package mq owns the PGMQ work/result transport and PostgreSQL signals.
package mq

import (
	"context"
	"database/sql"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	// Register the pgx database/sql driver used by openPostgres.
	_ "github.com/jackc/pgx/v5/stdlib"
)

// Connection is the shared, bounded PostgreSQL pool used by PGMQ consumers
// and publishers. It does not own application domain-table privileges.
type Connection struct {
	db     *sql.DB
	dsn    string
	closed atomic.Bool
}

type signalConnection interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	WaitForNotification(context.Context) (*pgconn.Notification, error)
	Close(context.Context) error
}

var openPostgres = func(dsn string) (*sql.DB, error) { return sql.Open("pgx", dsn) }
var connectSignal = func(ctx context.Context, dsn string) (signalConnection, error) {
	return pgx.Connect(ctx, dsn)
}

// NewConnection opens and validates a shared PostgreSQL connection pool.
func NewConnection(dsn string) (*Connection, error) {
	if dsn == "" {
		return nil, fmt.Errorf("database DSN is required")
	}
	db, err := openPostgres(dsn)
	if err != nil {
		return nil, fmt.Errorf("open PostgreSQL: %w", err)
	}
	db.SetMaxOpenConns(12)
	db.SetMaxIdleConns(4)
	if err := db.PingContext(context.Background()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connect to PostgreSQL: %w", err)
	}
	return &Connection{db: db, dsn: dsn}, nil
}

// DB returns the underlying PostgreSQL connection pool.
func (c *Connection) DB() *sql.DB { return c.db }

// IsClosed reports whether the connection is nil or has been closed.
func (c *Connection) IsClosed() bool { return c == nil || c.closed.Load() }

// Healthy reports whether PostgreSQL responds within the health-check timeout.
func (c *Connection) Healthy() bool {
	if c.IsClosed() {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return c.db.PingContext(ctx) == nil
}

// Close closes the shared PostgreSQL connection pool once.
func (c *Connection) Close() error {
	if c == nil || !c.closed.CompareAndSwap(false, true) {
		return nil
	}
	return c.db.Close()
}

func (c *Connection) listen(
	ctx context.Context,
	signal string,
	ready func(),
	handle func([]byte),
) error {
	conn, err := connectSignal(ctx, c.dsn)
	if err != nil {
		return fmt.Errorf("connect signal listener: %w", err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	if _, err := conn.Exec(ctx, "LISTEN "+pgx.Identifier{signal}.Sanitize()); err != nil {
		return fmt.Errorf("listen %s: %w", signal, err)
	}
	ready()
	for {
		notification, err := conn.WaitForNotification(ctx)
		if err != nil {
			return err
		}
		handle([]byte(notification.Payload))
	}
}
