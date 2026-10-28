import { register } from 'node:module';
import { diag, DiagConsoleLogger, DiagLogLevel } from '@opentelemetry/api';
import { getNodeAutoInstrumentations } from '@opentelemetry/auto-instrumentations-node';
import { OTLPMetricExporter } from '@opentelemetry/exporter-metrics-otlp-http';
import { OTLPTraceExporter } from '@opentelemetry/exporter-trace-otlp-http';
import { PeriodicExportingMetricReader } from '@opentelemetry/sdk-metrics';
import { NodeSDK } from '@opentelemetry/sdk-node';
import { SERVICE_OG } from '@echovisionlab/geul-telemetry';
import { emitTelemetryPipelineDegraded } from './system_logging.js';

// Leaves the rest of the pod's termination grace period for the runtime exit.
const TELEMETRY_SHUTDOWN_TIMEOUT_MS = 5_000;

let activeSdk: NodeSDK | undefined;

const hasOtelEndpoint = Boolean(
  process.env.OTEL_EXPORTER_OTLP_ENDPOINT ||
  process.env.OTEL_EXPORTER_OTLP_LOGS_ENDPOINT ||
  process.env.OTEL_EXPORTER_OTLP_TRACES_ENDPOINT ||
  process.env.OTEL_EXPORTER_OTLP_METRICS_ENDPOINT
);

function startTelemetry(): void {
  if (!hasOtelEndpoint) {
    return;
  }
  if (process.env.OTEL_DEBUG === 'true') {
    diag.setLogger(new DiagConsoleLogger(), DiagLogLevel.INFO);
  }

  // NodeSDK patches CommonJS require only. ESM imports of http, pg and the
  // other instrumented modules are patched only when they load through this
  // loader hook, so index.ts imports the runtime after this module runs.
  register('@opentelemetry/instrumentation/hook.mjs', import.meta.url);

  const sdk = new NodeSDK({
    serviceName: SERVICE_OG,
    traceExporter: new OTLPTraceExporter(),
    metricReaders: [
      new PeriodicExportingMetricReader({
        exporter: new OTLPMetricExporter(),
      }),
    ],
    instrumentations: [
      getNodeAutoInstrumentations({
        '@opentelemetry/instrumentation-fs': { enabled: false },
        // The queue polls PostgreSQL continuously; keep only queries that run
        // inside a traced delivery or request.
        '@opentelemetry/instrumentation-pg': { requireParentSpan: true },
        // logger.ts already emits every pino record as an OpenTelemetry log.
        '@opentelemetry/instrumentation-pino': { enabled: false },
      }),
    ],
  });

  Promise.resolve(sdk.start()).catch((error: unknown) => {
    emitTelemetryPipelineDegraded(error);
  });
  activeSdk = sdk;
}

/**
 * Flushes and stops the SDK. The runtime awaits this after its last log record
 * and before it exits, so the batched records and spans are exported.
 */
export async function shutdownTelemetry(): Promise<void> {
  const sdk = activeSdk;
  activeSdk = undefined;
  if (!sdk) {
    return;
  }
  let timeout: NodeJS.Timeout | undefined;
  const deadline = new Promise<never>((_resolve, reject) => {
    timeout = setTimeout(
      () => reject(new Error('OpenTelemetry shutdown timed out')),
      TELEMETRY_SHUTDOWN_TIMEOUT_MS
    );
  });
  try {
    await Promise.race([sdk.shutdown(), deadline]);
  } catch (error) {
    emitTelemetryPipelineDegraded(error);
  } finally {
    clearTimeout(timeout);
  }
}

startTelemetry();
