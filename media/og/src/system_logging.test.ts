import { beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => {
  const logger = { info: vi.fn(), warn: vi.fn(), error: vi.fn() };
  return {
    ...logger,
    logger: {
      ...logger,
      system: vi.fn((level: 'info' | 'warn' | 'error', record: unknown) =>
        logger[level](record, 'System event')
      ),
    },
  };
});

vi.mock('./logger.js', () => ({ logger: mocks.logger }));

import {
  emitPostgresDegraded,
  emitServiceFailed,
  emitServiceReady,
  emitServiceStopping,
  emitTelemetryPipelineDegraded,
} from './system_logging.js';

describe('system logging', () => {
  beforeEach(() => {
    mocks.info.mockReset();
    mocks.warn.mockReset();
    mocks.error.mockReset();
    mocks.logger.system = vi.fn((level: 'info' | 'warn' | 'error', record: unknown) =>
      mocks[level](record, 'System event')
    );
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2026-08-10T00:00:00Z'));
  });

  it('emits typed service lifecycle records', () => {
    emitServiceReady();
    emitServiceStopping();
    emitServiceFailed(new TypeError('private failure detail'));

    expect(mocks.info).toHaveBeenNthCalledWith(
      1,
      {
        occurred_at: '2026-08-10T00:00:00.000Z',
        event: 'service.ready',
        outcome: 'ready',
        component: 'runtime',
      },
      'System event'
    );
    expect(mocks.info).toHaveBeenNthCalledWith(
      2,
      {
        occurred_at: '2026-08-10T00:00:00.000Z',
        event: 'service.stopping',
        outcome: 'stopping',
        component: 'runtime',
      },
      'System event'
    );
    expect(mocks.error).toHaveBeenCalledWith(
      {
        occurred_at: '2026-08-10T00:00:00.000Z',
        event: 'service.failed',
        outcome: 'failed',
        component: 'runtime',
        error_code: 'type_error',
      },
      'System event'
    );
  });

  it('emits a typed telemetry pipeline degradation without raw error detail', () => {
    emitTelemetryPipelineDegraded('private provider response');

    expect(mocks.warn).toHaveBeenCalledWith(
      {
        occurred_at: '2026-08-10T00:00:00.000Z',
        event: 'telemetry.pipeline.degraded',
        outcome: 'degraded',
        component: 'otel_sdk',
        error_code: 'reported_error',
      },
      'System event'
    );
  });

  it('emits bounded PostgreSQL dependency degradation records', () => {
    emitPostgresDegraded('read', new TypeError('private detail'));

    expect(mocks.warn).toHaveBeenCalledWith(
      {
        occurred_at: '2026-08-10T00:00:00.000Z',
        event: 'dependency.degraded',
        outcome: 'degraded',
        dependency: 'postgresql',
        operation: 'read',
        error_code: 'type_error',
      },
      'System event'
    );
  });

  it('never lets record construction or logger failures escape', () => {
    const infoFailure = new Error('info logger unavailable');
    mocks.info.mockImplementationOnce(() => {
      throw infoFailure;
    });

    expect(() => emitServiceReady()).not.toThrow();
    expect(mocks.error).toHaveBeenCalledWith(
      { error: infoFailure },
      'System telemetry emission failed'
    );

    mocks.error.mockImplementation(() => {
      throw new Error('error logger unavailable');
    });
    expect(() => emitServiceFailed(new Error('service failed'))).not.toThrow();
  });
});
