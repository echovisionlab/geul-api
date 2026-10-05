import {
  JOB_KIND_OG_GENERATION,
  buildJobFailedRecord,
  buildJobSucceededRecord,
  type JobFailureReason,
} from '@echovisionlab/geul-telemetry';
import { emitSystemRecord, systemMetadata } from '../system_logging.js';
import type { PermanentGenerationError, TransientGenerationError } from './errors.js';

const INVALID_CLAIM_CODES = new Set([
  'invalid_target',
  'unsupported_entity',
  'invalid_config',
  'missing_title',
]);
const SOURCE_REJECTION_CODES = new Set([
  'invalid_image_content_type',
  'empty_source_image',
  'source_image_too_large',
  'processed_image_too_large',
]);

function durationMs(startedAt: number): number {
  return Math.max(0, Date.now() - startedAt);
}

export function emitGenerationSucceeded(generationId: string, startedAt: number): void {
  emitSystemRecord(() =>
    buildJobSucceededRecord(
      systemMetadata(),
      { job_kind: JOB_KIND_OG_GENERATION, job_id: generationId },
      durationMs(startedAt)
    )
  );
}

export function generationFailureReason(errorCode: string): JobFailureReason {
  if (INVALID_CLAIM_CODES.has(errorCode)) {
    return 'invalid_claim';
  }
  if (SOURCE_REJECTION_CODES.has(errorCode) || /^image_http_4\d\d$/.test(errorCode)) {
    return 'source_rejected';
  }
  if (errorCode === 'integrity_failure') {
    return 'integrity_failed';
  }
  if (errorCode === 'completion_rejected') {
    return 'completion_rejected';
  }
  return 'processing_failed';
}

export function emitGenerationFailed(
  generationId: string,
  startedAt: number,
  failure: PermanentGenerationError | TransientGenerationError
): void {
  emitSystemRecord(() =>
    buildJobFailedRecord(
      systemMetadata(),
      { job_kind: JOB_KIND_OG_GENERATION, job_id: generationId },
      durationMs(startedAt),
      { reason: generationFailureReason(failure.errorCode) }
    )
  );
}
