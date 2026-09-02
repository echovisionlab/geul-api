package mq

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"time"

	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

const pgmqIdlePollInterval = 250 * time.Millisecond

type QueueConsumer struct {
	db         *sql.DB
	config     QueueConfig
	handler    Handler
	pgmq       eventpkg.PGMQ
	closeOnce  sync.Once
	closeCh    chan struct{}
	workerDone sync.WaitGroup
}

func NewQueueConsumer(db *sql.DB, config QueueConfig, handler Handler, middlewares ...Middleware) *QueueConsumer {
	workers := config.Workers
	if workers <= 0 {
		workers = 1
	}
	config.Workers = workers
	if config.RetryDelay <= 0 {
		config.RetryDelay = time.Second
	}
	if config.RetryBackoff < 1 {
		config.RetryBackoff = 1
	}
	return &QueueConsumer{
		db: db, config: config, handler: ChainMiddlewares(handler, middlewares...), closeCh: make(chan struct{}),
	}
}

func (c *QueueConsumer) start(ctx context.Context, ready func(error)) error {
	if c.db == nil {
		err := fmt.Errorf("PostgreSQL connection is required")
		ready(err)
		return err
	}
	if c.config.Name == "" || c.config.MessageType == "" {
		err := fmt.Errorf("queue name and message type are required")
		ready(err)
		return err
	}
	var queueLength, totalMessages int64
	var newestAge, oldestAge sql.NullInt64
	if err := c.db.QueryRowContext(
		ctx,
		"SELECT queue_length, newest_msg_age_sec, oldest_msg_age_sec, total_messages FROM pgmq.metrics($1)",
		c.config.Name,
	).Scan(&queueLength, &newestAge, &oldestAge, &totalMessages); err != nil {
		ready(fmt.Errorf("verify PGMQ queue %s: %w", c.config.Name, err))
		return fmt.Errorf("verify PGMQ queue %s: %w", c.config.Name, err)
	}
	ready(nil)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	for range c.config.Workers {
		c.workerDone.Add(1)
		go func() {
			defer c.workerDone.Done()
			c.runWorker(runCtx)
		}()
	}
	select {
	case <-ctx.Done():
	case <-c.closeCh:
	}
	c.workerDone.Wait()
	return nil
}

func (c *QueueConsumer) runWorker(ctx context.Context) {
	visibilityTimeout := c.config.Timeout + time.Minute
	if c.config.Timeout == 0 {
		visibilityTimeout = time.Hour
	}
	for ctx.Err() == nil {
		messages, err := c.pgmq.Read(ctx, c.db, c.config.Name, visibilityTimeout, 1)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Error("PGMQ read failed", "queue", c.config.Name, "error", err)
			if !waitForQueuePoll(ctx, c.closeCh, time.Second) {
				return
			}
			continue
		}
		if len(messages) == 0 {
			if !waitForQueuePoll(ctx, c.closeCh, pgmqIdlePollInterval) {
				return
			}
			continue
		}
		c.processMessage(ctx, messages[0])
	}
}

func waitForQueuePoll(ctx context.Context, closeCh <-chan struct{}, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-closeCh:
		return false
	case <-timer.C:
		return true
	}
}

