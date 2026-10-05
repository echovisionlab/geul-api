import { Code, ConnectError } from '@connectrpc/connect';

export class PoisonMessageError extends Error {
  override readonly name: string = 'PoisonMessageError';
}

export class RequeueMessageError extends Error {
  override readonly name: string = 'RequeueMessageError';
}

export class RecoverGenerationLeaseError extends Error {
  override readonly name: string = 'RecoverGenerationLeaseError';

  constructor(
    message: string,
    readonly visibilitySeconds: number
  ) {
    super(message);
  }
}

export class TransientGenerationError extends Error {
  override readonly name: string = 'TransientGenerationError';

  constructor(
    message: string,
    readonly errorCode: string,
    options?: ErrorOptions
  ) {
    super(message, options);
  }
}

export class PermanentGenerationError extends Error {
  override readonly name: string = 'PermanentGenerationError';

  constructor(
    message: string,
    readonly errorCode: string,
    options?: ErrorOptions
  ) {
    super(message, options);
  }
}

export class IntegrityError extends PermanentGenerationError {
  override readonly name: string = 'IntegrityError';

  constructor(message: string, options?: ErrorOptions) {
    super(message, 'integrity_failure', options);
  }
}

const TRANSIENT_CONNECT_CODES = new Set<Code>([
  Code.Canceled,
  Code.Unknown,
  Code.DeadlineExceeded,
  Code.ResourceExhausted,
  Code.Aborted,
  Code.Internal,
  Code.Unavailable,
  Code.DataLoss,
]);

const TRANSIENT_NETWORK_CODES = new Set([
  'ECONNABORTED',
  'ECONNREFUSED',
  'ECONNRESET',
  'EHOSTUNREACH',
  'ENETDOWN',
  'ENETUNREACH',
  'ENOTFOUND',
  'EPIPE',
  'ETIMEDOUT',
  'RequestTimeout',
  'RequestTimeoutException',
  'ConditionalRequestConflict',
]);

interface ErrorShape {
  code?: unknown;
  name?: unknown;
  status?: unknown;
  $metadata?: { httpStatusCode?: unknown };
}

export function getErrorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

function asErrorShape(error: unknown): ErrorShape | undefined {
  return typeof error === 'object' && error !== null ? error as ErrorShape : undefined;
}

function hasTransientNetworkCode(error: ErrorShape): boolean {
  return (typeof error.code === 'string' && TRANSIENT_NETWORK_CODES.has(error.code)) ||
    (typeof error.name === 'string' && TRANSIENT_NETWORK_CODES.has(error.name));
}

function infrastructureStatus(error: ErrorShape): number | undefined {
  if (typeof error.$metadata?.httpStatusCode === 'number') {
    return error.$metadata.httpStatusCode;
  }
  return typeof error.status === 'number' ? error.status : undefined;
}

function isTransientStatus(status: number | undefined): boolean {
  return status === 408 || status === 409 || status === 429 ||
    (status !== undefined && status >= 500 && status <= 599);
}

export function isTransientInfrastructureError(error: unknown): boolean {
  if (error instanceof TransientGenerationError) {
    return true;
  }
  if (error instanceof PermanentGenerationError) {
    return false;
  }
  if (error instanceof ConnectError) {
    return TRANSIENT_CONNECT_CODES.has(error.code);
  }

  const shaped = asErrorShape(error);
  if (!shaped) {
    return false;
  }
  if (hasTransientNetworkCode(shaped)) {
    return true;
  }
  if (shaped.name === 'AbortError' || shaped.name === 'TimeoutError') {
    return true;
  }
  return isTransientStatus(infrastructureStatus(shaped));
}

export function asGenerationFailure(error: unknown):
  | TransientGenerationError
  | PermanentGenerationError {
  if (error instanceof TransientGenerationError || error instanceof PermanentGenerationError) {
    return error;
  }
  const message = getErrorMessage(error);
  if (isTransientInfrastructureError(error)) {
    return new TransientGenerationError(message, 'transient_infrastructure', { cause: error });
  }
  return new PermanentGenerationError(message, 'generation_failed', { cause: error });
}
