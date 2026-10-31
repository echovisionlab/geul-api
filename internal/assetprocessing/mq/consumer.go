package mq

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/echovisionlab/geul-api/internal/assetprocessing/jobs"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

type Handler func(ctx context.Context, body []byte) error

type Consumer struct {
	conn      *Connection
	config    jobs.QueueConfig
	handler   Handler
	client    eventpkg.PGMQ
	done      chan struct{}
	closeOnce sync.Once
	wg        sync.WaitGroup
}

func NewConsumer(conn *Connection, config jobs.QueueConfig, handler Handler) (*Consumer, error) {
	if conn == nil || conn.IsClosed() {
		return nil, fmt.Errorf("open PostgreSQL connection is required")
	}
	if handler == nil || config.Name == "" || config.MessageType == "" || config.Workers <= 0 || config.Timeout <= 0 || config.RetryLimit < 0 {
		return nil, fmt.Errorf("valid consumer configuration and handler are required")
	}
	return &Consumer{conn: conn, config: config, handler: handler, done: make(chan struct{})}, nil
}

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
		if err != nil && ctx.Err() != nil {
			return
		}
		if err != nil {
			slog.ErrorContext(ctx, "PGMQ read failed", "queue", c.config.Name, "worker", workerID, failurecause.Attr(err))
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
	messageID, commandID := queueMessageIdentity(message)
	parent = extractDeliveryCorrelation(parent, message)
	if message.ContractError != "" || message.Envelope.MessageType != c.config.MessageType {
		cause := message.ContractError
		if cause == "" {
			cause = "unexpected queue message type"
		}
		slog.WarnContext(parent, "PGMQ contract rejected", failurecause.Attr(fmt.Errorf("%s", cause)), "queue", c.config.Name, "message_id", messageID, "command_id", commandID, "transport_id", message.TransportID, "retry_count", max(0, message.ReadCount-1))
		emitQueueDeliveryFailed(parent, message, c.config.Name, time.Since(startedAt), sharedtelemetry.QueueFailureHandlerFailed)
		c.deadLetter(parent, message, "PGMQ contract-invalid archive failed")
		return
	}
	body, err := message.Envelope.Payload()
	if err != nil {
		slog.WarnContext(parent, "PGMQ payload rejected", failurecause.Attr(err), "queue", c.config.Name, "message_id", messageID, "command_id", commandID, "transport_id", message.TransportID, "retry_count", max(0, message.ReadCount-1))
		emitQueueDeliveryFailed(parent, message, c.config.Name, time.Since(startedAt), sharedtelemetry.QueueFailureHandlerFailed)
		c.deadLetter(parent, message, "")
		return
	}
	jobCtx, cancel := context.WithTimeout(parent, c.config.Timeout)
	err = c.handler(jobCtx, body)
	if err != nil {
		level := slog.LevelError
		if jobCtx.Err() != nil {
			level = slog.LevelWarn
		}
		slog.Log(jobCtx, level, "PGMQ handler failed", append(failurecause.Args(err), "queue", c.config.Name, "message_id", messageID, "command_id", commandID, "transport_id", message.TransportID, "retry_count", max(0, message.ReadCount-1))...)
	}
	cancel()
	if isTerminalResult(err) && parent.Err() == nil {
		c.complete(parent, message, startedAt, "PGMQ terminal completion failed")
		return
	}
	if err == nil && parent.Err() == nil {
		c.complete(parent, message, startedAt, "PGMQ delete failed")
		return
	}
	if parent.Err() != nil {
		retryCtx, retryCancel := shutdownRequeueContext(parent)
		defer retryCancel()
		retryErr := c.client.Retry(retryCtx, c.conn.DB(), c.config.Name, message.TransportID, 0)
		if retryErr != nil {
			slog.ErrorContext(parent, "PGMQ shutdown requeue failed", failurecause.Attr(retryErr), "queue", c.config.Name, "message_id", messageID, "command_id", commandID, "transport_id", message.TransportID, "retry_count", max(0, message.ReadCount-1))
		}
		emitQueueDeliveryRequeued(retryCtx, message, c.config.Name, time.Since(startedAt))
		emitQueueRetryResult(retryCtx, message, c.config.Name, retryErr != nil)
		return
	}
	emitQueueDeliveryFailed(parent, message, c.config.Name, time.Since(startedAt), sharedtelemetry.QueueFailureHandlerFailed)
	if isRetryable(err) && message.ReadCount <= c.config.RetryLimit {
		delay := time.Duration(min(60, 5*(1<<max(0, message.ReadCount-1)))) * time.Second
		c.retry(parent, message, delay)
		return
	}
	c.deadLetter(parent, message, "PGMQ archive failed")
}

func isTerminalResult(err error) bool {
	var terminal interface{ TerminalResult() bool }
	return errors.As(err, &terminal) && terminal.TerminalResult()
}

func isRetryable(err error) bool {
	var retryable interface{ Retryable() bool }
	return errors.As(err, &retryable) && retryable.Retryable()
}

func (c *Consumer) complete(ctx context.Context, message eventpkg.Message, startedAt time.Time, failureMessage string) {
	messageID, commandID := queueMessageIdentity(message)
	err := c.client.Complete(ctx, c.conn.DB(), c.config.Name, message.TransportID)
	if err == nil {
		emitQueueDeliverySucceeded(ctx, message, c.config.Name, time.Since(startedAt))
		return
	}
	emitQueueDeliveryFailed(ctx, message, c.config.Name, time.Since(startedAt), sharedtelemetry.QueueFailureCompletionFailed)
	if failureMessage == "" {
		failureMessage = "PGMQ archive failed"
	}
	slog.ErrorContext(ctx, failureMessage, failurecause.Attr(err), "queue", c.config.Name, "message_id", messageID, "command_id", commandID, "transport_id", message.TransportID, "retry_count", max(0, message.ReadCount-1))
}

func (c *Consumer) retry(ctx context.Context, message eventpkg.Message, delay time.Duration) {
	messageID, commandID := queueMessageIdentity(message)
	err := c.client.Retry(ctx, c.conn.DB(), c.config.Name, message.TransportID, delay)
	emitQueueRetryResult(ctx, message, c.config.Name, err != nil)
	if err == nil {
		return
	}
	slog.ErrorContext(ctx, "PGMQ retry failed", failurecause.Attr(err), "queue", c.config.Name, "message_id", messageID, "command_id", commandID, "transport_id", message.TransportID, "retry_count", max(0, message.ReadCount-1))
}

func (c *Consumer) deadLetter(ctx context.Context, message eventpkg.Message, failureMessage string) {
	messageID, commandID := queueMessageIdentity(message)
	err := c.client.DeadLetter(ctx, c.conn.DB(), c.config.Name, message.TransportID)
	emitQueueDLQResult(ctx, message, c.config.Name, err != nil)
	if err == nil {
		return
	}
	if failureMessage == "" {
		failureMessage = "PGMQ archive failed"
	}
	slog.ErrorContext(ctx, failureMessage, failurecause.Attr(err), "queue", c.config.Name, "message_id", messageID, "command_id", commandID, "transport_id", message.TransportID, "retry_count", max(0, message.ReadCount-1))
}

func (c *Consumer) Close() error {
	c.closeOnce.Do(func() { close(c.done) })
	c.wg.Wait()
	return nil
}

const shutdownRequeueTimeout = 5 * time.Second

// shutdownRequeueContext requeues a delivery after the worker shutdown has
// cancelled parent, keeping its correlation and trace for the requeue
// telemetry.
func shutdownRequeueContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(parent), shutdownRequeueTimeout)
}
