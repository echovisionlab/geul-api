import {
  HeadObjectCommand,
  PutObjectCommand,
  S3Client,
  type HeadObjectCommandOutput,
} from '@aws-sdk/client-s3';
import type { AssetWriteResult, AssetWriteTarget } from '@echovisionlab/geul-event';
import { env } from '../env.js';
import { assertOgAssetTarget, createOgAssetResult } from './asset.js';
import { IntegrityError, isTransientInfrastructureError } from './errors.js';
import { withAbortTimeout } from './timeout.js';

export interface S3WriterClient {
  send(
    command: PutObjectCommand | HeadObjectCommand,
    options?: { abortSignal: AbortSignal }
  ): Promise<unknown>;
}

const S3_OPERATION_TIMEOUT_MS = 15_000;

async function sendWithTimeout(
  client: S3WriterClient,
  command: PutObjectCommand | HeadObjectCommand
): Promise<unknown> {
  return withAbortTimeout(S3_OPERATION_TIMEOUT_MS, 'S3 OG operation timed out', (abortSignal) =>
    client.send(command, { abortSignal })
  );
}

let s3Client: S3Client | null = null;

function getS3Client(): S3Client {
  s3Client ??= new S3Client({
    endpoint: env.S3_ENDPOINT,
    region: env.S3_REGION,
    credentials: {
      accessKeyId: env.S3_ACCESS_KEY_ID,
      secretAccessKey: env.S3_SECRET_ACCESS_KEY,
    },
    forcePathStyle: true,
  });
  return s3Client;
}

function isPreconditionFailure(error: unknown): boolean {
  if (typeof error !== 'object' || error === null) {
    return false;
  }
  const value = error as {
    name?: unknown;
    code?: unknown;
    $metadata?: { httpStatusCode?: unknown };
  };
  return (
    value.name === 'PreconditionFailed' ||
    value.code === 'PreconditionFailed' ||
    value.$metadata?.httpStatusCode === 412
  );
}

function assertExistingObjectMatches(
  head: HeadObjectCommandOutput,
  target: AssetWriteTarget,
  result: AssetWriteResult,
  sha256Hex: string,
  checksumBase64: string
): void {
  const mismatches: string[] = [];
  if (head.ContentLength !== Number(result.fileSize)) {
    mismatches.push(`size=${String(head.ContentLength)}`);
  }
  if (head.Metadata?.sha256?.toLowerCase() !== sha256Hex) {
    mismatches.push(`sha256=${String(head.Metadata?.sha256)}`);
  }
  if (head.ContentType !== target.mimeType) {
    mismatches.push(`contentType=${String(head.ContentType)}`);
  }
  if (head.ChecksumSHA256 !== undefined && head.ChecksumSHA256 !== checksumBase64) {
    mismatches.push(`checksum=${head.ChecksumSHA256}`);
  }
  if (mismatches.length > 0) {
    throw new IntegrityError(
      `Existing OG object ${target.objectKey} failed integrity verification: ${mismatches.join(', ')}`
    );
  }
}

export async function writeOgAssetWithClient(
  client: S3WriterClient,
  target: AssetWriteTarget,
  buffer: Buffer,
  bucket: string
): Promise<AssetWriteResult> {
  const validatedTarget = assertOgAssetTarget(target);
  const result = createOgAssetResult(validatedTarget, buffer);
  const digest = Buffer.from(result.sha256);
  const sha256Hex = digest.toString('hex');
  const checksumBase64 = digest.toString('base64');

  try {
    await sendWithTimeout(
      client,
      new PutObjectCommand({
        Bucket: bucket,
        Key: validatedTarget.objectKey,
        Body: buffer,
        ContentType: validatedTarget.mimeType,
        IfNoneMatch: '*',
        ChecksumSHA256: checksumBase64,
        Metadata: { sha256: sha256Hex },
      })
    );
    return result;
  } catch (error) {
    if (!isPreconditionFailure(error)) {
      throw error;
    }
  }

  let head: HeadObjectCommandOutput;
  try {
    head = (await sendWithTimeout(
      client,
      new HeadObjectCommand({
        Bucket: bucket,
        Key: validatedTarget.objectKey,
        ChecksumMode: 'ENABLED',
      })
    )) as HeadObjectCommandOutput;
  } catch (error) {
    if (isTransientInfrastructureError(error)) {
      throw error;
    }
    throw new IntegrityError(
      `Existing OG object ${validatedTarget.objectKey} could not be verified`,
      { cause: error }
    );
  }

  assertExistingObjectMatches(head, validatedTarget, result, sha256Hex, checksumBase64);
  return result;
}

export function writeOgAsset(
  target: AssetWriteTarget,
  buffer: Buffer
): Promise<AssetWriteResult> {
  return writeOgAssetWithClient(
    getS3Client() as unknown as S3WriterClient,
    target,
    buffer,
    env.S3_MEDIA_BUCKET
  );
}
