package mq

import (
	"context"
	"log/slog"
	"strings"
	"time"

	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func emitQueuePublishResult(ctx context.Context, queue, messageID string, duration time.Duration, reason sharedtelemetry.QueueFailureReason) {
	metadata := systemMetadata(ctx)
	queueContext := sharedtelemetry.QueuePublishContext{
		Queue: queue, MessageID: messageID, CommandID: messageID, DurationMS: duration.Milliseconds(),
	}
	var (
		record sharedtelemetry.SystemRecord
		err    error
	)
	if reason == "" {
		record, err = sharedtelemetry.NewQueuePublishSucceededRecord(metadata, queueContext)
	} else {
		record, err = sharedtelemetry.NewQueuePublishFailedRecord(metadata, queueContext, reason)
	}
	emitSystem(ctx, record, err)
}

func emitQueueDeliverySucceeded(ctx context.Context, queue string, delivery Message, retryCount int, duration time.Duration) {
	record, err := sharedtelemetry.NewQueueDeliverySucceededRecord(systemMetadata(ctx), queueDeliveryTelemetry(queue, delivery, retryCount, duration))
	emitSystem(ctx, record, err)
}

func emitQueueDeliveryFailed(ctx context.Context, queue string, delivery Message, retryCount int, duration time.Duration, reason sharedtelemetry.QueueFailureReason) {
	record, err := sharedtelemetry.NewQueueDeliveryFailedRecord(
		systemMetadata(ctx), queueDeliveryTelemetry(queue, delivery, retryCount, duration), reason,
	)
	emitSystem(ctx, record, err)
}

func emitQueueDeliveryRequeued(ctx context.Context, queue string, delivery Message, retryCount int, duration time.Duration, reason sharedtelemetry.QueueFailureReason) {
	record, err := sharedtelemetry.NewQueueDeliveryRequeuedRecord(
		systemMetadata(ctx), queueDeliveryTelemetry(queue, delivery, retryCount, duration), reason,
	)
	emitSystem(ctx, record, err)
}

func emitQueueHandoff(ctx context.Context, event sharedtelemetry.SystemEvent, queue string, delivery Message, retryCount int, reason sharedtelemetry.QueueFailureReason) {
	queueContext := sharedtelemetry.QueueHandoffContext{
		Queue: queue, MessageID: strings.TrimSpace(delivery.MessageID), CommandID: queueCommandID(delivery), RetryCount: retryCount,
	}
	metadata := systemMetadata(ctx)
	var (
		record sharedtelemetry.SystemRecord
		err    error
	)
	switch event {
	case sharedtelemetry.EventQueueRetryAccepted:
		record, err = sharedtelemetry.NewQueueRetryAcceptedRecord(metadata, queueContext)
	case sharedtelemetry.EventQueueRetryFailed:
		record, err = sharedtelemetry.NewQueueRetryFailedRecord(metadata, queueContext, reason)
	case sharedtelemetry.EventQueueDLQAccepted:
		record, err = sharedtelemetry.NewQueueDLQAcceptedRecord(metadata, queueContext)
	case sharedtelemetry.EventQueueDLQFailed:
		record, err = sharedtelemetry.NewQueueDLQFailedRecord(metadata, queueContext, reason)
	default:
		return
	}
	emitSystem(ctx, record, err)
}

func emitPostgreSQLSignalDependencyDegraded(ctx context.Context, reason string) {
	record, err := sharedtelemetry.NewDependencyDegradedRecord(
		systemMetadata(ctx), "postgresql", "signal_listen", sharedtelemetry.SystemFailure{Reason: reason},
	)
	emitSystem(ctx, record, err)
}

func emitPostgreSQLSignalDependencyRecovered(ctx context.Context) {
	record, err := sharedtelemetry.NewDependencyRecoveredRecord(systemMetadata(ctx), "postgresql", "signal_listen")
	emitSystem(ctx, record, err)
}

func queueDeliveryTelemetry(queue string, delivery Message, retryCount int, duration time.Duration) sharedtelemetry.QueueDeliveryContext {
	return sharedtelemetry.QueueDeliveryContext{
		Queue: queue, MessageID: strings.TrimSpace(delivery.MessageID), CommandID: queueCommandID(delivery), RetryCount: retryCount, DurationMS: duration.Milliseconds(),
	}
}

func queueCommandID(delivery Message) string {
	if commandID := strings.TrimSpace(delivery.CorrelationID); commandID != "" {
		return commandID
	}
	return strings.TrimSpace(delivery.MessageID)
}

func systemMetadata(ctx context.Context) sharedtelemetry.SystemMetadata {
	return sharedtelemetry.SystemMetadata{OccurredAt: time.Now().UTC(), Correlation: sharedtelemetry.CorrelationFromContext(ctx)}
}

func emitSystem(ctx context.Context, record sharedtelemetry.SystemRecord, buildErr error) {
	if buildErr != nil {
		return
	}
	_ = sharedtelemetry.EmitSystem(ctx, slog.Default().Handler(), record)
}
