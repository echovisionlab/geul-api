import { create } from '@bufbuild/protobuf';
import { OgGenerationClaimResult, OgGenerationStatus } from '@echovisionlab/geul-event';
import { AssetWriteResultSchema } from '@echovisionlab/geul-proto/common/media_pb.ts';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({
  client: {
    claimOgGeneration: vi.fn(),
    completeOgGeneration: vi.fn(),
    failOgGeneration: vi.fn(),
  },
  createClient: vi.fn(),
  createConnectTransport: vi.fn((options: unknown) => ({ options })),
}));

vi.mock('../env.js', () => ({
  env: {
    BACKEND_URL: 'http://backend:8080',
    TOKEN_SIGNING_SECRET: 'test-only-token-signing-secret',
  },
}));
vi.mock('@connectrpc/connect-node', () => ({
  createConnectTransport: mocks.createConnectTransport,
}));
vi.mock('@connectrpc/connect', () => ({
  createClient: mocks.createClient,
}));

const written = create(AssetWriteResultSchema, {
  assetId: '00000000-0000-0000-0000-000000000001',
  fileSize: 3n,
  sha256: new Uint8Array([1, 2, 3]),
});

describe('internal OG backend client', () => {
  beforeEach(() => {
    vi.resetModules();
    vi.clearAllMocks();
    mocks.createClient.mockReturnValue(mocks.client);
  });

  afterEach(() => vi.useRealTimers());

  it('creates only the lifecycle client and forwards exact RPC payloads', async () => {
    mocks.client.claimOgGeneration.mockResolvedValue({
      result: OgGenerationClaimResult.SKIP,
      generationStatus: OgGenerationStatus.READY,
    });
    mocks.client.completeOgGeneration.mockResolvedValue({ status: OgGenerationStatus.READY });
    mocks.client.failOgGeneration.mockResolvedValue({ status: OgGenerationStatus.FAILED });

    const backend = await import('./backend.js');
    const generationId = '00000000-0000-0000-0000-000000000001';
    await backend.claimOgGeneration(generationId);
    await backend.completeOgGeneration(generationId, 'lease', written);
    await backend.failOgGeneration(generationId, 'lease', 'render', 'bad input');

    expect(mocks.createConnectTransport).toHaveBeenCalledOnce();
    expect(mocks.createConnectTransport).toHaveBeenCalledWith(
      expect.objectContaining({
        baseUrl: 'http://backend:8080',
        httpVersion: '1.1',
        interceptors: [expect.any(Function)],
      })
    );
    expect(mocks.createClient).toHaveBeenCalledOnce();
    expect(mocks.client.claimOgGeneration).toHaveBeenCalledWith(
      { generationId },
      expect.objectContaining({ timeoutMs: 15_000, signal: expect.any(AbortSignal) })
    );
    expect(mocks.client.completeOgGeneration).toHaveBeenCalledWith({
      generationId,
      leaseToken: 'lease',
      written,
    }, expect.objectContaining({ timeoutMs: 15_000, signal: expect.any(AbortSignal) }));
    expect(mocks.client.failOgGeneration).toHaveBeenCalledWith({
      generationId,
      leaseToken: 'lease',
      errorCode: 'render',
      error: 'bad input',
    }, expect.objectContaining({ timeoutMs: 15_000, signal: expect.any(AbortSignal) }));
  });

  it('actively aborts every lifecycle RPC at the bounded deadline', async () => {
    vi.useFakeTimers();
    const never = vi.fn(() => new Promise<never>(() => undefined));
    mocks.client.claimOgGeneration.mockImplementationOnce(never);
    mocks.client.completeOgGeneration.mockImplementationOnce(never);
    mocks.client.failOgGeneration.mockImplementationOnce(never);
    const backend = await import('./backend.js');
    const id = '00000000-0000-0000-0000-000000000001';
    const operations = [
      backend.claimOgGeneration(id),
      backend.completeOgGeneration(id, 'lease', written),
      backend.failOgGeneration(id, 'lease', 'invalid', 'stop'),
    ];
    const assertions = operations.map((operation) =>
      expect(operation).rejects.toMatchObject({ name: 'AbortError' })
    );
    await vi.advanceTimersByTimeAsync(15_000);
    await Promise.all(assertions);
    for (const method of Object.values(mocks.client)) {
      expect(method.mock.calls[0][1].signal.aborted).toBe(true);
    }
  });

  it('exposes client construction for a different internal URL', async () => {
    const { createInternalOgClient } = await import('./backend.js');
    expect(createInternalOgClient('http://other:9000')).toBe(mocks.client);
    expect(mocks.createConnectTransport).toHaveBeenLastCalledWith(
      expect.objectContaining({
        baseUrl: 'http://other:9000',
        httpVersion: '1.1',
        interceptors: [expect.any(Function)],
      })
    );
  });

  it('adds only the canonical token signing secret through the transport interceptor', async () => {
    await import('./backend.js');
    const options = mocks.createConnectTransport.mock.calls[0][0] as {
      interceptors: Array<(next: (request: { header: Headers }) => unknown) => (request: { header: Headers }) => unknown>;
    };
    const next = vi.fn(async (request: { header: Headers }) => request);
    const request = { header: new Headers() };

    await options.interceptors[0](next)(request);

    expect(request.header.get('X-Internal-Service')).toBe('test-only-token-signing-secret');
    expect([...request.header.keys()]).toEqual(['x-internal-service']);
    expect(next).toHaveBeenCalledWith(request);
  });
});
