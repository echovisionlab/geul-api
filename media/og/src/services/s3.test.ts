import { HeadObjectCommand, PutObjectCommand } from '@aws-sdk/client-s3';
import { create } from '@bufbuild/protobuf';
import { AssetDisposition } from '@echovisionlab/geul-event';
import { AssetWriteTargetSchema } from '@echovisionlab/geul-proto/common/media_pb.ts';
import { createHash } from 'node:crypto';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({
  constructor: vi.fn(),
  send: vi.fn(),
  logger: { info: vi.fn() },
}));

vi.mock('@aws-sdk/client-s3', async (load) => {
  const actual = await load<typeof import('@aws-sdk/client-s3')>();
  return {
    ...actual,
    S3Client: class {
      send = mocks.send;
      constructor(options: unknown) {
        mocks.constructor(options);
      }
    },
  };
});
vi.mock('../env.js', () => ({
  env: {
    S3_ENDPOINT: 'http://minio:9000',
    S3_REGION: 'us-east-1',
    S3_ACCESS_KEY_ID: 'access',
    S3_SECRET_ACCESS_KEY: 'secret',
    S3_MEDIA_BUCKET: 'media',
  },
}));
vi.mock('../logger.js', () => ({ logger: mocks.logger }));

const ASSET_ID = '00000000-0000-0000-0000-000000000001';
const target = create(AssetWriteTargetSchema, {
  assetId: ASSET_ID,
  objectKey: `asset/${ASSET_ID}.webp`,
  extension: 'webp',
  mimeType: 'image/webp',
  disposition: AssetDisposition.INLINE,
});
const body = Buffer.from('rendered-webp');
const digest = createHash('sha256').update(body).digest();
const expectedHead = {
  ContentLength: body.length,
  ContentType: 'image/webp',
  Metadata: { sha256: digest.toString('hex') },
  ChecksumSHA256: digest.toString('base64'),
};

