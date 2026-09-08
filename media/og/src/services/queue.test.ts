import { beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({
  pool: {
    query: vi.fn(),
    end: vi.fn(),
    on: vi.fn(),
  },
  poolConfig: undefined as unknown,
  poolErrorHandler: undefined as ((error: Error) => void) | undefined,
  client: {
    read: vi.fn(),
  },
  handleDelivery: vi.fn(),
  emitPostgresDegraded: vi.fn(),
  error: vi.fn(),
  warn: vi.fn(),
}));

vi.mock('pg', () => ({
  Pool: vi.fn(function Pool(config: unknown) {
    mocks.poolConfig = config;
    return mocks.pool;
  }),
}));

vi.mock('@echovisionlab/geul-event', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@echovisionlab/geul-event')>()),
  PgmqClient: vi.fn(function PgmqClient() {
    return mocks.client;
  }),
}));

vi.mock('../env.js', () => ({
  env: {
    DATABASE_DSN: 'postgres://test',
    OG_GENERATE_WORKERS: 1,
    OG_SHUTDOWN_TIMEOUT_MS: 100,
  },
}));

vi.mock('../logger.js', () => ({
  logger: { error: mocks.error, warn: mocks.warn },
}));

vi.mock('../system_logging.js', () => ({
  emitPostgresDegraded: mocks.emitPostgresDegraded,
}));

vi.mock('./queue_delivery.js', () => ({
  handleDelivery: mocks.handleDelivery,
  OG_VISIBILITY_TIMEOUT_SECONDS: 180,
}));

async function loadQueue() {
  vi.resetModules();
  return import('./queue.js');
}

