import {
  buildDependencyDegradedRecord,
  buildServiceFailedRecord,
  buildServiceReadyRecord,
  buildServiceStoppingRecord,
  buildTelemetryPipelineDegradedRecord,
  correlationFromActiveContext,
  stableErrorType,
  systemLogLevel,
  type SystemRecord,
} from '@echovisionlab/geul-telemetry';
import { logger } from './logger.js';

const serviceComponent = 'runtime';
const telemetryPipelineComponent = 'otel_sdk';

export function systemMetadata(): { occurred_at: string } & ReturnType<typeof correlationFromActiveContext> {
  return {
    occurred_at: new Date().toISOString(),
    ...correlationFromActiveContext(),
  };
}

export function emitSystemRecord(buildRecord: () => SystemRecord): void {
  try {
    const record = buildRecord();
    const level = systemLogLevel(record);
    logger.system(level, record);
  } catch (error) {
    try {
      logger.error({ error }, 'System telemetry emission failed');
    } catch {
      // Telemetry is fail-open even when the local logger itself is unavailable.
    }
  }
}

export function emitServiceReady(): void {
  emitSystemRecord(() => buildServiceReadyRecord(systemMetadata(), serviceComponent));
}

export function emitServiceStopping(): void {
  emitSystemRecord(() => buildServiceStoppingRecord(systemMetadata(), serviceComponent));
}

export function emitServiceFailed(error: unknown): void {
  emitSystemRecord(() =>
    buildServiceFailedRecord(systemMetadata(), serviceComponent, {
      error_code: stableErrorType(error),
    })
  );
}

export function emitTelemetryPipelineDegraded(error: unknown): void {
  emitSystemRecord(() =>
    buildTelemetryPipelineDegradedRecord(systemMetadata(), telemetryPipelineComponent, {
      error_code: stableErrorType(error),
    })
  );
}

export function emitPostgresDegraded(
  operation: 'read' | 'delivery' | 'pool' | 'visibility_heartbeat',
  error: unknown
): void {
  emitSystemRecord(() =>
    buildDependencyDegradedRecord(systemMetadata(), 'postgresql', operation, {
      error_code: stableErrorType(error),
    })
  );
}
