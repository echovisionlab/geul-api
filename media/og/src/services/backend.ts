import { createClient, type Interceptor } from '@connectrpc/connect';
import { createConnectTransport } from '@connectrpc/connect-node';
import {
  InternalOgService,
  type AssetWriteResult,
  type ClaimOgGenerationResponse,
  type CompleteOgGenerationResponse,
  type FailOgGenerationResponse,
} from '@echovisionlab/geul-event';
import { env } from '../env.js';
import { withAbortTimeout } from './timeout.js';

export interface InternalOgClient {
  claimOgGeneration(request: { generationId: string }, options?: RpcCallOptions): Promise<ClaimOgGenerationResponse>;
  completeOgGeneration(request: {
    generationId: string;
    leaseToken: string;
    written: AssetWriteResult;
  }, options?: RpcCallOptions): Promise<CompleteOgGenerationResponse>;
  failOgGeneration(request: {
    generationId: string;
    leaseToken: string;
    errorCode: string;
    error: string;
  }, options?: RpcCallOptions): Promise<FailOgGenerationResponse>;
}

interface RpcCallOptions {
  timeoutMs: number;
  signal: AbortSignal;
}

const RPC_TIMEOUT_MS = 15_000;

const internalServiceInterceptor: Interceptor = (next) => async (request) => {
  request.header.set('X-Internal-Service', env.TOKEN_SIGNING_SECRET);
  return next(request);
};

async function withRpcTimeout<T>(call: (options: RpcCallOptions) => Promise<T>): Promise<T> {
  return withAbortTimeout(RPC_TIMEOUT_MS, 'Backend OG RPC timed out', (signal) =>
    call({ timeoutMs: RPC_TIMEOUT_MS, signal })
  );
}

export function createInternalOgClient(baseUrl: string): InternalOgClient {
  const transport = createConnectTransport({
    baseUrl,
    httpVersion: '1.1',
    interceptors: [internalServiceInterceptor],
  });
  return createClient(InternalOgService, transport);
}

const client = createInternalOgClient(env.BACKEND_URL);

export function claimOgGeneration(generationId: string): Promise<ClaimOgGenerationResponse> {
  return withRpcTimeout((options) => client.claimOgGeneration({ generationId }, options));
}

export function completeOgGeneration(
  generationId: string,
  leaseToken: string,
  written: AssetWriteResult
): Promise<CompleteOgGenerationResponse> {
  return withRpcTimeout((options) =>
    client.completeOgGeneration({ generationId, leaseToken, written }, options)
  );
}

export function failOgGeneration(
  generationId: string,
  leaseToken: string,
  errorCode: string,
  error: string
): Promise<FailOgGenerationResponse> {
  return withRpcTimeout((options) =>
    client.failOgGeneration({ generationId, leaseToken, errorCode, error }, options)
  );
}
