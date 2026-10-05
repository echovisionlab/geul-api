import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({
  diag: {
    setLogger: vi.fn(),
  },
  getNodeAutoInstrumentations: vi.fn(() => ['instrumentation']),
  metricExporter: vi.fn(function OTLPMetricExporter() {
    return { type: 'metric-exporter' };
  }),
  metricReader: vi.fn(function PeriodicExportingMetricReader(options: unknown) {
    return { options };
  }),
  nodeSdkStart: vi.fn(async () => undefined),
  nodeSdkShutdown: vi.fn(async () => undefined),
  nodeSdk: vi.fn(function NodeSDK(options: unknown) {
    return {
      options,
      start: mocks.nodeSdkStart,
      shutdown: mocks.nodeSdkShutdown,
    };
  }),
  traceExporter: vi.fn(function OTLPTraceExporter() {
    return { type: 'trace-exporter' };
  }),
  emitTelemetryPipelineDegraded: vi.fn(),
}));

vi.mock('@opentelemetry/api', () => ({
  diag: mocks.diag,
  DiagConsoleLogger: vi.fn(function DiagConsoleLogger() {
    return { type: 'diag-console' };
  }),
  DiagLogLevel: {
    INFO: 1,
  },
}));

vi.mock('@opentelemetry/auto-instrumentations-node', () => ({
  getNodeAutoInstrumentations: mocks.getNodeAutoInstrumentations,
}));

vi.mock('@opentelemetry/exporter-metrics-otlp-http', () => ({
  OTLPMetricExporter: mocks.metricExporter,
}));

vi.mock('@opentelemetry/exporter-trace-otlp-http', () => ({
  OTLPTraceExporter: mocks.traceExporter,
}));

vi.mock('@opentelemetry/sdk-metrics', () => ({
  PeriodicExportingMetricReader: mocks.metricReader,
}));

vi.mock('@opentelemetry/sdk-node', () => ({
  NodeSDK: mocks.nodeSdk,
}));

vi.mock('./system_logging.js', () => ({
  emitTelemetryPipelineDegraded: mocks.emitTelemetryPipelineDegraded,
}));

async function importTelemetry() {
  vi.resetModules();
  return import('./telemetry.js');
}

describe('telemetry', () => {
  const originalEnv = process.env;
  const originalOnce = process.once;
  const originalStderrWrite = process.stderr.write;

  beforeEach(() => {
    process.env = { ...originalEnv };
    vi.clearAllMocks();
  });

  afterEach(() => {
    process.env = originalEnv;
    process.once = originalOnce;
    process.stderr.write = originalStderrWrite;
  });

  it('does not initialize OpenTelemetry when no OTLP endpoint is configured', async () => {
    delete process.env.OTEL_EXPORTER_OTLP_ENDPOINT;
    delete process.env.OTEL_EXPORTER_OTLP_LOGS_ENDPOINT;
    delete process.env.OTEL_EXPORTER_OTLP_TRACES_ENDPOINT;
    delete process.env.OTEL_EXPORTER_OTLP_METRICS_ENDPOINT;

    await importTelemetry();

    expect(mocks.nodeSdk).not.toHaveBeenCalled();
  });

  it('starts the SDK, enables debug logging, and registers signal shutdown handlers', async () => {
    process.env.OTEL_EXPORTER_OTLP_ENDPOINT = 'http://otel:4318';
    process.env.OTEL_DEBUG = 'true';
    process.env.OTEL_SERVICE_NAME = 'custom-og';
    process.env.OTEL_RESOURCE_ATTRIBUTES = 'service.name=also-custom,deployment.environment=test';
    const signalHandlers = new Map<string, (...args: unknown[]) => void>();
    process.once = vi.fn((event: string | symbol, listener: (...args: unknown[]) => void) => {
      signalHandlers.set(String(event), listener);
      return process;
    }) as typeof process.once;

    await importTelemetry();
    await Promise.resolve();

    expect(mocks.diag.setLogger).toHaveBeenCalledWith(expect.anything(), 1);
    expect(mocks.nodeSdk).toHaveBeenCalledWith(
      expect.objectContaining({
        serviceName: 'geul-og',
        traceExporter: { type: 'trace-exporter' },
        metricReader: expect.objectContaining({
          options: {
            exporter: { type: 'metric-exporter' },
          },
        }),
        instrumentations: [['instrumentation']],
      })
    );
    expect(mocks.getNodeAutoInstrumentations).toHaveBeenCalledWith({
      '@opentelemetry/instrumentation-fs': { enabled: false },
    });
    expect(mocks.nodeSdkStart).toHaveBeenCalledTimes(1);

    signalHandlers.get('SIGTERM')?.();
    await Promise.resolve();

    expect(mocks.nodeSdkShutdown).toHaveBeenCalledTimes(1);
  });

  it('records startup and shutdown failures through the structured logger', async () => {
    process.env.OTEL_EXPORTER_OTLP_TRACES_ENDPOINT = 'http://otel:4318/v1/traces';
    mocks.nodeSdkStart.mockRejectedValueOnce(new Error('start failed'));
    mocks.nodeSdkShutdown.mockRejectedValueOnce(new Error('stop failed'));
    let shutdown: (() => void) | undefined;
    process.once = vi.fn((event: string | symbol, listener: (...args: unknown[]) => void) => {
      if (event === 'SIGINT') {
        shutdown = listener as () => void;
      }
      return process;
    }) as typeof process.once;

    await importTelemetry();
    await Promise.resolve();
    shutdown?.();
    await Promise.resolve();

    expect(mocks.emitTelemetryPipelineDegraded).toHaveBeenNthCalledWith(
      1,
      expect.objectContaining({ message: 'start failed' })
    );
    expect(mocks.emitTelemetryPipelineDegraded).toHaveBeenNthCalledWith(
      2,
      expect.objectContaining({ message: 'stop failed' })
    );
  });

  it('supports the metrics-only endpoint and non-Error SDK rejections', async () => {
    delete process.env.OTEL_EXPORTER_OTLP_ENDPOINT;
    delete process.env.OTEL_EXPORTER_OTLP_TRACES_ENDPOINT;
    process.env.OTEL_EXPORTER_OTLP_METRICS_ENDPOINT = 'http://otel:4318/v1/metrics';
    mocks.nodeSdkStart.mockRejectedValueOnce('start string');
    mocks.nodeSdkShutdown.mockRejectedValueOnce('stop string');
    let shutdown: (() => void) | undefined;
    process.once = vi.fn((event: string | symbol, listener: (...args: unknown[]) => void) => {
      if (event === 'SIGTERM') {
        shutdown = listener as () => void;
      }
      return process;
    }) as typeof process.once;

    await importTelemetry();
    await Promise.resolve();
    shutdown?.();
    await Promise.resolve();

    expect(mocks.emitTelemetryPipelineDegraded).toHaveBeenNthCalledWith(1, 'start string');
    expect(mocks.emitTelemetryPipelineDegraded).toHaveBeenNthCalledWith(2, 'stop string');
  });

  it('supports the logs-only endpoint', async () => {
    delete process.env.OTEL_EXPORTER_OTLP_ENDPOINT;
    delete process.env.OTEL_EXPORTER_OTLP_TRACES_ENDPOINT;
    delete process.env.OTEL_EXPORTER_OTLP_METRICS_ENDPOINT;
    process.env.OTEL_EXPORTER_OTLP_LOGS_ENDPOINT = 'http://otel:4318/v1/logs';
    process.once = vi.fn(() => process) as typeof process.once;

    await importTelemetry();

    expect(mocks.nodeSdkStart).toHaveBeenCalledTimes(1);
  });
});
