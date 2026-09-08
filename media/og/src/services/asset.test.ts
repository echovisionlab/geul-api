import { create } from '@bufbuild/protobuf';
import { AssetDisposition } from '@echovisionlab/geul-event';
import { AssetWriteTargetSchema } from '@echovisionlab/geul-proto/common/media_pb.ts';
import { createHash } from 'node:crypto';
import { describe, expect, it } from 'vitest';
import { assertOgAssetTarget, createOgAssetResult } from './asset.js';

const ASSET_ID = '00000000-0000-0000-0000-000000000001';

function target(overrides: Record<string, unknown> = {}) {
  return create(AssetWriteTargetSchema, {
    assetId: ASSET_ID,
    objectKey: `asset/${ASSET_ID}.webp`,
    extension: 'webp',
    mimeType: 'image/webp',
    disposition: AssetDisposition.INLINE,
    ...overrides,
  });
}

describe('OG asset contract', () => {
  it.each([
    [undefined, 'missing output asset target'],
    [target({ assetId: 'not-a-uuid' }), 'Invalid OG output asset ID'],
    [target({ extension: 'png' }), 'Invalid OG output extension'],
    [target({ mimeType: 'image/png' }), 'Invalid OG output MIME type'],
    [target({ disposition: AssetDisposition.ATTACHMENT }), 'Invalid OG output disposition'],
    [target({ downloadFilename: 'og.webp' }), 'must not define a download filename'],
    [target({ objectKey: 'asset/other.webp' }), 'Invalid OG output object key'],
  ])('rejects a non-canonical target %#', (value, message) => {
    expect(() => assertOgAssetTarget(value)).toThrow(message);
  });

  it('accepts the exact canonical target and creates an integrity result', () => {
    const value = target();
    const contents = Buffer.from('webp');

    expect(assertOgAssetTarget(value)).toBe(value);
    expect(createOgAssetResult(value, contents)).toMatchObject({
      assetId: ASSET_ID,
      fileSize: 4n,
      sha256: new Uint8Array(createHash('sha256').update(contents).digest()),
    });
  });
});
