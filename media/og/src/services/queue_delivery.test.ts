import { create, toBinary } from '@bufbuild/protobuf';
import {
  createPgmqEnvelope,
  OgGenerationJobSchema,
  type PgmqClient,
  type PgmqExecutor,
  type PgmqMessage,
} from '@echovisionlab/geul-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  handleDelivery,
  OG_VISIBILITY_TIMEOUT_SECONDS,
} from './queue_delivery.js';
import { RecoverGenerationLeaseError, RequeueMessageError } from './errors.js';

const mocks = vi.hoisted(() => ({
  emitPostgresDegraded: vi.fn(),
  error: vi.fn(),
  system: vi.fn(),
  warn: vi.fn(),
}));

vi.mock('../logger.js', () => ({
  logger: { error: mocks.error, system: mocks.system, warn: mocks.warn },
}));
vi.mock('../system_logging.js', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../system_logging.js')>()),
  emitPostgresDegraded: mocks.emitPostgresDegraded,
}));

const generationId = '11111111-1111-4111-8111-111111111111';

function message(
  payload: Uint8Array,
  readCount = 1,
  headers: Record<string, string> = {}
): PgmqMessage {
  return {
    transportId: 42n,
    readCount,
    enqueuedAt: new Date('2026-08-14T00:00:00Z'),
    visibleAt: new Date('2026-08-14T00:03:00Z'),
    envelope: createPgmqEnvelope({
      messageId: 'og-generation:11111111-1111-4111-8111-111111111111',
      messageType: 'api.manage.v1.OgGenerationJob',
      payload,
      createdAt: new Date('2026-08-14T00:00:00Z'),
    }),
    headers,
  };
}

function client() {
  return {
    complete: vi.fn().mockResolvedValue(undefined),
    retry: vi.fn().mockResolvedValue(undefined),
    deadLetter: vi.fn().mockResolvedValue(undefined),
  } as unknown as PgmqClient;
}

const executor = {} as PgmqExecutor;