describe('OG PGMQ consumer runtime', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.clearAllMocks();
    mocks.poolConfig = undefined;
    mocks.poolErrorHandler = undefined;
    mocks.pool.query.mockResolvedValue({ rows: [] });
    mocks.pool.end.mockResolvedValue(undefined);
    mocks.pool.on.mockImplementation((_event: string, handler: (error: Error) => void) => {
      mocks.poolErrorHandler = handler;
      return mocks.pool;
    });
  });

  it('polls an empty queue and closes idempotently', async () => {
    mocks.client.read.mockResolvedValue([]);
    const queue = await loadQueue();

    await queue.startConsumer(vi.fn());
    await queue.startConsumer(vi.fn());
    await vi.waitFor(() => expect(mocks.client.read).toHaveBeenCalled());
    await vi.advanceTimersByTimeAsync(250);
    expect(mocks.client.read).toHaveBeenCalledTimes(2);

    const firstClose = queue.closePGMQ();
    const secondClose = queue.closePGMQ();
    expect(secondClose).toBe(firstClose);
    await vi.advanceTimersByTimeAsync(250);
    await firstClose;

    expect(mocks.pool.query).toHaveBeenCalledTimes(2);
    expect(mocks.pool.query).toHaveBeenCalledWith(
      'SELECT total_messages FROM pgmq.metrics($1)',
      ['og.generate']
    );
    expect(mocks.pool.end).toHaveBeenCalledOnce();
    expect(mocks.poolConfig).toMatchObject({
      application_name: 'geul-og',
      connectionTimeoutMillis: 2_000,
      max: 2,
      query_timeout: 2_000,
      statement_timeout: 2_000,
    });
  });

  it('checks live PostgreSQL health and reports idle pool failures', async () => {
    mocks.client.read.mockResolvedValue([]);
    const queue = await loadQueue();
    await queue.startConsumer(vi.fn());

    await expect(queue.checkPGMQHealth()).resolves.toBe(true);
    expect(mocks.pool.query).toHaveBeenLastCalledWith('SELECT 1');

    const healthError = new Error('offline');
    mocks.pool.query.mockRejectedValueOnce(healthError);
    await expect(queue.checkPGMQHealth()).resolves.toBe(false);
    mocks.poolErrorHandler?.(healthError);
    expect(mocks.emitPostgresDegraded).toHaveBeenCalledWith('pool', healthError);
    expect(mocks.error).toHaveBeenCalledWith(
      { error: healthError },
      'PostgreSQL OG pool error'
    );

    await queue.closePGMQ();
    await expect(queue.checkPGMQHealth()).resolves.toBe(false);

    const degradationCount = mocks.emitPostgresDegraded.mock.calls.length;
    mocks.poolErrorHandler?.(new Error('pool closed'));
    expect(mocks.emitPostgresDegraded).toHaveBeenCalledTimes(degradationCount);
  });

  it('fails readiness when the configured login cannot inspect its queue', async () => {
    const error = new Error('permission denied for function metrics');
    mocks.pool.query.mockRejectedValueOnce(error);
    const queue = await loadQueue();

    await expect(queue.startConsumer(vi.fn())).rejects.toBe(error);
    expect(mocks.client.read).not.toHaveBeenCalled();
    await queue.closePGMQ();
  });

  it('reports a PostgreSQL read failure and resumes the bounded poll loop', async () => {
    const error = new Error('read failed');
    mocks.client.read.mockRejectedValue(error);
    const queue = await loadQueue();

    await queue.startConsumer(vi.fn());
    await vi.waitFor(() => expect(mocks.emitPostgresDegraded).toHaveBeenCalledWith('read', error));
    expect(mocks.error).toHaveBeenCalledWith({ error }, 'PGMQ OG consumer failed');

    const closing = queue.closePGMQ();
    await vi.advanceTimersByTimeAsync(250);
    await closing;
  });

  it('bounds shutdown when an in-flight delivery does not finish', async () => {
    const pending = new Promise<void>(() => undefined);
    mocks.client.read.mockResolvedValue([{ transportId: 42n }]);
    mocks.handleDelivery.mockReturnValue(pending);
    const queue = await loadQueue();

    await queue.startConsumer(vi.fn());
    await vi.waitFor(() => expect(mocks.handleDelivery).toHaveBeenCalledOnce());

    const closing = queue.closePGMQ();
    await vi.advanceTimersByTimeAsync(350);
    await closing;

    expect(mocks.warn).toHaveBeenCalledWith(
      {
        inFlightCount: 1,
        pollStopped: true,
        poolClosed: false,
        timeoutMs: 100,
      },
      'PGMQ OG shutdown timed out; unfinished messages return after visibility timeout'
    );
    expect(mocks.pool.end).toHaveBeenCalledOnce();
  });

  it('waits for a successful in-flight delivery before closing', async () => {
    let finishDelivery: (() => void) | undefined;
    const delivery = new Promise<void>((resolve) => {
      finishDelivery = resolve;
    });
    mocks.client.read.mockResolvedValue([{ transportId: 42n }]);
    mocks.handleDelivery.mockReturnValue(delivery);
    const queue = await loadQueue();

    await queue.startConsumer(vi.fn());
    await vi.waitFor(() => expect(mocks.handleDelivery).toHaveBeenCalledOnce());
    const closing = queue.closePGMQ();
    await vi.advanceTimersByTimeAsync(0);
    finishDelivery?.();
    await closing;

    expect(mocks.warn).not.toHaveBeenCalled();
  });

  it('closes cleanly before a pool has been created', async () => {
    const queue = await loadQueue();

    await expect(queue.closePGMQ()).resolves.toBeUndefined();

    expect(mocks.pool.end).not.toHaveBeenCalled();
    expect(mocks.warn).not.toHaveBeenCalled();
  });

  it('suppresses read-failure noise after shutdown begins', async () => {
    let rejectRead: ((error: Error) => void) | undefined;
    mocks.client.read.mockReturnValue(
      new Promise((_, reject) => {
        rejectRead = reject;
      })
    );
    const queue = await loadQueue();

    await queue.startConsumer(vi.fn());
    const closing = queue.closePGMQ();
    rejectRead?.(new Error('closed'));
    await closing;

    expect(mocks.emitPostgresDegraded).not.toHaveBeenCalled();
    expect(mocks.error).not.toHaveBeenCalled();
  });

  it('contains a rejected delivery handoff and continues polling', async () => {
    const error = new Error('archive failed');
    mocks.client.read
      .mockResolvedValueOnce([{ transportId: 42n }])
      .mockResolvedValue([]);
    mocks.handleDelivery.mockRejectedValueOnce(error);
    const queue = await loadQueue();

    await queue.startConsumer(vi.fn());
    await vi.waitFor(() =>
      expect(mocks.emitPostgresDegraded).toHaveBeenCalledWith('delivery', error)
    );

    expect(mocks.error).toHaveBeenCalledWith(
      { error, messageId: '42' },
      'PGMQ OG delivery handoff failed'
    );
    await queue.closePGMQ();
  });

  it('bounds the complete shutdown when the active read never settles', async () => {
    mocks.client.read.mockReturnValue(new Promise(() => undefined));
    const queue = await loadQueue();
    await queue.startConsumer(vi.fn());
    await vi.waitFor(() => expect(mocks.client.read).toHaveBeenCalled());

    const closing = queue.closePGMQ();
    await vi.advanceTimersByTimeAsync(100);
    await closing;

    expect(mocks.warn).toHaveBeenCalledWith(
      {
        inFlightCount: 0,
        pollStopped: false,
        poolClosed: false,
        timeoutMs: 100,
      },
      'PGMQ OG shutdown timed out; unfinished messages return after visibility timeout'
    );
  });
});
