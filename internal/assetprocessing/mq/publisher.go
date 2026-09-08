package mq

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	apiv1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"google.golang.org/protobuf/proto"
)

type Publisher struct {
	conn   *Connection
	client eventpkg.PGMQ
}

func NewPublisher(conn *Connection) (*Publisher, error) {
	if conn == nil || conn.IsClosed() {
		return nil, fmt.Errorf("open PostgreSQL connection is required")
	}
	return &Publisher{conn: conn}, nil
}

func (p *Publisher) PublishMeshOptimizationProgress(ctx context.Context, value *apiv1.MeshOptimizationProgressEvent) error {
	setTimestampIfMissing(&value.TimestampMs)
	return p.notify(ctx, eventpkg.SignalMeshOptimizationProgress, value.GetJobId(), "api.manage.v1.MeshOptimizationProgressEvent", value)
}

func (p *Publisher) PublishMeshOptimizationComplete(ctx context.Context, value *apiv1.MeshOptimizationCompleteEvent) error {
	setTimestampIfMissing(&value.TimestampMs)
	result := &apiv1.MeshOptimizationResultEvent{Outcome: &apiv1.MeshOptimizationResultEvent_Completed{Completed: value}}
	return p.enqueueResult(ctx, value.GetJobId(), result)
}

func (p *Publisher) PublishMeshOptimizationFail(ctx context.Context, value *apiv1.MeshOptimizationFailEvent) error {
	setTimestampIfMissing(&value.TimestampMs)
	result := &apiv1.MeshOptimizationResultEvent{Outcome: &apiv1.MeshOptimizationResultEvent_Failed{Failed: value}}
	return p.enqueueResult(ctx, value.GetJobId(), result)
}

func (p *Publisher) enqueueResult(ctx context.Context, messageID string, value proto.Message) error {
	startedAt := time.Now()
	body, err := proto.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshal mesh result: %w", err)
	}
	envelope, err := eventpkg.NewEnvelope(messageID, "api.manage.v1.MeshOptimizationResultEvent", body)
	if err != nil {
		return err
	}
	_, err = p.client.Enqueue(ctx, p.conn.DB(), eventpkg.QueueMeshOptimizationResult, envelope, injectMessageCorrelation(ctx, map[string]string{
		"content_type": eventpkg.ContentTypeProtobuf,
	}), 0)
	emitQueuePublishResult(ctx, eventpkg.QueueMeshOptimizationResult, messageID, time.Since(startedAt), err != nil)
	return err
}

func (p *Publisher) notify(ctx context.Context, signal, messageID, messageType string, value proto.Message) error {
	body, err := proto.Marshal(value)
	if err != nil {
		return err
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

func setTimestampIfMissing(timestamp *int64) {
	if *timestamp == 0 {
		*timestamp = time.Now().UnixMilli()
	}
}

func (p *Publisher) Close() error { return nil }
