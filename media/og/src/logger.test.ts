import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => {
  const pino = vi.fn((options: {
    hooks: {
      logMethod(args: unknown[], method: (...args: unknown[]) => void, level: number): void;
    };
  }) => {
    const levelLogger = (level: number) => vi.fn((...args: unknown[]) => {
      options.hooks.logMethod(args, vi.fn(), level);
    });
    return {
      options,
      debug: levelLogger(20),
      info: levelLogger(30),
      warn: levelLogger(40),
      error: levelLogger(50),
    };
  });
  const otelEmit = vi.fn();
  return {
    activeContext: {},
    contextActive: vi.fn(),
    getLogger: vi.fn(() => ({ emit: otelEmit })),
    getSpan: vi.fn(),
    isSpanContextValid: vi.fn(),
    otelEmit,
    pino,
    serializeError: vi.fn((error: Error) => ({
      message: error.message,
      name: error.name,
    })),
    isoTime: vi.fn(),
  };
});

vi.mock('@opentelemetry/api', () => ({
  context: {
    active: mocks.contextActive,
  },
  isSpanContextValid: mocks.isSpanContextValid,
  trace: {
    getSpan: mocks.getSpan,
  },
}));

vi.mock('@opentelemetry/api-logs', () => ({
  logs: {
    getLogger: mocks.getLogger,
  },
  SeverityNumber: {
    DEBUG: 5,
    INFO: 9,
    WARN: 13,
    ERROR: 17,
    FATAL: 21,
  },
}));

vi.mock('pino', () => {
  Object.assign(mocks.pino, {
    stdSerializers: {
      err: mocks.serializeError,
    },
    stdTimeFunctions: {
      isoTime: mocks.isoTime,
    },
  });

  return {
    default: mocks.pino,
  };
});

async function importLogger() {
  vi.resetModules();
  return import('./logger.js');
}

