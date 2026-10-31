package mq

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/echovisionlab/geul-api/internal/failurecause"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"github.com/jackc/pgx/v5"
)

type BroadcastHandler func(ctx context.Context, body []byte) error

// BroadcastSubscriber retains the existing name at the API boundary while the
// implementation is now PostgreSQL LISTEN/NOTIFY. Signals are latency hints;
// durable work never uses this type.
type BroadcastSubscriber struct {
	databaseDSN string
	signal      string
	handler     Handler
	closed      chan struct{}
	closeOnce   sync.Once
	mu          sync.Mutex
	conn        *pgx.Conn
}

func NewBroadcastSubscriber(
	databaseDSN string,
	signal string,
	handler BroadcastHandler,
	middlewares ...Middleware,
) (*BroadcastSubscriber, error) {
	if databaseDSN == "" {
		return nil, fmt.Errorf("database DSN is required")
	}
	if signal == "" {
		return nil, fmt.Errorf("signal is required")
	}
	if handler == nil {
		return nil, fmt.Errorf("signal handler is required")
	}
	wrapped := ChainMiddlewares(func(ctx context.Context, message Message) error {
		return handler(ctx, message.Body)
	}, middlewares...)
	return &BroadcastSubscriber{
		databaseDSN: databaseDSN, signal: signal, handler: wrapped, closed: make(chan struct{}),
	}, nil
}

func (s *BroadcastSubscriber) Start(ctx context.Context) error {
	for attempt := 1; ctx.Err() == nil; attempt++ {
		if isClosed(s.closed) {
			return nil
		}
		conn, err := pgx.Connect(ctx, s.databaseDSN)
		if err == nil {
			_, err = conn.Exec(ctx, "LISTEN "+pgx.Identifier{s.signal}.Sanitize())
		}
		if err != nil {
			if conn != nil {
				if closeErr := conn.Close(context.Background()); closeErr != nil {
					slog.ErrorContext(ctx, "PostgreSQL signal connection close failed", "signal", s.signal, "failure_cause", failurecause.Message(closeErr))
				}
			}
			if ctx.Err() == nil && !isClosed(s.closed) {
				slog.ErrorContext(ctx, "PostgreSQL signal listener connect failed", "signal", s.signal, "attempt", attempt, "failure_cause", failurecause.Message(err, s.databaseDSN))
			}
			emitPostgreSQLSignalDependencyDegraded(ctx, "listener_connect_failed")
			if !waitForSignalReconnect(ctx, s.closed, broadcastReconnectDelay(attempt)) {
				return nil
			}
			continue
		}
		s.setConnection(conn)
		if attempt > 1 {
			emitPostgreSQLSignalDependencyRecovered(ctx)
		}
		slog.InfoContext(ctx, "PostgreSQL signal subscriber started", "signal", s.signal)
		attempt = 0
		for ctx.Err() == nil && !isClosed(s.closed) {
			notification, waitErr := conn.WaitForNotification(ctx)
			if waitErr != nil {
				if ctx.Err() == nil && !isClosed(s.closed) {
					slog.ErrorContext(ctx, "PostgreSQL signal listener disconnected", "signal", s.signal, "failure_cause", failurecause.Message(waitErr))
				}
				break
			}
			// process logs failures with the decoded message identity before the
			// listener continues to the next signal.
			_ = s.process(ctx, notification.Payload)
		}
		s.clearConnection(conn)
		if closeErr := conn.Close(context.Background()); closeErr != nil {
			slog.ErrorContext(ctx, "PostgreSQL signal connection close failed", "signal", s.signal, "failure_cause", failurecause.Message(closeErr))
		}
		if ctx.Err() != nil || isClosed(s.closed) {
			return nil //nolint:nilerr // context cancellation or Close is the normal shutdown path, not a listener failure
		}
		emitPostgreSQLSignalDependencyDegraded(ctx, "listener_disconnected")
	}
	return nil
}

func (s *BroadcastSubscriber) process(ctx context.Context, payload string) (resultErr error) {
	var envelope eventpkg.Envelope
	defer func() {
		if resultErr != nil {
			commandID := queueCommandID(Message{MessageID: envelope.MessageID, CorrelationID: envelope.CorrelationID})
			slog.ErrorContext(ctx, "PostgreSQL signal handler failed", append(failurecause.Args(resultErr), "signal", s.signal, "message_id", envelope.MessageID, "command_id", commandID)...)
		}
	}()
	if err := json.Unmarshal([]byte(payload), &envelope); err != nil {
		return fmt.Errorf("decode signal envelope: %w", err)
	}
	body, err := envelope.Payload()
	if err != nil {
		return err
	}
	return s.handler(ctx, Message{
		Body:          body,
		Queue:         s.signal,
		ContentType:   eventpkg.ContentTypeProtobuf,
		MessageID:     envelope.MessageID,
		CorrelationID: envelope.CorrelationID,
		Timestamp:     time.Now().UTC(),
	})
}

func (s *BroadcastSubscriber) setConnection(conn *pgx.Conn) {
	s.mu.Lock()
	s.conn = conn
	s.mu.Unlock()
}

func (s *BroadcastSubscriber) clearConnection(conn *pgx.Conn) {
	s.mu.Lock()
	if s.conn == conn {
		s.conn = nil
	}
	s.mu.Unlock()
}

func (s *BroadcastSubscriber) Close() error {
	s.closeOnce.Do(func() { close(s.closed) })
	s.mu.Lock()
	conn := s.conn
	s.mu.Unlock()
	if conn != nil {
		return conn.Close(context.Background())
	}
	return nil
}

func waitForSignalReconnect(ctx context.Context, closed <-chan struct{}, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-closed:
		return false
	case <-timer.C:
		return true
	}
}

func isClosed(closed <-chan struct{}) bool {
	select {
	case <-closed:
		return true
	default:
		return false
	}
}

func broadcastReconnectDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt >= 20 {
		return 5 * time.Second
	}
	return time.Duration(attempt) * 250 * time.Millisecond
}
