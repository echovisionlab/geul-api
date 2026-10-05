import { create } from '@bufbuild/protobuf';
import {
  AssetDisposition,
  type AssetWriteResult,
  type AssetWriteTarget,
} from '@echovisionlab/geul-event';
import { AssetWriteResultSchema } from '@echovisionlab/geul-proto/common/media_pb.ts';
import { createHash } from 'node:crypto';

const UUID_PATTERN = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
const OG_EXTENSION = 'webp';
const OG_MIME_TYPE = 'image/webp';

export function assertOgAssetTarget(
  target: AssetWriteTarget | null | undefined
): AssetWriteTarget {
  if (!target) {
    throw new Error('OG generate event is missing output asset target');
  }
  if (!UUID_PATTERN.test(target.assetId)) {
    throw new Error(`Invalid OG output asset ID: ${target.assetId}`);
  }
  if (target.extension !== OG_EXTENSION) {
    throw new Error(`Invalid OG output extension: ${target.extension}`);
  }
  if (target.mimeType !== OG_MIME_TYPE) {
    throw new Error(`Invalid OG output MIME type: ${target.mimeType}`);
  }
  if (target.disposition !== AssetDisposition.INLINE) {
    throw new Error(`Invalid OG output disposition: ${target.disposition}`);
  }
  if (target.downloadFilename !== undefined) {
    throw new Error('OG output must not define a download filename');
  }

  const expectedKey = `asset/${target.assetId}.${OG_EXTENSION}`;
  if (target.objectKey !== expectedKey) {
    throw new Error(`Invalid OG output object key: expected ${expectedKey}`);
  }

  return target;
}

export function createOgAssetResult(
  target: AssetWriteTarget,
  contents: Uint8Array
): AssetWriteResult {
  return create(AssetWriteResultSchema, {
    assetId: target.assetId,
    fileSize: BigInt(contents.byteLength),
    sha256: new Uint8Array(createHash('sha256').update(contents).digest()),
  });
}
