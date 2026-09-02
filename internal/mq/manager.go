package mq

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

const consumerFatalDrainTimeout = time.Second

// ConsumerManager orchestrates multiple queue consumers
type ConsumerManager struct {
	db        *sql.DB
	consumers []*QueueConsumer
	mu        sync.Mutex
	closed    bool
}

// NewConsumerManager creates a new manager
func NewConsumerManager(db *sql.DB) *ConsumerManager {
	return &ConsumerManager{db: db}
}

// Register adds a queue consumer with configuration and middlewares
func (m *ConsumerManager) Register(config QueueConfig, handler Handler, middlewares ...Middleware) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return fmt.Errorf("manager is closed")
	}

	consumer := NewQueueConsumer(m.db, config, handler, middlewares...)
	m.consumers = append(m.consumers, consumer)

	slog.Info("Registered queue consumer",
		"queue", config.Name,
		"workers", config.Workers,
	)
	return nil
}

// StartWithReady begins all consumers and reports once every consumer-owned
// queue topology declaration and Consume registration has succeeded. The
// caller must receive from ready concurrently.
func (m *ConsumerManager) StartWithReady(ctx context.Context, ready chan<- error) error {
	return m.start(ctx, ready)
}

func (m *ConsumerManager) start(ctx context.Context, ready chan<- error) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return fmt.Errorf("manager is closed")
	}
	consumers := m.consumers
	m.mu.Unlock()

	if len(consumers) == 0 {
		slog.Warn("No consumers registered")
		if ready != nil {
			ready <- nil
		}
		<-ctx.Done()
		return nil
	}

	slog.Info("Starting consumer manager", "queues", len(consumers))

	consumerReady := make(chan error, len(consumers))
	starts := make([]func(context.Context) error, 0, len(consumers))
	for _, consumer := range consumers {
		current := consumer
		starts = append(starts, func(runCtx context.Context) error {
			if err := current.start(runCtx, func(err error) {
				consumerReady <- err
			}); err != nil {
				return fmt.Errorf("queue %s: %w", current.config.Name, err)
			}
			return nil
		})
	}

	groupDone := make(chan error, 1)
	go func() {
		groupDone <- runConsumerGroup(ctx, starts)
	}()

	for range consumers {
		select {
		case readyErr := <-consumerReady:
			if readyErr != nil {
				if ready != nil {
					ready <- readyErr
				}
				firstErr := <-groupDone
				slog.Info("Consumer manager stopped")
				return firstErr
			}
		case firstErr := <-groupDone:
			if firstErr == nil && ctx.Err() == nil {
				firstErr = fmt.Errorf("consumer manager stopped before topology became ready")
			}
			if ready != nil {
				ready <- firstErr
			}
			slog.Info("Consumer manager stopped")
			return firstErr
		case <-ctx.Done():
			if ready != nil {
				ready <- ctx.Err()
			}
			firstErr := <-groupDone
			slog.Info("Consumer manager stopped")
			return firstErr
		}
	}

	if ready != nil {
		ready <- nil
	}
	firstErr := <-groupDone

	slog.Info("Consumer manager stopped")
	return firstErr
}

func runConsumerGroup(
	ctx context.Context,
	starts []func(context.Context) error,
) error {
	return runConsumerGroupWithDrainTimeout(ctx, starts, consumerFatalDrainTimeout)
}

func runConsumerGroupWithDrainTimeout(
	ctx context.Context,
	starts []func(context.Context) error,
	drainTimeout time.Duration,
) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	results := make(chan error, len(starts))
	for _, start := range starts {
		current := start
		go func() {
			results <- current(runCtx)
		}()
	}

	remaining := len(starts)
	drainRemaining := func(firstErr error, reason string) error {
		if remaining == 0 {
			return firstErr
		}
		timer := time.NewTimer(drainTimeout)
		defer timer.Stop()
		for remaining > 0 {
			select {
			case siblingErr := <-results:
				remaining--
				if siblingErr != nil {
					slog.Error("Consumer error during drain", "reason", reason, "error", siblingErr)
					if firstErr == nil {
						firstErr = siblingErr
					}
				}
			case <-timer.C:
				slog.Error(
					"Consumer group drain timed out",
					"reason", reason,
					"remaining_consumers", remaining,
				)
				return firstErr
			}
		}
		return firstErr
	}

	for remaining > 0 {
		select {
		case err := <-results:
			remaining--
			if err == nil {
				continue
			}
			slog.Error("Consumer error", "error", err)
			cancel()
			return drainRemaining(err, "fatal_error")
		case <-ctx.Done():
			cancel()
			return drainRemaining(nil, "parent_cancelled")
		}
	}
	return nil
}

// Close closes all consumer channels
func (m *ConsumerManager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return nil
	}
	m.closed = true

	var firstErr error
	for _, consumer := range m.consumers {
		if err := consumer.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// QueueNames returns the names of all registered queues
func (m *ConsumerManager) QueueNames() []string {
	m.mu.Lock()
	defer m.mu.Unlock()

	names := make([]string, len(m.consumers))
	for i, c := range m.consumers {
		names[i] = c.config.Name
	}
	return names
}