func (c *QueueConsumer) processMessage(parent context.Context, delivery eventpkg.Message) {
	if delivery.ContractError != "" || delivery.Envelope.MessageType != c.config.MessageType {
		messageID := delivery.Envelope.MessageID
		if messageID == "" {
			messageID = fmt.Sprintf("invalid:%d", delivery.TransportID)
		}
		message := Message{TransportID: delivery.TransportID, Queue: c.config.Name, MessageID: messageID, CorrelationID: delivery.Envelope.CorrelationID, RetryCount: max(0, delivery.ReadCount-1), Timestamp: delivery.EnqueuedAt}
		emitQueueDeliveryFailed(parent, c.config.Name, message, message.RetryCount, 0, sharedtelemetry.QueueFailureHandlerFailed)
		if archiveErr := c.pgmq.DeadLetter(parent, c.db, c.config.Name, delivery.TransportID); archiveErr != nil {
			emitQueueHandoff(parent, sharedtelemetry.EventQueueDLQFailed, c.config.Name, message, message.RetryCount, sharedtelemetry.QueueFailureArchiveFailed)
			slog.Error("PGMQ contract-invalid archive failed", "queue", c.config.Name, "message_id", delivery.TransportID, "error", archiveErr)
			return
		}
		emitQueueHandoff(parent, sharedtelemetry.EventQueueDLQAccepted, c.config.Name, message, message.RetryCount, "")
		return
	}
	body, err := delivery.Envelope.Payload()
	if err != nil {
		slog.Error("Invalid PGMQ envelope payload", "queue", c.config.Name, "message_id", delivery.TransportID, "error", err)
		_ = c.pgmq.DeadLetter(parent, c.db, c.config.Name, delivery.TransportID)
		return
	}
	retryCount := delivery.ReadCount - 1
	message := Message{
		TransportID:   delivery.TransportID,
		Body:          body,
		Queue:         c.config.Name,
		ContentType:   eventpkg.ContentTypeProtobuf,
		MessageID:     delivery.Envelope.MessageID,
		CorrelationID: delivery.Envelope.CorrelationID,
		Timestamp:     delivery.EnqueuedAt,
		RetryCount:    retryCount,
		Redelivered:   delivery.ReadCount > 1,
		Headers:       stringHeaders(delivery.Headers),
	}
	ctx, cancel := context.WithCancel(parent)
	if c.config.Timeout > 0 {
		ctx, cancel = context.WithTimeout(parent, c.config.Timeout)
	}
	defer cancel()
	ctx, span := StartConsumerSpan(ctx, message)
	defer span.End()
	startedAt := time.Now()
	err = c.handler(ctx, message)
	if parent.Err() != nil {
		requeueCtx, requeueCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer requeueCancel()
		if retryErr := c.pgmq.Retry(requeueCtx, c.db, c.config.Name, delivery.TransportID, 0); retryErr != nil {
			emitQueueHandoff(requeueCtx, sharedtelemetry.EventQueueRetryFailed, c.config.Name, message, retryCount, sharedtelemetry.QueueFailureVisibilityUpdateFailed)
			slog.Error("PGMQ shutdown visibility reset failed", "queue", c.config.Name, "message_id", delivery.TransportID, "error", retryErr)
			return
		}
		emitQueueHandoff(requeueCtx, sharedtelemetry.EventQueueRetryAccepted, c.config.Name, message, retryCount, "")
		emitQueueDeliveryRequeued(requeueCtx, c.config.Name, message, retryCount, time.Since(startedAt), sharedtelemetry.QueueFailureShutdown)
		return
	}
	if err == nil {
		if completeErr := c.pgmq.Complete(parent, c.db, c.config.Name, delivery.TransportID); completeErr != nil {
			RecordConsumerError(span, completeErr)
			slog.Error("PGMQ delete failed", "queue", c.config.Name, "message_id", delivery.TransportID, "error", completeErr)
			return
		}
		emitQueueDeliverySucceeded(parent, c.config.Name, message, retryCount, time.Since(startedAt))
		return
	}
	RecordConsumerError(span, err)
	if _, terminal := TerminalDeliveryErrorClass(err); terminal || retryCount >= c.config.MaxRetries {
		if archiveErr := c.pgmq.DeadLetter(parent, c.db, c.config.Name, delivery.TransportID); archiveErr != nil {
			emitQueueHandoff(parent, sharedtelemetry.EventQueueDLQFailed, c.config.Name, message, retryCount, sharedtelemetry.QueueFailureArchiveFailed)
			slog.Error("PGMQ archive failed", "queue", c.config.Name, "message_id", delivery.TransportID, "error", archiveErr)
			return
		}
		emitQueueHandoff(parent, sharedtelemetry.EventQueueDLQAccepted, c.config.Name, message, retryCount, "")
		emitQueueDeliveryFailed(parent, c.config.Name, message, retryCount, time.Since(startedAt), sharedtelemetry.QueueFailureHandlerFailed)
		return
	}
	delay := retryDelay(c.config, retryCount)
	if retryErr := c.pgmq.Retry(parent, c.db, c.config.Name, delivery.TransportID, delay); retryErr != nil {
		emitQueueHandoff(parent, sharedtelemetry.EventQueueRetryFailed, c.config.Name, message, retryCount, sharedtelemetry.QueueFailureVisibilityUpdateFailed)
		slog.Error("PGMQ retry visibility update failed", "queue", c.config.Name, "message_id", delivery.TransportID, "error", retryErr)
		return
	}
	emitQueueHandoff(parent, sharedtelemetry.EventQueueRetryAccepted, c.config.Name, message, retryCount+1, "")
	emitQueueDeliveryRequeued(parent, c.config.Name, message, retryCount+1, time.Since(startedAt), sharedtelemetry.QueueFailureHandlerFailed)
}

func retryDelay(config QueueConfig, retryCount int) time.Duration {
	multiplier := math.Pow(config.RetryBackoff, float64(retryCount))
	return time.Duration(float64(config.RetryDelay) * multiplier)
}

func (c *QueueConsumer) Close() error {
	c.closeOnce.Do(func() { close(c.closeCh) })
	return nil
}
