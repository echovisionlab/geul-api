package mq

import (
	"context"
	"fmt"
	"time"

	apptelemetry "github.com/echovisionlab/geul-api/internal/assetprocessing/telemetry"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

type pgmqHeaderCarrier map[string]string

func (carrier pgmqHeaderCarrier) Get(key string) string { return carrier[key] }
func (carrier pgmqHeaderCarrier) Set(key, value string) { carrier[key] = value }
func (carrier pgmqHeaderCarrier) Keys() []string {
	keys := make([]string, 0, len(carrier))
	for key := range carrier {
		keys = append(keys, key)
	}
	return keys
}

func injectMessageCorrelation(ctx context.Context, headers map[string]string) map[string]string {
	if headers == nil {
		headers = map[string]string{}
	}
	sharedtelemetry.InjectCorrelation(ctx, pgmqHeaderCarrier(headers))
	return headers
}

func extractDeliveryCorrelation(ctx context.Context, message eventpkg.Message) context.Context {
	return sharedtelemetry.ExtractCorrelation(ctx, pgmqHeaderCarrier(message.Headers), sharedtelemetry.SystemActor{ServiceName: sharedtelemetry.ServiceAssetOptimizer})
}

func queueMessageIdentity(message eventpkg.Message) (string, string) {
	messageID := message.Envelope.MessageID
	if messageID == "" {
		messageID = fmt.Sprintf("invalid:%d", message.TransportID)
	}
	commandID := message.Envelope.CorrelationID
	if commandID == "" {
		commandID = messageID
	}
	return messageID, commandID
}

func queueDeliveryContext(message eventpkg.Message, queue string, duration time.Duration) sharedtelemetry.QueueDeliveryContext {
	messageID, commandID := queueMessageIdentity(message)
	return sharedtelemetry.QueueDeliveryContext{Queue: queue, MessageID: messageID, CommandID: commandID, RetryCount: max(0, message.ReadCount-1), DurationMS: duration.Milliseconds()}
}

func queueHandoffContext(message eventpkg.Message, queue string) sharedtelemetry.QueueHandoffContext {
	messageID, commandID := queueMessageIdentity(message)
	return sharedtelemetry.QueueHandoffContext{Queue: queue, MessageID: messageID, CommandID: commandID, RetryCount: max(0, message.ReadCount-1)}
}

func emitQueueDeliverySucceeded(ctx context.Context, message eventpkg.Message, queue string, duration time.Duration) {
	record, err := sharedtelemetry.NewQueueDeliverySucceededRecord(apptelemetry.SystemMetadata(ctx, time.Now()), queueDeliveryContext(message, queue, duration))
	_ = apptelemetry.EmitSystem(ctx, record, err)
}

func emitQueueDeliveryFailed(ctx context.Context, message eventpkg.Message, queue string, duration time.Duration, reason sharedtelemetry.QueueFailureReason) {
	record, err := sharedtelemetry.NewQueueDeliveryFailedRecord(apptelemetry.SystemMetadata(ctx, time.Now()), queueDeliveryContext(message, queue, duration), reason)
	_ = apptelemetry.EmitSystem(ctx, record, err)
}

func emitQueueDeliveryRequeued(ctx context.Context, message eventpkg.Message, queue string, duration time.Duration) {
	record, err := sharedtelemetry.NewQueueDeliveryRequeuedRecord(apptelemetry.SystemMetadata(ctx, time.Now()), queueDeliveryContext(message, queue, duration), sharedtelemetry.QueueFailureShutdown)
	_ = apptelemetry.EmitSystem(ctx, record, err)
}

func emitQueueRetryResult(ctx context.Context, message eventpkg.Message, queue string, failure bool) {
	metadata := apptelemetry.SystemMetadata(ctx, time.Now())
	handoff := queueHandoffContext(message, queue)
	if failure {
		record, err := sharedtelemetry.NewQueueRetryFailedRecord(metadata, handoff, sharedtelemetry.QueueFailureVisibilityUpdateFailed)
		_ = apptelemetry.EmitSystem(ctx, record, err)
		return
	}
	record, err := sharedtelemetry.NewQueueRetryAcceptedRecord(metadata, handoff)
	_ = apptelemetry.EmitSystem(ctx, record, err)
}

func emitQueueDLQResult(ctx context.Context, message eventpkg.Message, queue string, failure bool) {
	metadata := apptelemetry.SystemMetadata(ctx, time.Now())
	handoff := queueHandoffContext(message, queue)
	if failure {
		record, err := sharedtelemetry.NewQueueDLQFailedRecord(metadata, handoff, sharedtelemetry.QueueFailureArchiveFailed)
		_ = apptelemetry.EmitSystem(ctx, record, err)
		return
	}
	record, err := sharedtelemetry.NewQueueDLQAcceptedRecord(metadata, handoff)
	_ = apptelemetry.EmitSystem(ctx, record, err)
}

func emitQueuePublishResult(ctx context.Context, queue, messageID string, duration time.Duration, failure bool) {
	metadata := apptelemetry.SystemMetadata(ctx, time.Now())
	publish := sharedtelemetry.QueuePublishContext{Queue: queue, MessageID: messageID, CommandID: messageID, DurationMS: duration.Milliseconds()}
	if failure {
		record, err := sharedtelemetry.NewQueuePublishFailedRecord(metadata, publish, sharedtelemetry.QueueFailureEnqueueFailed)
		_ = apptelemetry.EmitSystem(ctx, record, err)
		return
	}
	record, err := sharedtelemetry.NewQueuePublishSucceededRecord(metadata, publish)
	_ = apptelemetry.EmitSystem(ctx, record, err)
}
