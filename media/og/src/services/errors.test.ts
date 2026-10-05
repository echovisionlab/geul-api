import { Code, ConnectError } from '@connectrpc/connect';
import { describe, expect, it } from 'vitest';
import {
  IntegrityError,
  PermanentGenerationError,
  PoisonMessageError,
  RecoverGenerationLeaseError,
  RequeueMessageError,
  TransientGenerationError,
  asGenerationFailure,
  getErrorMessage,
  isTransientInfrastructureError,
} from './errors.js';

describe('OG failure classification', () => {
  it('preserves explicit lifecycle errors and names queue-control errors', () => {
    const transient = new TransientGenerationError('later', 'timeout');
    const permanent = new PermanentGenerationError('stop', 'invalid');
    expect(isTransientInfrastructureError(transient)).toBe(true);
    expect(isTransientInfrastructureError(permanent)).toBe(false);
    expect(asGenerationFailure(transient)).toBe(transient);
    expect(asGenerationFailure(permanent)).toBe(permanent);
    expect(new IntegrityError('mismatch')).toMatchObject({
      name: 'IntegrityError',
      errorCode: 'integrity_failure',
    });
    expect(new PoisonMessageError('poison').name).toBe('PoisonMessageError');
    expect(new RecoverGenerationLeaseError('lease', 60)).toMatchObject({
      name: 'RecoverGenerationLeaseError',
      visibilitySeconds: 60,
    });
    expect(new RequeueMessageError('retry').name).toBe('RequeueMessageError');
  });

  it.each([
    Code.Canceled,
    Code.Unknown,
    Code.DeadlineExceeded,
    Code.ResourceExhausted,
    Code.Aborted,
    Code.Internal,
    Code.Unavailable,
    Code.DataLoss,
  ])('classifies Connect code %s as transient', (code) => {
    expect(isTransientInfrastructureError(new ConnectError('rpc', code))).toBe(true);
  });

  it('classifies deterministic Connect errors as permanent', () => {
    expect(isTransientInfrastructureError(new ConnectError('bad', Code.InvalidArgument))).toBe(false);
  });

  it.each([
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
  ])('classifies network code %s as transient', (code) => {
    expect(isTransientInfrastructureError({ code })).toBe(true);
  });

  it.each([
    { name: 'AbortError' },
    { name: 'TimeoutError' },
    { status: 429 },
    { status: 408 },
    { status: 409 },
    { status: 500 },
    { status: 599 },
    { $metadata: { httpStatusCode: 503 } },
    { name: 'RequestTimeout' },
    { name: 'ConditionalRequestConflict' },
  ])('classifies transient infrastructure shape %#', (error) => {
    expect(isTransientInfrastructureError(error)).toBe(true);
  });

  it.each([
    null,
    'failure',
    { code: 'EACCES' },
    { status: 400 },
    { status: 600 },
    { $metadata: { httpStatusCode: '503' } },
  ])('rejects non-transient shape %#', (error) => {
    expect(isTransientInfrastructureError(error)).toBe(false);
  });

  it('wraps inferred transient and permanent failures with stable error codes', () => {
    const timeout = Object.assign(new Error('socket'), { code: 'ETIMEDOUT' });
    expect(asGenerationFailure(timeout)).toMatchObject({
      name: 'TransientGenerationError',
      errorCode: 'transient_infrastructure',
      message: 'socket',
    });
    expect(asGenerationFailure(42)).toMatchObject({
      name: 'PermanentGenerationError',
      errorCode: 'generation_failed',
      message: '42',
    });
    expect(getErrorMessage(new Error('message'))).toBe('message');
    expect(getErrorMessage(false)).toBe('false');
  });
});