describe('replay-safe OG S3 writer', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });
  afterEach(() => vi.useRealTimers());

  it('conditionally writes checksum-protected immutable output and returns exact result', async () => {
    const client = { send: vi.fn(async (_command: PutObjectCommand | HeadObjectCommand) => ({})) };
    const { writeOgAssetWithClient } = await import('./s3.js');
    const result = await writeOgAssetWithClient(client, target, body, 'bucket');
    const command = client.send.mock.calls[0][0];
    expect(command).toBeInstanceOf(PutObjectCommand);
    expect(command.input).toEqual({
      Bucket: 'bucket',
      Key: target.objectKey,
      Body: body,
      ContentType: 'image/webp',
      IfNoneMatch: '*',
      ChecksumSHA256: digest.toString('base64'),
      Metadata: { sha256: digest.toString('hex') },
    });
    expect(result.assetId).toBe(ASSET_ID);
    expect(result.fileSize).toBe(BigInt(body.length));
    expect(Buffer.from(result.sha256)).toEqual(digest);
  });

  it.each([
    { name: 'name', error: { name: 'PreconditionFailed' } },
    { name: 'code', error: { code: 'PreconditionFailed' } },
    { name: 'status', error: { $metadata: { httpStatusCode: 412 } } },
  ])('reuses an exact object after a $name precondition response', async ({ error }) => {
    const client = {
      send: vi.fn().mockRejectedValueOnce(error).mockResolvedValueOnce(expectedHead),
    };
    const { writeOgAssetWithClient } = await import('./s3.js');
    await expect(writeOgAssetWithClient(client, target, body, 'bucket')).resolves.toMatchObject({
      assetId: ASSET_ID,
    });
    const head = client.send.mock.calls[1][0];
    expect(head).toBeInstanceOf(HeadObjectCommand);
    expect(head.input).toEqual({
      Bucket: 'bucket',
      Key: target.objectKey,
      ChecksumMode: 'ENABLED',
    });
  });

  it('accepts a matching legacy Head response when S3 omits the optional checksum response', async () => {
    const client = {
      send: vi.fn().mockRejectedValueOnce({ name: 'PreconditionFailed' }).mockResolvedValueOnce({
        ...expectedHead,
        ChecksumSHA256: undefined,
      }),
    };
    const { writeOgAssetWithClient } = await import('./s3.js');
    await expect(writeOgAssetWithClient(client, target, body, 'bucket')).resolves.toBeDefined();
  });

  it.each([
    ['missing size', { ...expectedHead, ContentLength: undefined }],
    ['wrong size', { ...expectedHead, ContentLength: 999 }],
    ['missing metadata', { ...expectedHead, Metadata: undefined }],
    ['wrong hash', { ...expectedHead, Metadata: { sha256: 'bad' } }],
    ['wrong content type', { ...expectedHead, ContentType: 'image/png' }],
    ['wrong checksum', { ...expectedHead, ChecksumSHA256: 'bad' }],
  ])('rejects an existing object with %s without overwriting', async (_name, head) => {
    const client = {
      send: vi.fn().mockRejectedValueOnce({ name: 'PreconditionFailed' }).mockResolvedValueOnce(head),
    };
    const { writeOgAssetWithClient } = await import('./s3.js');
    await expect(writeOgAssetWithClient(client, target, body, 'bucket')).rejects.toMatchObject({
      name: 'IntegrityError',
      errorCode: 'integrity_failure',
    });
    expect(client.send).toHaveBeenCalledTimes(2);
  });

  it('turns a failed Head verification into a permanent integrity error', async () => {
    const client = {
      send: vi.fn()
        .mockRejectedValueOnce({ name: 'PreconditionFailed' })
        .mockRejectedValueOnce(new Error('head failed')),
    };
    const { writeOgAssetWithClient } = await import('./s3.js');
    await expect(writeOgAssetWithClient(client, target, body, 'bucket')).rejects.toMatchObject({
      name: 'IntegrityError',
    });
  });

  it('propagates a temporary Head failure for terminal lifecycle reporting', async () => {
    const temporary = { $metadata: { httpStatusCode: 503 } };
    const client = {
      send: vi.fn()
        .mockRejectedValueOnce({ name: 'PreconditionFailed' })
        .mockRejectedValueOnce(temporary),
    };
    const { writeOgAssetWithClient } = await import('./s3.js');
    await expect(writeOgAssetWithClient(client, target, body, 'bucket')).rejects.toBe(temporary);
  });

  it.each(['put', 'head'])('actively aborts a hung S3 %s operation', async (phase) => {
    vi.useFakeTimers();
    const client = {
      send: vi.fn((_command: PutObjectCommand | HeadObjectCommand, _options?: { abortSignal: AbortSignal }) => {
        if (phase === 'head' && client.send.mock.calls.length === 1) {
          return Promise.reject({ name: 'PreconditionFailed' });
        }
        return new Promise<never>(() => undefined);
      }),
    };
    const { writeOgAssetWithClient } = await import('./s3.js');
    const operation = writeOgAssetWithClient(client, target, body, 'bucket');
    const rejection = expect(operation).rejects.toMatchObject({ name: 'AbortError' });
    await vi.advanceTimersByTimeAsync(15_000);
    await rejection;
    const timedCall = client.send.mock.calls.at(-1)!;
    expect(timedCall[1]?.abortSignal.aborted).toBe(true);
  });

  it('never treats an S3 409 conditional conflict as an existing object', async () => {
    const conflict = { name: 'ConditionalRequestConflict', $metadata: { httpStatusCode: 409 } };
    const client = { send: vi.fn().mockRejectedValueOnce(conflict) };
    const { writeOgAssetWithClient } = await import('./s3.js');
    await expect(writeOgAssetWithClient(client, target, body, 'bucket')).rejects.toBe(conflict);
    expect(client.send).toHaveBeenCalledOnce();
  });

  it.each([new Error('S3 down'), null, { $metadata: { httpStatusCode: 500 } }])(
    'propagates non-precondition Put failures %#',
    async (error) => {
      const client = { send: vi.fn().mockRejectedValueOnce(error) };
      const { writeOgAssetWithClient } = await import('./s3.js');
      await expect(writeOgAssetWithClient(client, target, body, 'bucket')).rejects.toBe(error);
      expect(client.send).toHaveBeenCalledOnce();
    }
  );

  it('builds and reuses the configured MinIO client for the runtime wrapper', async () => {
    mocks.send.mockResolvedValue({});
    const { writeOgAsset } = await import('./s3.js');
    await writeOgAsset(target, body);
    await writeOgAsset(target, body);
    expect(mocks.constructor).toHaveBeenCalledOnce();
    expect(mocks.constructor).toHaveBeenCalledWith({
      endpoint: 'http://minio:9000',
      region: 'us-east-1',
      credentials: { accessKeyId: 'access', secretAccessKey: 'secret' },
      forcePathStyle: true,
    });
    expect(mocks.send).toHaveBeenCalledTimes(2);
  });
});
