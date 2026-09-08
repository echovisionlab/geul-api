package mq

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"google.golang.org/protobuf/proto"
)

// Publisher sends durable results and transient progress through PostgreSQL.
type Publisher struct {
	conn   *Connection
	client eventpkg.PGMQ
}

// NewPublisher creates a publisher backed by an open PostgreSQL connection.
func NewPublisher(conn *Connection) (*Publisher, error) {
	if conn == nil || conn.IsClosed() {
		return nil, fmt.Errorf("open PostgreSQL connection is required")
	}
	return &Publisher{conn: conn}, nil
}

func (p *Publisher) enqueue(
	ctx context.Context,
	queue, messageID, messageType string,
	message proto.Message,
) error {
	startedAt := time.Now()
	body, err := (proto.MarshalOptions{Deterministic: true}).Marshal(message)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	envelope, err := eventpkg.NewEnvelope(messageID, messageType, body)
	if err != nil {
		return err
	}
	_, err = p.client.Enqueue(ctx, p.conn.DB(), queue, envelope, injectMessageCorrelation(ctx, map[string]string{
		"content_type": eventpkg.ContentTypeProtobuf,
	}), 0)
	emitQueuePublishResult(ctx, queue, messageID, time.Since(startedAt), err != nil)
	return err
}

func (p *Publisher) notify(
	ctx context.Context,
	signal, messageID, messageType string,
	message proto.Message,
) error {
	body, err := (proto.MarshalOptions{Deterministic: true}).Marshal(message)
	if err != nil {
		return fmt.Errorf("marshal signal: %w", err)
	}
	envelope, err := eventpkg.NewEnvelope(messageID, messageType, body)
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(envelope)
	if len(payload) >= 8_000 {
		return fmt.Errorf("signal %s exceeds PostgreSQL NOTIFY payload limit", signal)
	}
	_, err = p.conn.DB().ExecContext(ctx, "SELECT pg_notify($1, $2)", signal, payload)
	return err
}

// Close is a no-op because Publisher owns no resources.
func (p *Publisher) Close() error { return nil }

func ensureTimestamp(value *int64) {
	if *value == 0 {
		*value = time.Now().UnixMilli()
	}
}
