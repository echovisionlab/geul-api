import type { AssetWriteResult } from '@echovisionlab/geul-event';
import { logger } from '../logger.js';
import {
  completeOgGeneration,
  failOgGeneration,
} from './backend.js';
import {
  PermanentGenerationError,
  RequeueMessageError,
  TransientGenerationError,
  getErrorMessage,
  isTransientInfrastructureError,
} from './errors.js';

const COMPLETION_RETRY_DELAYS_MS = [250, 1_000] as const;

export interface CompletionDependencies {
  complete: typeof completeOgGeneration;
  fail: typeof failOgGeneration;
  sleep: (milliseconds: number) => Promise<void>;
}

function completionRejected(error: unknown): PermanentGenerationError {
  return new PermanentGenerationError(
    `Backend rejected OG completion: ${getErrorMessage(error)}`,
    'completion_rejected',
    { cause: error }
  );
}

export async function completeWithRetry(
  dependencies: CompletionDependencies,
  generationId: string,
  leaseToken: string,
  result: AssetWriteResult
): Promise<void> {
  for (const delay of COMPLETION_RETRY_DELAYS_MS) {
    try {
      await dependencies.complete(generationId, leaseToken, result);
      return;
    } catch (error) {
      if (!isTransientInfrastructureError(error)) {
        throw completionRejected(error);
      }
      await dependencies.sleep(delay);
    }
  }
  try {
    await dependencies.complete(generationId, leaseToken, result);
    return;
  } catch (error) {
    if (!isTransientInfrastructureError(error)) {
      throw completionRejected(error);
    }
    logger.error(
      {
        reason: 'retry_exhausted',
        error,
        generationId,
      },
      'OG completion retries exhausted; lease will expire'
    );
    throw new RequeueMessageError(
      `OG completion result is uncertain for ${generationId}`,
      { cause: error }
    );
  }
}

export async function reportFailure(
  dependencies: CompletionDependencies,
  generationId: string,
  leaseToken: string,
  failure: TransientGenerationError | PermanentGenerationError
): Promise<void> {
  try {
    await dependencies.fail(generationId, leaseToken, failure.errorCode, failure.message);
  } catch (error) {
    if (!isTransientInfrastructureError(error)) {
      throw error;
    }
    logger.error(
      {
        error,
        generationId,
        errorType: failure.errorCode,
      },
      'Failed to record OG generation result; queue delivery will be retried'
    );
    throw new RequeueMessageError(
      `Could not record OG generation result for ${generationId}`,
      { cause: error }
    );
  }
}
