import { beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({
  serve: vi.fn(),
  startGenerationConsumer: vi.fn(),
  closeQueue: vi.fn(),
  checkPGMQHealth: vi.fn(),
  processEvent: vi.fn(),
  emitServiceFailed: vi.fn(),
  emitServiceReady: vi.fn(),
  emitServiceStopping: vi.fn(),
}));

vi.mock('@hono/node-server', () => ({ serve: mocks.serve }));
vi.mock('./env.js', () => ({ env: { HOST: '127.0.0.1', PORT: 3010 } }));
vi.mock('./system_logging.js', () => ({
  emitServiceFailed: mocks.emitServiceFailed,
  emitServiceReady: mocks.emitServiceReady,
  emitServiceStopping: mocks.emitServiceStopping,
}));
vi.mock('./services/queue.js', () => ({
  startConsumer: mocks.startGenerationConsumer,
  closePGMQ: mocks.closeQueue,
  checkPGMQHealth: mocks.checkPGMQHealth,
}));
vi.mock('./services/processor.js', () => ({ processOgGenerate: mocks.processEvent }));

import { createApp, startRuntime } from './runtime.js';

describe('OG runtime', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.startGenerationConsumer.mockResolvedValue(undefined);
    mocks.closeQueue.mockResolvedValue(undefined);
    mocks.checkPGMQHealth.mockResolvedValue(true);
  });

  it('serves health with CORS without creating an access record', async () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2026-07-10T00:00:00Z'));
    const app = createApp();

    const response = await app.request('/health', { headers: { origin: 'https://studio.example.com' } });

    expect(response.status).toBe(200);
    expect(response.headers.get('access-control-allow-origin')).toBe('*');
    expect(await response.json()).toEqual({
      status: 'ok',
      postgresql: true,
      timestamp: '2026-07-10T00:00:00.000Z',
    });
    await expect(app.request('/generate', { method: 'POST' })).resolves.toMatchObject({
      status: 404,
    });
    await expect(app.request('/preview', { method: 'POST' })).resolves.toMatchObject({
      status: 404,
    });
    vi.useRealTimers();
  });

  it('returns the bounded error response for an unhandled route failure', async () => {
    const error = new Error('route failed');
    const app = createApp();
    app.get('/failure', () => {
      throw error;
    });

    const response = await app.request('/failure');

    expect(response.status).toBe(500);
    expect(await response.json()).toEqual({ error: 'Internal server error' });
  });

  it('starts consumers before HTTP and shuts down idempotently from either signal', async () => {
    let finishHttpClose: (() => void) | undefined;
    const close = vi.fn((callback?: () => void) => {
      finishHttpClose = callback;
    });
    mocks.serve.mockImplementation((options: unknown, callback: (info: { port: number }) => void) => {
      callback({ port: 3010 });
      return { close };
    });
    const signalHandlers = new Map<string, () => void | Promise<void>>();
    const registerSignal = vi.fn(
      (signal: 'SIGTERM' | 'SIGINT', handler: () => void | Promise<void>) => {
      signalHandlers.set(String(signal), handler);
      return process;
      }
    );
    const exit = vi.fn();

    const runtime = await startRuntime({ registerSignal, exit });

    expect(mocks.startGenerationConsumer).toHaveBeenCalledWith(mocks.processEvent);
    expect(mocks.serve).toHaveBeenCalledWith(
      expect.objectContaining({ hostname: '127.0.0.1', port: 3010, fetch: expect.any(Function) }),
      expect.any(Function)
    );
    expect(signalHandlers.has('SIGTERM')).toBe(true);
    expect(signalHandlers.has('SIGINT')).toBe(true);
    expect(mocks.emitServiceReady).toHaveBeenCalledOnce();

    let finishQueueClose: (() => void) | undefined;
    mocks.closeQueue.mockImplementationOnce(() => new Promise<void>((resolve) => {
      finishQueueClose = resolve;
    }));
    const first = runtime.shutdown();
    const second = signalHandlers.get('SIGINT')?.();
    expect(exit).not.toHaveBeenCalled();
    finishQueueClose?.();
    await Promise.resolve();
    expect(exit).not.toHaveBeenCalled();
    finishHttpClose?.();
    await Promise.all([first, second]);

    expect(close).toHaveBeenCalledTimes(1);
    expect(mocks.closeQueue).toHaveBeenCalledTimes(1);
    expect(mocks.emitServiceStopping).toHaveBeenCalledOnce();
    expect(exit).toHaveBeenCalledWith(0);
  });

  it('uses all default runtime dependencies', async () => {
    const close = vi.fn((callback?: () => void) => callback?.());
    mocks.serve.mockReturnValue({ close });
    const on = vi.spyOn(process, 'on').mockImplementation(() => process);
    const exit = vi.spyOn(process, 'exit').mockImplementation((() => undefined) as never);
    const runtime = await startRuntime();
    await runtime.shutdown();

    expect(mocks.startGenerationConsumer).toHaveBeenCalledWith(mocks.processEvent);
    expect(on).toHaveBeenCalledTimes(2);
    expect(close).toHaveBeenCalledTimes(1);
    expect(exit).toHaveBeenCalledWith(0);

    on.mockRestore();
    exit.mockRestore();
  });

  it('exits nonzero when the queue cannot shut down cleanly', async () => {
    const error = new Error('queue close failed');
    const close = vi.fn((callback?: () => void) => callback?.());
    mocks.serve.mockReturnValue({ close });
    mocks.closeQueue.mockRejectedValueOnce(error);
    const exit = vi.fn();

    const runtime = await startRuntime({ registerSignal: vi.fn(), exit });
    await runtime.shutdown();

    expect(close).toHaveBeenCalledOnce();
    expect(exit).toHaveBeenCalledWith(1);
    expect(mocks.emitServiceFailed).toHaveBeenCalledWith(error);
  });

  it('waits for queue shutdown and exits nonzero when the HTTP server close fails', async () => {
    const error = new Error('http close failed');
    const close = vi.fn((callback?: (error?: Error) => void) => callback?.(error));
    mocks.serve.mockReturnValue({ close });
    const exit = vi.fn();

    const runtime = await startRuntime({ registerSignal: vi.fn(), exit });
    await runtime.shutdown();

    expect(mocks.closeQueue).toHaveBeenCalledOnce();
    expect(exit).toHaveBeenCalledWith(1);
    expect(mocks.emitServiceFailed).toHaveBeenCalledWith(error);
  });
});
