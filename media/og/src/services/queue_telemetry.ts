import { context, type TextMapGetter } from '@opentelemetry/api';
import {
  SERVICE_OG,
  buildQueueDeliveryFailedRecord,
  buildQueueDeliverySucceededRecord,
  buildQueueDLQAcceptedRecord,
  buildQueueRetryAcceptedRecord,
  extractCorrelation,
  runWithRequestContext,
  type QueueFailureReason,
} from '@echovisionlab/geul-telemetry';
import type { PgmqMessage } from '@echovisionlab/geul-event';
import { emitSystemRecord, systemMetadata } from '../system_logging.js';

type MessageHeaders = Record<string, string>;

const headerGetter: TextMapGetter<MessageHeaders> = {
  get: (carrier, key) => carrier[key],
  keys: Object.keys,
};

export function runWithQueueCorrelation<T>(message: PgmqMessage, callback: () => T): T {
  const extracted = extractCorrelation(message.headers, headerGetter, {
    kind: 'system',
    serviceName: SERVICE_OG,
  });
  return context.with(extracted.otelContext, () =>
    extracted.requestContext
      ? runWithRequestContext(extracted.requestContext, callback)
      : callback()
  );
}

function baseContext(message: PgmqMessage, queue: string) {
  return {
    queue,
    message_id: String(message.transportId),
    command_id: message.envelope?.message_id ?? `${queue}:${String(message.transportId)}`,
    retry_count: Math.max(0, message.readCount - 1),
  };
}

export function emitQueueDeliverySucceeded(
  message: PgmqMessage,
  queue: string,
  startedAt: number
): void {
  emitSystemRecord(() =>
    buildQueueDeliverySucceededRecord(systemMetadata(), {
      ...baseContext(message, queue),
      duration_ms: Math.max(0, Date.now() - startedAt),
    })
  );
}

export function emitQueueDeliveryFailed(
  message: PgmqMessage,
  queue: string,
  startedAt: number,
  reason: QueueFailureReason
): void {
  emitSystemRecord(() =>
    buildQueueDeliveryFailedRecord(
      systemMetadata(),
      {
        ...baseContext(message, queue),
        duration_ms: Math.max(0, Date.now() - startedAt),
      },
      reason
    )
  );
}

export function emitQueueHandoffAccepted(
  message: PgmqMessage,
  queue: string,
  target: 'retry' | 'dlq'
): void {
  const handoff = baseContext(message, queue);
  emitSystemRecord(() =>
    target === 'retry'
      ? buildQueueRetryAcceptedRecord(systemMetadata(), handoff)
      : buildQueueDLQAcceptedRecord(systemMetadata(), handoff)
  );
}