describe('OG PGMQ delivery', () => {
  afterEach(() => {
    vi.useRealTimers();
    vi.clearAllMocks();
  });

  it('deletes only after handler success', async () => {
    const pgmq = client();
    const handler = vi.fn().mockResolvedValue(undefined);
    const delivery = message(
      toBinary(OgGenerationJobSchema, create(OgGenerationJobSchema, { generationId })),
      1,
      { 'x-request-id': '11111111-1111-4111-8111-111111111111' }
    );

    await handleDelivery(executor, pgmq, delivery, handler);

    expect(handler).toHaveBeenCalledWith(generationId);
    expect(pgmq.complete).toHaveBeenCalledWith(
      executor,
      'og.generate',
      42n
    );
    expect(pgmq.retry).not.toHaveBeenCalled();
    expect(pgmq.deadLetter).not.toHaveBeenCalled();
  });

  it('uses bounded set_vt only for same-generation commit uncertainty', async () => {
    const handler = vi.fn().mockRejectedValue(new RequeueMessageError('commit uncertain'));

    const initialRetryClient = client();
    await handleDelivery(
      executor,
      initialRetryClient,
      message(toBinary(OgGenerationJobSchema, create(OgGenerationJobSchema, { generationId })), 1),
      handler
    );
    expect(initialRetryClient.retry).toHaveBeenCalledWith(
      executor,
      'og.generate',
      42n,
      5
    );

    const retryClient = client();
    await handleDelivery(
      executor,
      retryClient,
      message(toBinary(OgGenerationJobSchema, create(OgGenerationJobSchema, { generationId })), 2),
      handler
    );
    expect(retryClient.retry).toHaveBeenCalledWith(
      executor,
      'og.generate',
      42n,
      10
    );

    const lastRetryClient = client();
    await handleDelivery(
      executor,
      lastRetryClient,
      message(toBinary(OgGenerationJobSchema, create(OgGenerationJobSchema, { generationId })), 3),
      handler
    );
    expect(lastRetryClient.retry).toHaveBeenCalledWith(
      executor,
      'og.generate',
      42n,
      20
    );

    const exhaustedClient = client();
    await handleDelivery(
      executor,
      exhaustedClient,
      message(toBinary(OgGenerationJobSchema, create(OgGenerationJobSchema, { generationId })), 4),
      handler
    );
    expect(exhaustedClient.deadLetter).toHaveBeenCalledWith(
      executor,
      'og.generate',
      42n
    );
  });

  it('parks the same message until an active processing lease expires even after many reads', async () => {
    const pgmq = client();
    const handler = vi.fn().mockRejectedValue(
      new RecoverGenerationLeaseError('active lease', 420)
    );

    await handleDelivery(
      executor,
      pgmq,
      message(
        toBinary(OgGenerationJobSchema, create(OgGenerationJobSchema, { generationId })),
        99
      ),
      handler
    );

    expect(pgmq.retry).toHaveBeenCalledWith(executor, 'og.generate', 42n, 420);
    expect(pgmq.complete).not.toHaveBeenCalled();
    expect(pgmq.deadLetter).not.toHaveBeenCalled();
  });

  it('archives an unexpected handler error immediately without automatic retry', async () => {
    const pgmq = client();
    const handler = vi.fn().mockRejectedValue(new Error('unexpected'));

    await handleDelivery(
      executor,
      pgmq,
      message(toBinary(OgGenerationJobSchema, create(OgGenerationJobSchema, { generationId }))),
      handler
    );

    expect(pgmq.retry).not.toHaveBeenCalled();
    expect(pgmq.deadLetter).toHaveBeenCalledWith(executor, 'og.generate', 42n);
  });

  it('archives a transport contract error and unexpected message type immediately', async () => {
    const contractClient = client();
    const handler = vi.fn();
    const invalidContract = {
      ...message(new Uint8Array()),
      envelope: undefined,
      contractError: 'PGMQ envelope message_id is required',
    };

    await handleDelivery(executor, contractClient, invalidContract, handler);

    expect(contractClient.deadLetter).toHaveBeenCalledWith(
      executor,
      'og.generate',
      42n
    );
    expect(mocks.system).toHaveBeenCalledWith(
      'info',
      expect.objectContaining({
        event: 'queue.dlq.accepted',
        message_id: '42',
        command_id: 'og.generate:42',
      })
    );
    expect(handler).not.toHaveBeenCalled();

    const missingEnvelopeClient = client();
    await handleDelivery(
      executor,
      missingEnvelopeClient,
      { ...message(new Uint8Array()), envelope: undefined },
      handler
    );
    expect(missingEnvelopeClient.deadLetter).toHaveBeenCalled();

    const typeClient = client();
    const wrongType = message(
      toBinary(OgGenerationJobSchema, create(OgGenerationJobSchema, { generationId }))
    );
    wrongType.envelope = { ...wrongType.envelope!, message_type: 'api.manage.v1.OtherJob' };

    await handleDelivery(executor, typeClient, wrongType, handler);

    expect(typeClient.deadLetter).toHaveBeenCalled();
    expect(handler).not.toHaveBeenCalled();
  });

  it('extends visibility while a generation remains in flight', async () => {
    vi.useFakeTimers();
    let finish: (() => void) | undefined;
    const handler = vi.fn(
      () => new Promise<void>((resolve) => {
        finish = resolve;
      })
    );
    const pgmq = client();
    const delivery = handleDelivery(
      executor,
      pgmq,
      message(toBinary(OgGenerationJobSchema, create(OgGenerationJobSchema, { generationId }))),
      handler
    );

    await vi.advanceTimersByTimeAsync(60_000);

    expect(pgmq.retry).toHaveBeenCalledWith(
      executor,
      'og.generate',
      42n,
      OG_VISIBILITY_TIMEOUT_SECONDS
    );
    finish?.();
    await delivery;
    expect(pgmq.complete).toHaveBeenCalledOnce();
  });

  it('reports a failed visibility heartbeat without abandoning the active handler', async () => {
    vi.useFakeTimers();
    let finish: (() => void) | undefined;
    const heartbeatError = new Error('set_vt failed');
    const pgmq = client();
    vi.mocked(pgmq.retry).mockRejectedValueOnce(heartbeatError);
    const delivery = handleDelivery(
      executor,
      pgmq,
      message(toBinary(OgGenerationJobSchema, create(OgGenerationJobSchema, { generationId }))),
      () => new Promise<void>((resolve) => {
        finish = resolve;
      })
    );

    await vi.advanceTimersByTimeAsync(60_000);

    expect(mocks.emitPostgresDegraded).toHaveBeenCalledWith(
      'visibility_heartbeat',
      heartbeatError
    );
    expect(mocks.warn).toHaveBeenCalled();
    finish?.();
    await delivery;
    expect(pgmq.complete).toHaveBeenCalledOnce();
  });

  it('archives malformed protobuf without invoking the handler', async () => {
    const pgmq = client();
    const handler = vi.fn();
    await handleDelivery(executor, pgmq, message(new Uint8Array([255])), handler);
    expect(handler).not.toHaveBeenCalled();
    expect(pgmq.deadLetter).toHaveBeenCalled();
  });

  it('archives a protobuf with an invalid generation ID', async () => {
    const pgmq = client();
    const handler = vi.fn();
    await handleDelivery(
      executor,
      pgmq,
      message(toBinary(OgGenerationJobSchema, create(OgGenerationJobSchema, {}))),
      handler
    );
    expect(handler).not.toHaveBeenCalled();
    expect(pgmq.deadLetter).toHaveBeenCalled();
  });

  it('leaves a successfully handled message visible for retry when completion fails', async () => {
    const pgmq = client();
    vi.mocked(pgmq.complete).mockRejectedValue(new Error('delete failed'));
    const handler = vi.fn().mockResolvedValue(undefined);

    await expect(
      handleDelivery(
        executor,
        pgmq,
        message(toBinary(OgGenerationJobSchema, create(OgGenerationJobSchema, { generationId }))),
        handler
      )
    ).resolves.toBeUndefined();

    expect(handler).toHaveBeenCalledOnce();
    expect(pgmq.retry).not.toHaveBeenCalled();
    expect(pgmq.deadLetter).not.toHaveBeenCalled();
  });
});
