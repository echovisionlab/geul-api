import { fromBinary } from '@bufbuild/protobuf';
import {
  OgGenerationJobSchema,
  PgmqClient,
  pgmqEnvelopePayload,
  Queues,
  type PgmqExecutor,
  type PgmqMessage,
} from '@echovisionlab/geul-event';
import {
  PoisonMessageError,
  RecoverGenerationLeaseError,
  RequeueMessageError,
} from './errors.js';
import { logger } from '../logger.js';
import { emitPostgresDegraded } from '../system_logging.js';
import {
  emitQueueDeliveryFailed,
  emitQueueDeliverySucceeded,
  emitQueueHandoffAccepted,
  runWithQueueCorrelation,
} from './queue_telemetry.js';

const UUID_PATTERN = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
const MESSAGE_TYPE = 'api.manage.v1.OgGenerationJob';
const MAX_COMMIT_UNCERTAINTY_RETRIES = 3;
const VISIBILITY_HEARTBEAT_INTERVAL_MS = 60_000;

export const OG_VISIBILITY_TIMEOUT_SECONDS = 180;

export type GenerationHandler = (generationId: string) => Promise<unknown>;

function decodeGenerationId(message: PgmqMessage): string {
  if (message.contractError || !message.envelope) {
    throw new PoisonMessageError(
      message.contractError ?? 'PGMQ OG message has no envelope'
    );
  }
  if (message.envelope.message_type !== MESSAGE_TYPE) {
    throw new PoisonMessageError(
      `Unexpected OG message type: ${message.envelope.message_type}`
    );
  }
  let generationId: string;
  try {
    generationId = fromBinary(
      OgGenerationJobSchema,
      pgmqEnvelopePayload(message.envelope)
    ).generationId;
  } catch (error) {
    throw new PoisonMessageError('Malformed OG generation protobuf', { cause: error });
  }
  if (!UUID_PATTERN.test(generationId)) {
    throw new PoisonMessageError(`Invalid OG generation ID: ${generationId || '<empty>'}`);
  }
  return generationId;
}

function startVisibilityHeartbeat(
  executor: PgmqExecutor,
  client: PgmqClient,
  message: PgmqMessage
): () => Promise<void> {
  let activeHeartbeat: Promise<void> = Promise.resolve();
  const timer = setInterval(() => {
    activeHeartbeat = client
      .retry(
        executor,
        Queues.ogGenerate,
        message.transportId,
        OG_VISIBILITY_TIMEOUT_SECONDS
      )
      .catch((error: unknown) => {
        emitPostgresDegraded('visibility_heartbeat', error);
        logger.warn(
          { error, messageId: String(message.transportId) },
          'Could not extend OG PGMQ visibility timeout'
        );
      });
  }, VISIBILITY_HEARTBEAT_INTERVAL_MS);
  return async () => {
    clearInterval(timer);
    await activeHeartbeat;
  };
}

function retryDelaySeconds(readCount: number): number {
  return 5 * 2 ** (readCount - 1);
}

async function processDelivery(
  executor: PgmqExecutor,
  client: PgmqClient,
  message: PgmqMessage,
  handler: GenerationHandler,
  startedAt: number
): Promise<void> {
  let generationId: string;
  try {
    generationId = decodeGenerationId(message);
  } catch {
    await client.deadLetter(executor, Queues.ogGenerate, message.transportId);
    emitQueueHandoffAccepted(message, Queues.ogGenerate, 'dlq');
    return;
  }

  const stopHeartbeat = startVisibilityHeartbeat(executor, client, message);
  try {
    await handler(generationId);
  } catch (error) {
    await stopHeartbeat();
    if (error instanceof RecoverGenerationLeaseError) {
      await client.retry(
        executor,
        Queues.ogGenerate,
        message.transportId,
        error.visibilitySeconds
      );
      emitQueueHandoffAccepted(message, Queues.ogGenerate, 'retry');
      return;
    }
    if (error instanceof RequeueMessageError && message.readCount <= MAX_COMMIT_UNCERTAINTY_RETRIES) {
      await client.retry(
        executor,
        Queues.ogGenerate,
        message.transportId,
        retryDelaySeconds(message.readCount)
      );
      emitQueueHandoffAccepted(message, Queues.ogGenerate, 'retry');
      return;
    }
    await client.deadLetter(executor, Queues.ogGenerate, message.transportId);
    emitQueueHandoffAccepted(message, Queues.ogGenerate, 'dlq');
    return;
  }
  await stopHeartbeat();

  try {
    await client.complete(executor, Queues.ogGenerate, message.transportId);
  } catch {
    emitQueueDeliveryFailed(message, Queues.ogGenerate, startedAt, 'completion_failed');
    return;
  }
  emitQueueDeliverySucceeded(message, Queues.ogGenerate, startedAt);
}

export function handleDelivery(
  executor: PgmqExecutor,
  client: PgmqClient,
  message: PgmqMessage,
  handler: GenerationHandler
): Promise<void> {
  const startedAt = Date.now();
  return runWithQueueCorrelation(message, () =>
    processDelivery(executor, client, message, handler, startedAt)
  );
}
