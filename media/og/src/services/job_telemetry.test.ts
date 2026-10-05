import { beforeEach, describe, expect, it, vi } from 'vitest';
import { PermanentGenerationError } from './errors.js';

const mocks = vi.hoisted(() => {
  const logger = { info: vi.fn(), warn: vi.fn(), error: vi.fn(), system: vi.fn() };
  return { ...logger, logger };
});

vi.mock('../logger.js', () => ({ logger: mocks.logger }));

import {
  emitGenerationFailed,
  emitGenerationSucceeded,
  generationFailureReason,
} from './job_telemetry.js';

const GENERATION_ID = '00000000-0000-0000-0000-000000000001';

describe('OG job telemetry', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.logger.system.mockImplementation((level: 'info' | 'warn' | 'error', record: unknown) =>
      mocks[level](record, 'System event')
    );
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2026-08-10T00:00:01Z'));
  });

  it.each([
    ['invalid_target', 'invalid_claim'],
    ['unsupported_entity', 'invalid_claim'],
    ['invalid_config', 'invalid_claim'],
    ['missing_title', 'invalid_claim'],
    ['invalid_image_content_type', 'source_rejected'],
    ['empty_source_image', 'source_rejected'],
    ['source_image_too_large', 'source_rejected'],
    ['processed_image_too_large', 'source_rejected'],
    ['image_http_404', 'source_rejected'],
    ['integrity_failure', 'integrity_failed'],
    ['completion_rejected', 'completion_rejected'],
    ['generation_failed', 'processing_failed'],
  ])('maps %s to the bounded %s reason', (errorCode, expected) => {
    expect(generationFailureReason(errorCode)).toBe(expected);
  });

  it('emits only bounded terminal success and failure fields', () => {
    emitGenerationSucceeded(GENERATION_ID, Date.now() - 250);
    emitGenerationFailed(
      GENERATION_ID,
      Date.now() + 1,
      new PermanentGenerationError('private detail', 'integrity_failure')
    );

    expect(mocks.info).toHaveBeenCalledWith(
      {
        occurred_at: '2026-08-10T00:00:01.000Z',
        event: 'job.succeeded',
        outcome: 'succeeded',
        job_kind: 'og_generation',
        job_id: GENERATION_ID,
        duration_ms: 250,
      },
      'System event'
    );
    expect(mocks.error).toHaveBeenCalledWith(
      {
        occurred_at: '2026-08-10T00:00:01.000Z',
        event: 'job.failed',
        outcome: 'failed',
        job_kind: 'og_generation',
        job_id: GENERATION_ID,
        duration_ms: 0,
        reason: 'integrity_failed',
      },
      'System event'
    );
  });
});
