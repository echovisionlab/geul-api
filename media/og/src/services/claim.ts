import { Code, ConnectError } from '@connectrpc/connect';
import { timestampMs } from '@bufbuild/protobuf/wkt';
import {
  OgEntityType,
  OgGenerationClaimResult,
  OgGenerationStatus,
  type AssetWriteTarget,
  type ClaimOgGenerationResponse,
  type OgRenderConfigSnapshot,
} from '@echovisionlab/geul-event';
import { logger } from '../logger.js';
import { assertOgAssetTarget } from './asset.js';
import { claimOgGeneration } from './backend.js';
import {
  PermanentGenerationError,
  RecoverGenerationLeaseError,
  RequeueMessageError,
  getErrorMessage,
  isTransientInfrastructureError,
} from './errors.js';

const LOCALE_ENTITIES = new Set([
  OgEntityType.POST,
  OgEntityType.PAGE,
  OgEntityType.WORK,
  OgEntityType.ARTIST,
  OgEntityType.FORM,
  OgEntityType.SERIES,
  OgEntityType.PRIVACY,
  OgEntityType.TERMS,
]);
const ENTITY_ENTITIES = new Set([
  OgEntityType.LABEL,
  OgEntityType.RELEASE,
  OgEntityType.SITE,
]);

export interface ValidatedClaim {
  leaseToken: string;
  entityType: OgEntityType;
  entityId: string;
  title: string;
  snapshot: OgRenderConfigSnapshot;
  output: AssetWriteTarget;
}

export type ClaimDisposition = { state: 'skip' } | null;

type ClaimedTarget = NonNullable<ClaimOgGenerationResponse['target']>;

function requireClaimTarget(claim: ClaimOgGenerationResponse): ClaimedTarget {
  const target = claim.target;
  if (!target || target.entityType === OgEntityType.UNSPECIFIED || !target.entityId.trim()) {
    throw new PermanentGenerationError('Claimed OG generation has an invalid target', 'invalid_target');
  }
  return target;
}

function validateTargetScope(target: ClaimedTarget): void {
  if (LOCALE_ENTITIES.has(target.entityType)) {
    validateLocaleScope(target);
    return;
  }
  if (ENTITY_ENTITIES.has(target.entityType)) {
    validateEntityScope(target);
    return;
  }
  throw new PermanentGenerationError(
    'Claimed OG generation has an unsupported entity',
    'unsupported_entity'
  );
}

function validateLocaleScope(target: ClaimedTarget): void {
  if (target.scope.case !== 'locale' || !target.scope.value.locale.trim()) {
    throw new PermanentGenerationError('Locale-aware OG target has no locale', 'invalid_target');
  }
}

function validateEntityScope(target: ClaimedTarget): void {
  if (target.scope.case !== 'entity') {
    throw new PermanentGenerationError('Entity OG target has an invalid scope', 'invalid_target');
  }
}

function requireOutputTarget(
  generationId: string,
  claim: ClaimOgGenerationResponse
): AssetWriteTarget {
  let output: AssetWriteTarget;
  try {
    output = assertOgAssetTarget(claim.output);
  } catch (error) {
    throw new PermanentGenerationError(getErrorMessage(error), 'invalid_target', { cause: error });
  }
  if (output.assetId !== generationId) {
    throw new PermanentGenerationError(
      `OG output asset ${output.assetId} does not match generation ${generationId}`,
      'invalid_target'
    );
  }
  return output;
}

function requireRenderSnapshot(claim: ClaimOgGenerationResponse): OgRenderConfigSnapshot {
  if (!claim.renderConfig || !claim.renderConfig.revision.trim()) {
    throw new PermanentGenerationError('Claimed OG generation has no render snapshot', 'invalid_config');
  }
  return claim.renderConfig;
}

export function validateClaim(
  generationId: string,
  claim: ClaimOgGenerationResponse
): ValidatedClaim {
  if (!claim.leaseToken) {
    throw new PermanentGenerationError(
      'Claimed OG generation has no lease token',
      'invalid_target'
    );
  }
  const target = requireClaimTarget(claim);
  validateTargetScope(target);
  if (target.entityType !== OgEntityType.SITE && (claim.title === undefined || !claim.title.trim())) {
    throw new PermanentGenerationError('Claimed OG generation has no canonical title', 'missing_title');
  }
  return {
    leaseToken: claim.leaseToken,
    entityType: target.entityType,
    entityId: target.entityId,
    title: claim.title ?? '',
    snapshot: requireRenderSnapshot(claim),
    output: requireOutputTarget(generationId, claim),
  };
}

export async function claimGeneration(
  claim: typeof claimOgGeneration,
  generationId: string
): Promise<ClaimOgGenerationResponse | null> {
  try {
    return await claim(generationId);
  } catch (error) {
    if (error instanceof ConnectError && error.code === Code.NotFound) {
      logger.warn(
        {
          reason: 'stale_or_unknown',
          generationId,
        },
        'Skipping stale or unknown OG generation job'
      );
      return null;
    }
    if (isTransientInfrastructureError(error)) {
      throw new RequeueMessageError(
        `OG claim result is uncertain for ${generationId}`,
        { cause: error }
      );
    }
    throw error;
  }
}

function recoverActiveLease(claim: ClaimOgGenerationResponse): never {
  if (!claim.leaseExpiresAt) {
    throw new PermanentGenerationError(
      'Active OG generation claim has no lease expiry',
      'invalid_target'
    );
  }
  const visibilitySeconds = Math.max(
    1,
    Math.ceil((timestampMs(claim.leaseExpiresAt) - Date.now()) / 1_000)
  );
  throw new RecoverGenerationLeaseError(
    'OG generation is held by an active processing lease',
    visibilitySeconds
  );
}

function skippedClaimDisposition(claim: ClaimOgGenerationResponse): ClaimDisposition {
  if (claim.generationStatus === OgGenerationStatus.PROCESSING) {
    return recoverActiveLease(claim);
  }
  if ([
    OgGenerationStatus.READY,
    OgGenerationStatus.FAILED,
    OgGenerationStatus.SUPERSEDED,
    OgGenerationStatus.CANCELLED,
  ].includes(claim.generationStatus)) {
    return { state: 'skip' };
  }
  throw new PermanentGenerationError(
    `Skipped OG generation has invalid status ${String(claim.generationStatus)}`,
    'invalid_target'
  );
}

export function claimDisposition(claim: ClaimOgGenerationResponse): ClaimDisposition {
  if (claim.result === OgGenerationClaimResult.SKIP) {
    return skippedClaimDisposition(claim);
  }
  if (claim.result !== OgGenerationClaimResult.CLAIMED) {
    throw new PermanentGenerationError(
      `Invalid claim result ${String(claim.result)}`,
      'invalid_target'
    );
  }
  return null;
}
