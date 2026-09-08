import { create } from '@bufbuild/protobuf';
import {
  AssetDisposition,
  OgEntityType,
  OgGenerationClaimResult,
  OgGenerationStatus,
  type ClaimOgGenerationResponse,
} from '@echovisionlab/geul-event';
import { AssetWriteResultSchema, AssetWriteTargetSchema } from '@echovisionlab/geul-proto/common/media_pb.ts';

export const GENERATION_ID = '00000000-0000-0000-0000-000000000001';

export const target = create(AssetWriteTargetSchema, {
  assetId: GENERATION_ID,
  objectKey: `asset/${GENERATION_ID}.webp`,
  extension: 'webp',
  mimeType: 'image/webp',
  disposition: AssetDisposition.INLINE,
});

export const written = create(AssetWriteResultSchema, {
  assetId: GENERATION_ID,
  fileSize: 4n,
  sha256: new Uint8Array([1, 2, 3]),
});

export function claimed(
  overrides: Record<string, unknown> = {}
): ClaimOgGenerationResponse {
  return {
    $typeName: 'api.intra.v1.ClaimOgGenerationResponse',
    result: OgGenerationClaimResult.CLAIMED,
    generationStatus: OgGenerationStatus.PROCESSING,
    leaseToken: 'lease-token',
    target: {
      $typeName: 'api.manage.v1.OgGenerationTarget',
      entityType: OgEntityType.FORM,
      entityId: 'form-1',
      scope: {
        case: 'locale',
        value: { $typeName: 'api.manage.v1.OgLocaleTarget', locale: 'ko' },
      },
    },
    title: '  정확한 번역 제목  ',
    featuredImage: {
      $typeName: 'api.common.v1.AssetRef',
      assetId: '00000000-0000-0000-0000-000000000002',
      url: 'https://cdn.test/featured.webp',
      extension: 'webp',
      mimeType: 'image/webp',
      disposition: AssetDisposition.INLINE,
    },
    output: target,
    renderConfig: {
      $typeName: 'api.intra.v1.OgRenderConfigSnapshot',
      siteTitle: 'Example Studio snapshot',
      primaryColor: '#123456',
      logoAsset: {
        $typeName: 'api.common.v1.AssetRef',
        assetId: '00000000-0000-0000-0000-000000000003',
        url: 'https://cdn.test/logo.png',
        extension: 'png',
        mimeType: 'image/png',
        disposition: AssetDisposition.INLINE,
      },
      ogImageConfig: {
        content: { darkBackground: '#010203', title: { fontSizeLarge: 51 } },
        home: { darkBackground: '#040506' },
      },
      revision: 'revision-sha256',
    },
    ...overrides,
  } as unknown as ClaimOgGenerationResponse;
}
