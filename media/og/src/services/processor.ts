import {
  OgEntityType,
  type AssetWriteResult,
  type ClaimOgGenerationResponse,
} from '@echovisionlab/geul-event';
import type {
  ContentOgImageConfig,
  HomeOgImageConfig,
  LabelOgImageConfig,
  OgImageSettings,
} from '../types/config.js';
import {
  claimOgGeneration,
  completeOgGeneration,
  failOgGeneration,
} from './backend.js';
import {
  claimDisposition,
  claimGeneration,
  validateClaim,
  type ValidatedClaim,
} from './claim.js';
import { completeWithRetry, reportFailure } from './completion.js';
import {
  RequeueMessageError,
  asGenerationFailure,
} from './errors.js';
import { generateContentOgImage, generateHomeOgImage, generateLabelOgImage } from './generator.js';
import { fetchImageAsDataUrl } from './image_source.js';
import { emitGenerationFailed, emitGenerationSucceeded } from './job_telemetry.js';
import { getSnapshotConfigs } from './render_config.js';
import { writeOgAsset } from './s3.js';
import { sleep } from './timeout.js';

export type OgProcessOutcome =
  | { state: 'skip' | 'failed' }
  | { state: 'completed'; asset: AssetWriteResult };

interface ProcessorDependencies {
  claim: typeof claimOgGeneration;
  fetchImage: typeof fetchImageAsDataUrl;
  generateContent: (
    title: string,
    settings: OgImageSettings,
    config: ContentOgImageConfig
  ) => Promise<Buffer>;
  generateHome: (settings: OgImageSettings, config: HomeOgImageConfig) => Promise<Buffer>;
  generateLabel: (settings: OgImageSettings, config: LabelOgImageConfig) => Promise<Buffer>;
  writeAsset: typeof writeOgAsset;
  complete: typeof completeOgGeneration;
  fail: typeof failOgGeneration;
  sleep: (milliseconds: number) => Promise<void>;
}

const defaultDependencies: ProcessorDependencies = {
  claim: claimOgGeneration,
  fetchImage: fetchImageAsDataUrl,
  generateContent: generateContentOgImage,
  generateHome: generateHomeOgImage,
  generateLabel: generateLabelOgImage,
  writeAsset: writeOgAsset,
  complete: completeOgGeneration,
  fail: failOgGeneration,
  sleep,
};

type ClaimValidation =
  | { valid: true; claim: ValidatedClaim }
  | { valid: false; error: unknown };

function validateClaimResult(
  generationId: string,
  claim: ClaimOgGenerationResponse
): ClaimValidation {
  try {
    return { valid: true, claim: validateClaim(generationId, claim) };
  } catch (error) {
    return { valid: false, error };
  }
}

async function reportClaimValidationFailure(
  dependencies: ProcessorDependencies,
  generationId: string,
  claim: ClaimOgGenerationResponse,
  error: unknown,
  startedAt: number
): Promise<OgProcessOutcome> {
  const leaseToken = claim.leaseToken;
  if (error instanceof RequeueMessageError || !leaseToken) {
    throw error;
  }
  const failure = asGenerationFailure(error);
  await reportFailure(dependencies, generationId, leaseToken, failure);
  emitGenerationFailed(generationId, startedAt, failure);
  return { state: 'failed' };
}

async function loadImageSettings(
  dependencies: ProcessorDependencies,
  claim: ClaimOgGenerationResponse,
  validated: ValidatedClaim
): Promise<OgImageSettings> {
  let featuredImageDataUrl: string | undefined;
  if (claim.featuredImage?.url) {
    const usage = validated.entityType === OgEntityType.LABEL ? 'label-logo' : 'featured';
    featuredImageDataUrl = await dependencies.fetchImage(claim.featuredImage.url, usage);
  }

  let logoSvg: string | undefined;
  if (validated.entityType !== OgEntityType.LABEL && validated.snapshot.logoAsset?.url) {
    logoSvg = await dependencies.fetchImage(validated.snapshot.logoAsset.url, 'logo');
  }

  return {
    siteTitle: validated.snapshot.siteTitle,
    primaryColor: validated.snapshot.primaryColor,
    logoSvg,
    featuredImageDataUrl,
  };
}

async function renderClaim(
  dependencies: ProcessorDependencies,
  validated: ValidatedClaim,
  settings: OgImageSettings
): Promise<Buffer> {
  const configs = getSnapshotConfigs(validated.snapshot);
  if (validated.entityType === OgEntityType.SITE) {
    return dependencies.generateHome(settings, configs.home);
  }
  if (validated.entityType === OgEntityType.LABEL) {
    return dependencies.generateLabel(settings, configs.label);
  }
  return dependencies.generateContent(
    validated.title,
    settings,
    configs.content
  );
}

async function executeClaimedGeneration(
  dependencies: ProcessorDependencies,
  generationId: string,
  claim: ClaimOgGenerationResponse,
  validated: ValidatedClaim,
  startedAt: number
): Promise<OgProcessOutcome> {
  try {
    const settings = await loadImageSettings(dependencies, claim, validated);
    const buffer = await renderClaim(dependencies, validated, settings);
    const result = await dependencies.writeAsset(validated.output, buffer);
    await completeWithRetry(
      dependencies,
      generationId,
      validated.leaseToken,
      result
    );
    emitGenerationSucceeded(generationId, startedAt);
    return { state: 'completed', asset: result };
  } catch (error) {
    if (error instanceof RequeueMessageError) {
      throw error;
    }
    const failure = asGenerationFailure(error);
    await reportFailure(
      dependencies,
      generationId,
      validated.leaseToken,
      failure
    );
    emitGenerationFailed(generationId, startedAt, failure);
    return { state: 'failed' };
  }
}

async function processGeneration(
  dependencies: ProcessorDependencies,
  generationId: string
): Promise<OgProcessOutcome> {
  const startedAt = Date.now();
  const claim = await claimGeneration(dependencies.claim, generationId);
  if (!claim) {
    return { state: 'skip' };
  }

  const outcome = claimDisposition(claim);
  if (outcome) {
    return outcome;
  }

  const validation = validateClaimResult(generationId, claim);
  if (!validation.valid) {
    return reportClaimValidationFailure(
      dependencies,
      generationId,
      claim,
      validation.error,
      startedAt
    );
  }
  return executeClaimedGeneration(
    dependencies,
    generationId,
    claim,
    validation.claim,
    startedAt
  );
}

export function createOgProcessor(
  overrides: Partial<ProcessorDependencies> = {}
): (generationId: string) => Promise<OgProcessOutcome> {
  const dependencies = { ...defaultDependencies, ...overrides };
  return (generationId) => processGeneration(dependencies, generationId);
}

export const processOgGenerate = createOgProcessor();
