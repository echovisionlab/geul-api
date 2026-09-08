package mq

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/echovisionlab/geul-api/internal/transcoding/jobresult"
	"github.com/echovisionlab/geul-api/internal/transcoding/jobs"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// Handler processes one queue message payload.
type Handler func(ctx context.Context, body []byte) error

// Consumer polls and processes messages from a configured PGMQ queue.
type Consumer struct {
	conn      *Connection
	config    jobs.QueueConfig
	handler   Handler
	done      chan struct{}
	closeOnce sync.Once
	wg        sync.WaitGroup
	client    eventpkg.PGMQ
}

// NewConsumer validates and constructs a PGMQ queue consumer.
func NewConsumer(conn *Connection, config jobs.QueueConfig, handler Handler) (*Consumer, error) {
	if conn == nil || conn.IsClosed() {
		return nil, fmt.Errorf("open PostgreSQL connection is required")
	}
	if config.Name == "" || config.MessageType == "" || config.Workers <= 0 || config.Timeout <= 0 || config.RetryLimit < 0 {
		return nil, fmt.Errorf("valid queue configuration is required")
	}
	if handler == nil {
		return nil, fmt.Errorf("handler is required")
	}
	return &Consumer{
		conn: conn, config: config, handler: handler, done: make(chan struct{}),
	}, nil
}

// Start verifies queue readiness and starts the configured workers.
func (c *Consumer) Start(ctx context.Context) error {
	var totalMessages int64
	if err := c.conn.DB().QueryRowContext(
		ctx,
		"SELECT total_messages FROM pgmq.metrics($1)",
		c.config.Name,
	).Scan(&totalMessages); err != nil {
		return fmt.Errorf("PGMQ readiness: %w", err)
	}
	for workerID := 0; workerID < c.config.Workers; workerID++ {
		c.wg.Add(1)
		go c.worker(ctx, workerID)
	}
	slog.Info("Started PGMQ consumer", "queue", c.config.Name, "workers", c.config.Workers)
	return nil
}

func (c *Consumer) worker(ctx context.Context, workerID int) {
	defer c.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.done:
			return
		default:
		}
		messages, err := c.client.Read(ctx, c.conn.DB(), c.config.Name, c.config.Timeout+time.Minute, 1)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Error("PGMQ read failed", "queue", c.config.Name, "worker", workerID, "error", err)
			time.Sleep(250 * time.Millisecond)
			continue
		}
		if len(messages) == 0 {
			time.Sleep(250 * time.Millisecond)
			continue
		}
		c.process(ctx, messages[0])
	}
}

func (c *Consumer) process(parent context.Context, message eventpkg.Message) {
	startedAt := time.Now()
	parent = extractDeliveryCorrelation(parent, message, c.config.ServiceName)
	if message.ContractError != "" || message.Envelope.MessageType != c.config.MessageType {
		emitQueueDeliveryFailed(parent, message, c.config.Name, time.Since(startedAt), sharedtelemetry.QueueFailureHandlerFailed)
		c.deadLetter(parent, message, "PGMQ contract-invalid archive failed")
		return
	}
	body, err := message.Envelope.Payload()
	if err != nil {
		emitQueueDeliveryFailed(parent, message, c.config.Name, time.Since(startedAt), sharedtelemetry.QueueFailureHandlerFailed)
		c.deadLetter(parent, message, "")
		return
	}
	jobCtx, cancel := context.WithTimeout(parent, c.config.Timeout)
	err = c.handler(jobCtx, body)
	cancel()
	if c.completeTerminalOrSuccessful(parent, message, startedAt, err) {
		return
	}
	if parent.Err() != nil {
		retryCtx, retryCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer retryCancel()
		retryErr := c.client.Retry(retryCtx, c.conn.DB(), c.config.Name, message.TransportID, 0)
		emitQueueDeliveryRequeued(retryCtx, message, c.config.Name, time.Since(startedAt))
		emitQueueRetryResult(retryCtx, message, c.config.Name, retryErr != nil)
		return
	}
	emitQueueDeliveryFailed(parent, message, c.config.Name, time.Since(startedAt), sharedtelemetry.QueueFailureHandlerFailed)
	if c.retryIfAllowed(parent, message, err) {
		return
	}
	c.deadLetter(parent, message, "PGMQ archive failed")
}

func (c *Consumer) completeTerminalOrSuccessful(parent context.Context, message eventpkg.Message, startedAt time.Time, err error) bool {
	if jobresult.IsTerminal(err) && parent.Err() == nil {
		c.complete(parent, message, startedAt, "PGMQ terminal completion failed")
		return true
	}
	if err == nil && parent.Err() == nil {
		c.complete(parent, message, startedAt, "PGMQ delete failed")
		return true
	}
	return false
}

func (c *Consumer) complete(parent context.Context, message eventpkg.Message, startedAt time.Time, failureMessage string) {
	completeErr := c.client.Complete(parent, c.conn.DB(), c.config.Name, message.TransportID)
	if completeErr != nil {
		emitQueueDeliveryFailed(parent, message, c.config.Name, time.Since(startedAt), sharedtelemetry.QueueFailureCompletionFailed)
		slog.Error(failureMessage, "queue", c.config.Name, "message_id", message.TransportID, "error", completeErr)
		return
	}
	emitQueueDeliverySucceeded(parent, message, c.config.Name, time.Since(startedAt))
}

func (c *Consumer) retryIfAllowed(parent context.Context, message eventpkg.Message, err error) bool {
	if !jobresult.IsRetry(err) || message.ReadCount > c.config.RetryLimit {
		return false
	}
	delay := time.Duration(min(60, 5*(1<<max(0, message.ReadCount-1)))) * time.Second
	retryErr := c.client.Retry(parent, c.conn.DB(), c.config.Name, message.TransportID, delay)
	emitQueueRetryResult(parent, message, c.config.Name, retryErr != nil)
	if retryErr != nil {
		slog.Error("PGMQ retry failed", "queue", c.config.Name, "message_id", message.TransportID, "error", retryErr)
	}
	return true
}

func (c *Consumer) deadLetter(parent context.Context, message eventpkg.Message, failureMessage string) {
	archiveErr := c.client.DeadLetter(parent, c.conn.DB(), c.config.Name, message.TransportID)
	emitQueueDLQResult(parent, message, c.config.Name, archiveErr != nil)
	if archiveErr == nil || failureMessage == "" {
		return
	}
	slog.Error(failureMessage, "queue", c.config.Name, "message_id", message.TransportID, "error", archiveErr)
}

// Close stops the consumer and waits for its workers to exit.
func (c *Consumer) Close() error {
	c.closeOnce.Do(func() { close(c.done) })
	c.wg.Wait()
	return nil
}