describe('logger', () => {
  const originalEnv = process.env;

  beforeEach(() => {
    process.env = { ...originalEnv };
    vi.clearAllMocks();
    mocks.contextActive.mockReturnValue(mocks.activeContext);
    mocks.getLogger.mockReturnValue({ emit: mocks.otelEmit });
    mocks.getSpan.mockReturnValue(undefined);
    mocks.isSpanContextValid.mockReturnValue(false);
  });

  afterEach(() => {
    process.env = originalEnv;
  });

  it('normalizes primitive log payload keys and drops untyped nested data', async () => {
    process.env.LOG_LEVEL = 'debug';

    await importLogger();

    const options = mocks.pino.mock.calls[0][0] as {
      level: string;
      formatters: { level(label: string): Record<string, string> };
      hooks: {
        logMethod(args: unknown[], method: (...args: unknown[]) => void): void;
      };
    };
    const method = vi.fn();
    const error = new Error('boom');
    const args = [
      {
        camelCase: 'value',
        'space key': true,
        nestedValue: {
          innerKey: [error],
        },
      },
      'message',
    ];

    options.hooks.logMethod(args, method);

    expect(options.level).toBe('debug');
    expect(options.formatters.level('info')).toEqual({ level: 'INFO' });
    expect(method).toHaveBeenCalledWith(
      {
        camel_case: 'value',
        space_key: true,
      },
      'message'
    );
  });

  it('drops forbidden fields and replaces raw errors with a stable type', async () => {
    await importLogger();
    const options = mocks.pino.mock.calls[0][0] as {
      hooks: {
        logMethod(args: unknown[], method: (...args: unknown[]) => void, level: number): void;
      };
    };
    const method = vi.fn();
    const args = [
      {
        generationId: 'generation-1',
        objectKey: 'private/key',
        sourcePath: '/private/source',
        tokenPrefix: 'secret',
        err: new Error('private detail'),
      },
      'generation failed',
    ];

    options.hooks.logMethod(args, method, 50);

    expect(method).toHaveBeenCalledWith(
      {
        generation_id: 'generation-1',
        error_type: 'error',
      },
      'generation failed'
    );
  });

  it('uses the default level without inventing catalog fields for diagnostics', async () => {
    delete process.env.LOG_LEVEL;

    await importLogger();

    const options = mocks.pino.mock.calls[0][0] as {
      level: string;
      hooks: {
        logMethod(args: unknown[], method: (...args: unknown[]) => void): void;
      };
    };
    const method = vi.fn();
    options.hooks.logMethod([], method);
    options.hooks.logMethod(['message'], method);

    expect(options.level).toBe('info');
    expect(method).toHaveBeenNthCalledWith(1, {});
    expect(method).toHaveBeenNthCalledWith(2, {}, 'message');
  });

  it('normalizes a directly reported error and exports valid trace correlation', async () => {
    const spanContext = {
      traceId: '11111111111111111111111111111111',
      spanId: '2222222222222222',
      traceFlags: 1,
    };
    mocks.getSpan.mockReturnValue({ spanContext: () => spanContext });
    mocks.isSpanContextValid.mockReturnValue(true);

    await importLogger();

    const options = mocks.pino.mock.calls[0][0] as {
      hooks: {
        logMethod(args: unknown[], method: (...args: unknown[]) => void, level: number): void;
      };
    };
    const method = vi.fn();
    const error = new Error('private detail');
    error.name = '';

    options.hooks.logMethod([error, 'debug diagnostic'], method, 20);

    const attributes = {
      error_type: 'error',
      trace_id: spanContext.traceId,
      span_id: spanContext.spanId,
    };
    expect(method).toHaveBeenCalledWith(attributes, 'debug diagnostic');
    expect(mocks.otelEmit).toHaveBeenCalledWith(
      expect.objectContaining({
        severityNumber: 5,
        severityText: 'DEBUG',
        eventName: undefined,
        body: 'debug diagnostic',
        attributes,
        context: mocks.activeContext,
      })
    );
  });

  it('drops circular records and exports warn and fatal severities without invalid trace data', async () => {
    const invalidSpanContext = {
      traceId: 'invalid',
      spanId: 'invalid',
      traceFlags: 0,
    };
    mocks.getSpan.mockReturnValue({ spanContext: () => invalidSpanContext });
    mocks.isSpanContextValid.mockReturnValue(false);

    await importLogger();

    const options = mocks.pino.mock.calls[0][0] as {
      hooks: {
        logMethod(args: unknown[], method: (...args: unknown[]) => void, level: number): void;
      };
    };
    const method = vi.fn();
    const circularObject: Record<string, unknown> = {};
    circularObject.self = circularObject;
    const circularArray: unknown[] = [];
    circularArray.push(circularArray);
    const warningArgs = [
      {
        circularObject,
        circularArray,
        err: 'reported by provider',
        event: 42,
      },
      'bounded warning',
    ];

    options.hooks.logMethod(warningArgs, method, 40);
    options.hooks.logMethod([{ event: 'runtime.fatal' }, 'fatal failure'], method, 60);

    expect(method).toHaveBeenNthCalledWith(
      1,
      {
        error_type: 'reported_error',
      },
      'bounded warning'
    );
    expect(mocks.otelEmit).toHaveBeenNthCalledWith(
      1,
      expect.objectContaining({
        severityNumber: 13,
        severityText: 'WARN',
        eventName: undefined,
      })
    );
    expect(mocks.otelEmit).toHaveBeenNthCalledWith(
      2,
      expect.objectContaining({
        severityNumber: 21,
        severityText: 'FATAL',
        eventName: undefined,
        attributes: {},
      })
    );
  });

  it('reserves System controls for typed System records', async () => {
    const { logger } = await importLogger();
    const pinoLogger = mocks.pino.mock.results[0]?.value as { info: ReturnType<typeof vi.fn> };

    logger.system('info', {
      occurred_at: '2026-08-11T00:00:00.000Z',
      event: 'service.ready',
      outcome: 'ready',
      component: 'runtime',
    });

    const [record, message] = pinoLogger.info.mock.calls[0] ?? [];
    expect(record).toMatchObject({ event: 'service.ready', outcome: 'ready' });
    expect(Object.getOwnPropertySymbols(record as object)).toHaveLength(1);
    expect(message).toBe('System event');
    expect(mocks.otelEmit).toHaveBeenCalledWith(
      expect.objectContaining({
        eventName: 'service.ready',
        attributes: expect.objectContaining({ event: 'service.ready', outcome: 'ready' }),
      })
    );
  });

  it('drops untyped shared object references', async () => {
    await importLogger();
    const options = mocks.pino.mock.calls[0][0] as {
      hooks: {
        logMethod(args: unknown[], method: (...args: unknown[]) => void): void;
      };
    };
    const method = vi.fn();
    const shared = { assetId: 'asset-1' };

    options.hooks.logMethod([{ first: shared, second: shared }, 'shared'], method);

    expect(method).toHaveBeenCalledWith({}, 'shared');
  });
});
