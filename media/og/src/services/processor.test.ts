import { Code, ConnectError } from '@connectrpc/connect';
import { timestampFromDate } from '@bufbuild/protobuf/wkt';
import {
  OgEntityType,
  OgGenerationClaimResult,
  OgGenerationStatus,
} from '@echovisionlab/geul-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import {
  IntegrityError,
  PermanentGenerationError,
  RequeueMessageError,
  TransientGenerationError,
} from './errors.js';
import { fetchImageAsDataUrl } from './image_source.js';
import {
  GENERATION_ID,
  claimed,
  dependencies,
  processorWith,
  target,
  written,
} from './processor.test-fixture.js';


const mocks = vi.hoisted(() => ({
  logger: { error: vi.fn(), info: vi.fn(), warn: vi.fn(), system: vi.fn() },
}));
vi.mock('../logger.js', () => ({ logger: mocks.logger }));
vi.mock('./backend.js', () => ({
  claimOgGeneration: vi.fn(),
  completeOgGeneration: vi.fn(),
  failOgGeneration: vi.fn(),
}));
vi.mock('./s3.js', () => ({ writeOgAsset: vi.fn() }));


describe('claim-driven OG processor', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.logger.system.mockImplementation((level: 'info' | 'warn' | 'error', record: unknown) =>
      mocks.logger[level](record, 'System event')
    );
  });

  it.each([
    OgGenerationStatus.READY,
    OgGenerationStatus.FAILED,
    OgGenerationStatus.SUPERSEDED,
    OgGenerationStatus.CANCELLED,
  ])('acks a terminal skipped claim in status %s without processing it', async (generationStatus) => {
    const deps = dependencies(claimed({
      result: OgGenerationClaimResult.SKIP,
      generationStatus,
      leaseToken: undefined,
    }));
    await expect(processorWith(deps)(GENERATION_ID)).resolves.toEqual({ state: 'skip' });
    expect(deps.generateContent).not.toHaveBeenCalled();
    expect(mocks.logger.info).not.toHaveBeenCalledWith(
      expect.objectContaining({ event: expect.stringMatching(/^job\./) }),
      'System event'
    );
  });

  it('parks an active-lease crash recovery delivery until the exact lease expiry', async () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2026-08-21T00:00:00Z'));
    const deps = dependencies(claimed({
      result: OgGenerationClaimResult.SKIP,
      generationStatus: OgGenerationStatus.PROCESSING,
      leaseToken: undefined,
      leaseExpiresAt: timestampFromDate(new Date('2026-08-21T00:10:00Z')),
    }));

    await expect(processorWith(deps)(GENERATION_ID)).rejects.toMatchObject({
      name: 'RecoverGenerationLeaseError',
      visibilitySeconds: 600,
    });
    expect(deps.generateContent).not.toHaveBeenCalled();
    vi.useRealTimers();
  });

  it('rejects a processing skip without its required lease expiry', async () => {
    const deps = dependencies(claimed({
      result: OgGenerationClaimResult.SKIP,
      generationStatus: OgGenerationStatus.PROCESSING,
      leaseToken: undefined,
      leaseExpiresAt: undefined,
    }));

    await expect(processorWith(deps)(GENERATION_ID)).rejects.toMatchObject({
      name: 'PermanentGenerationError',
      errorCode: 'invalid_target',
    });
  });

  it('rejects a skipped nonterminal status that is not active processing', async () => {
    const deps = dependencies(claimed({
      result: OgGenerationClaimResult.SKIP,
      generationStatus: OgGenerationStatus.QUEUED,
      leaseToken: undefined,
    }));

    await expect(processorWith(deps)(GENERATION_ID)).rejects.toMatchObject({
      name: 'PermanentGenerationError',
      errorCode: 'invalid_target',
    });
  });

  it('rejects an unknown claim result without retrying it as commit uncertainty', async () => {
    const invalid = dependencies(claimed({ result: OgGenerationClaimResult.UNSPECIFIED }));
    await expect(processorWith(invalid)(GENERATION_ID)).rejects.toMatchObject({
      name: 'PermanentGenerationError',
      errorCode: 'invalid_target',
    });
  });

  it('requeues a temporary claim RPC failure as same-generation commit uncertainty', async () => {
    const unavailable = dependencies();
    unavailable.claim.mockRejectedValueOnce(new ConnectError('down', Code.Unavailable));
    await expect(processorWith(unavailable)(GENERATION_ID)).rejects.toMatchObject({
      name: 'RequeueMessageError',
    });
  });

  it('does not requeue a deterministically rejected claim RPC', async () => {
    const rejected = dependencies();
    rejected.claim.mockRejectedValueOnce(new ConnectError('denied', Code.PermissionDenied));

    await expect(processorWith(rejected)(GENERATION_ID)).rejects.toMatchObject({
      code: Code.PermissionDenied,
    });
  });

  it('acks a syntactically valid but stale or unknown generation as skipped', async () => {
    const deps = dependencies();
    deps.claim.mockRejectedValueOnce(new ConnectError('missing', Code.NotFound));
    await expect(processorWith(deps)(GENERATION_ID)).resolves.toEqual({ state: 'skip' });
    expect(deps.generateContent).not.toHaveBeenCalled();
    expect(deps.fail).not.toHaveBeenCalled();
    expect(mocks.logger.warn).toHaveBeenCalledWith(
      expect.objectContaining({
        generationId: GENERATION_ID,
        reason: 'stale_or_unknown',
      }),
      'Skipping stale or unknown OG generation job'
    );
    expect(mocks.logger.info).not.toHaveBeenCalledWith(
      expect.objectContaining({ event: expect.stringMatching(/^job\./) }),
      'System event'
    );
  });

  it('renders the exact Form locale title and the claimed render snapshot', async () => {
    const deps = dependencies();
    await expect(processorWith(deps)(GENERATION_ID)).resolves.toEqual({
      state: 'completed',
      asset: written,
    });
    expect(deps.fetchImage.mock.calls).toEqual([
      ['https://cdn.test/featured.webp', 'featured'],
      ['https://cdn.test/logo.png', 'logo'],
    ]);
    expect(deps.generateContent).toHaveBeenCalledWith(
      '  정확한 번역 제목  ',
      {
        siteTitle: 'Example Studio snapshot',
        primaryColor: '#123456',
        featuredImageDataUrl: 'data:featured',
        logoSvg: 'data:logo',
      },
      expect.objectContaining({
        darkBackground: '#010203',
        title: expect.objectContaining({ fontSizeLarge: 51 }),
      })
    );
    expect(deps.complete).toHaveBeenCalledWith(GENERATION_ID, 'lease-token', written);
    expect(mocks.logger.info).toHaveBeenCalledWith(
      expect.objectContaining({
        event: 'job.succeeded',
        outcome: 'succeeded',
        job_kind: 'og_generation',
        job_id: GENERATION_ID,
      }),
      'System event'
    );
  });

  it('does not retry completed business work when terminal telemetry fails', async () => {
    const telemetryFailure = new Error('logger unavailable');
    mocks.logger.info.mockImplementationOnce(() => {
      throw telemetryFailure;
    });
    const deps = dependencies();

    await expect(processorWith(deps)(GENERATION_ID)).resolves.toEqual({
      state: 'completed',
      asset: written,
    });
    expect(deps.complete).toHaveBeenCalledOnce();
    expect(mocks.logger.error).toHaveBeenCalledWith(
      { error: telemetryFailure },
      'System telemetry emission failed'
    );
  });

  it('renders a locale-scoped Series title-only OG when no Featured Image is claimed', async () => {
	const seriesClaim = claimed({
		title: '한국어 포스트 시리즈',
		featuredImage: undefined,
		target: {
			$typeName: 'api.manage.v1.OgGenerationTarget',
			entityType: OgEntityType.SERIES,
			entityId: 'series-1',
			scope: { case: 'locale', value: { $typeName: 'api.manage.v1.OgLocaleTarget', locale: 'ko' } },
		},
	});
	const deps = dependencies(seriesClaim);

	await expect(processorWith(deps)(GENERATION_ID)).resolves.toEqual({
		state: 'completed',
		asset: written,
	});
	expect(deps.fetchImage).toHaveBeenCalledTimes(1);
	expect(deps.fetchImage).toHaveBeenCalledWith('https://cdn.test/logo.png', 'logo');
	expect(deps.generateContent).toHaveBeenCalledWith(
		'한국어 포스트 시리즈',
		expect.objectContaining({ featuredImageDataUrl: undefined, logoSvg: 'data:logo' }),
		expect.any(Object),
	);
  });

  it.each([
    [OgEntityType.WORK, 'work-1', 'Translated work'],
    [OgEntityType.ARTIST, 'artist-1', 'Translated artist'],
  ])('renders locale-scoped %s from its claimed translation snapshot', async (entityType, entityId, title) => {
    const deps = dependencies(claimed({
      title,
      target: {
        $typeName: 'api.manage.v1.OgGenerationTarget',
        entityType,
        entityId,
        scope: {
          case: 'locale',
          value: { $typeName: 'api.manage.v1.OgLocaleTarget', locale: 'fr' },
        },
      },
    }));

    await expect(processorWith(deps)(GENERATION_ID)).resolves.toEqual({
      state: 'completed',
      asset: written,
    });
    expect(deps.generateContent).toHaveBeenCalledWith(
      title,
      expect.objectContaining({ featuredImageDataUrl: 'data:featured' }),
      expect.any(Object)
    );
  });

  it('renders Site through the home snapshot without inventing a title', async () => {
    const siteClaim = claimed({
      title: undefined,
      featuredImage: undefined,
      target: {
        $typeName: 'api.manage.v1.OgGenerationTarget',
        entityType: OgEntityType.SITE,
        entityId: 'site',
        scope: { case: 'entity', value: { $typeName: 'api.manage.v1.OgEntityTarget' } },
      },
    });
    const deps = dependencies(siteClaim);
    await processorWith(deps)(GENERATION_ID);
    expect(deps.generateHome).toHaveBeenCalledWith(
      expect.objectContaining({ siteTitle: 'Example Studio snapshot' }),
      expect.objectContaining({ darkBackground: '#040506' })
    );
    expect(deps.generateContent).not.toHaveBeenCalled();
  });

  it('renders Label through the logo-only template without passing its name to the raster', async () => {
    const labelClaim = claimed({
      target: {
        $typeName: 'api.manage.v1.OgGenerationTarget',
        entityType: OgEntityType.LABEL,
        entityId: 'label-1',
        scope: { case: 'entity', value: { $typeName: 'api.manage.v1.OgEntityTarget' } },
      },
      title: 'Metadata title only',
    });
    const deps = dependencies(labelClaim);

    await processorWith(deps)(GENERATION_ID);

    expect(deps.fetchImage.mock.calls[0]).toEqual([
      'https://cdn.test/featured.webp',
      'label-logo',
    ]);
    expect(deps.fetchImage).toHaveBeenCalledTimes(1);
    expect(deps.generateLabel).toHaveBeenCalledWith(
      expect.objectContaining({ featuredImageDataUrl: 'data:label-logo' }),
      { darkBackground: '#1A1B1E', logo: { width: 720, height: 315 } }
    );
    expect(deps.generateContent).not.toHaveBeenCalled();
    expect(deps.generateHome).not.toHaveBeenCalled();
  });

  it.each([
    ['missing lease', { leaseToken: undefined }, 'reject'],
    ['missing target', { target: undefined }, 'failed'],
    ['unspecified target', { target: { entityType: OgEntityType.UNSPECIFIED, entityId: 'x', scope: { case: undefined } } }, 'failed'],
    ['empty entity id', { target: { entityType: OgEntityType.FORM, entityId: ' ', scope: { case: 'locale', value: { locale: 'ko' } } } }, 'failed'],
    ['locale entity without locale', { target: { entityType: OgEntityType.FORM, entityId: 'x', scope: { case: 'locale', value: { locale: ' ' } } } }, 'failed'],
    ['locale target with entity scope', { target: { entityType: OgEntityType.WORK, entityId: 'x', scope: { case: 'entity', value: {} } } }, 'failed'],
    ['entity target with locale scope', { target: { entityType: OgEntityType.LABEL, entityId: 'x', scope: { case: 'locale', value: { locale: 'ko' } } } }, 'failed'],
    ['unsupported entity', { target: { entityType: 99, entityId: 'x', scope: { case: 'entity', value: {} } } }, 'failed'],
    ['missing title', { title: ' ' }, 'failed'],
    ['missing output', { output: undefined }, 'failed'],
    ['bad output', { output: { ...target, mimeType: 'image/png' } }, 'failed'],
    ['mismatched output id', { output: { ...target, assetId: '00000000-0000-0000-0000-000000000099', objectKey: 'asset/00000000-0000-0000-0000-000000000099.webp' } }, 'failed'],
    ['missing config', { renderConfig: undefined }, 'failed'],
    ['missing revision', { renderConfig: { ...claimed().renderConfig, revision: ' ' } }, 'failed'],
  ])('handles claimed protocol error: %s', async (_name, change, expected) => {
    const deps = dependencies(claimed(change));
    const operation = processorWith(deps)(GENERATION_ID);
    if (expected === 'reject') {
      await expect(operation).rejects.toMatchObject({
        name: 'PermanentGenerationError',
        errorCode: 'invalid_target',
      });
    } else {
      await expect(operation).resolves.toEqual({ state: 'failed' });
      expect(deps.fail).toHaveBeenCalled();
      expect(mocks.logger.error).toHaveBeenCalledWith(
        expect.objectContaining({ event: 'job.failed', reason: 'invalid_claim' }),
        'System event'
      );
    }
  });

  it('terminalizes transient and permanent generation failures without automatic retry', async () => {
    const transient = dependencies();
    transient.writeAsset.mockRejectedValueOnce({ $metadata: { httpStatusCode: 503 } });
    await expect(processorWith(transient)(GENERATION_ID)).resolves.toEqual({ state: 'failed' });
    expect(transient.fail).toHaveBeenCalledWith(
      GENERATION_ID,
      'lease-token',
      'transient_infrastructure',
      '[object Object]'
    );

    const permanent = dependencies();
    permanent.writeAsset.mockRejectedValueOnce(new IntegrityError('hash mismatch'));
    await expect(processorWith(permanent)(GENERATION_ID)).resolves.toEqual({ state: 'failed' });
    expect(permanent.fail).toHaveBeenCalledWith(
      GENERATION_ID,
      'lease-token',
      'integrity_failure',
      'hash mismatch'
    );
    expect(mocks.logger.error).toHaveBeenCalledWith(
      expect.objectContaining({ event: 'job.failed', reason: 'integrity_failed' }),
      'System event'
    );
  });

  it('terminalizes an S3 conditional-request conflict without treating it as replay success', async () => {
    const deps = dependencies();
    deps.writeAsset.mockRejectedValueOnce({
      name: 'ConditionalRequestConflict',
      $metadata: { httpStatusCode: 409 },
    });
    await expect(processorWith(deps)(GENERATION_ID)).resolves.toEqual({ state: 'failed' });
    expect(deps.complete).not.toHaveBeenCalled();
    expect(deps.fail).toHaveBeenCalledWith(
      GENERATION_ID,
      'lease-token',
      'transient_infrastructure',
      '[object Object]'
    );
    expect(mocks.logger.error).toHaveBeenCalledWith(
      expect.objectContaining({ event: 'job.failed', reason: 'processing_failed' }),
      'System event'
    );
  });

  it('does not strip claimed assets or retry a failed content render', async () => {
    const deps = dependencies();
    deps.generateContent.mockRejectedValueOnce(new Error('XML parse error'));

    await expect(processorWith(deps)(GENERATION_ID)).resolves.toEqual({ state: 'failed' });

    expect(deps.generateContent).toHaveBeenCalledOnce();
    expect(deps.generateContent).toHaveBeenCalledWith(
      '  정확한 번역 제목  ',
      expect.objectContaining({
        featuredImageDataUrl: 'data:featured',
        logoSvg: 'data:logo',
      }),
      expect.any(Object)
    );
    expect(deps.fail).toHaveBeenCalledWith(
      GENERATION_ID,
      'lease-token',
      'generation_failed',
      'XML parse error'
    );
  });

  it('does not strip the claimed logo or retry a failed Site render', async () => {
    const deps = dependencies(claimed({
      title: undefined,
      featuredImage: undefined,
      target: {
        $typeName: 'api.manage.v1.OgGenerationTarget',
        entityType: OgEntityType.SITE,
        entityId: 'site',
        scope: { case: 'entity', value: { $typeName: 'api.manage.v1.OgEntityTarget' } },
      },
    }));
    deps.generateHome.mockRejectedValueOnce(new Error('Buffer size limit exceeded'));

    await expect(processorWith(deps)(GENERATION_ID)).resolves.toEqual({ state: 'failed' });

    expect(deps.generateHome).toHaveBeenCalledOnce();
    expect(deps.generateHome).toHaveBeenCalledWith(
      expect.objectContaining({ logoSvg: 'data:logo' }),
      expect.any(Object)
    );
  });

  it('terminalizes a source-image HTTP 408 through FailOgGeneration', async () => {
    const deps = dependencies();
    deps.fetchImage.mockImplementation((url, usage) =>
      fetchImageAsDataUrl(
        url,
        usage,
        vi.fn(async () => new Response('', { status: 408 })),
        vi.fn()
      )
    );
    await expect(processorWith(deps)(GENERATION_ID)).resolves.toEqual({ state: 'failed' });
    expect(deps.fail).toHaveBeenCalledWith(
      GENERATION_ID,
      'lease-token',
      'image_http_408',
      'Source image https://cdn.test/featured.webp returned HTTP 408'
    );
    expect(deps.complete).not.toHaveBeenCalled();
  });

  it('rejects the delivery if recording a known failure is temporarily unavailable', async () => {
    const deps = dependencies();
    deps.generateContent.mockRejectedValueOnce(new Error('deterministic render'));
    deps.fail.mockRejectedValueOnce(new ConnectError('backend down', Code.Unavailable));
    await expect(processorWith(deps)(GENERATION_ID)).rejects.toBeInstanceOf(RequeueMessageError);
    expect(mocks.logger.error).toHaveBeenCalledWith(
      expect.objectContaining({ generationId: GENERATION_ID }),
      'Failed to record OG generation result; queue delivery will be retried'
    );
  });

  it('also retries the same generation if recording a transient failure is commit-uncertain', async () => {
    const deps = dependencies();
    deps.fetchImage.mockRejectedValueOnce(new TransientGenerationError('timeout', 'image_fetch'));
    deps.fail.mockRejectedValueOnce(new ConnectError('backend down', Code.Unavailable));
    await expect(processorWith(deps)(GENERATION_ID)).rejects.toBeInstanceOf(RequeueMessageError);
  });

  it('does not retry a deterministically rejected failure record', async () => {
    const deps = dependencies();
    deps.generateContent.mockRejectedValueOnce(new Error('deterministic render'));
    deps.fail.mockRejectedValueOnce(new ConnectError('bad lease', Code.InvalidArgument));

    await expect(processorWith(deps)(GENERATION_ID)).rejects.toMatchObject({
      code: Code.InvalidArgument,
    });
  });

  it('retries completion with bounded delays and succeeds', async () => {
    const deps = dependencies();
    deps.complete
      .mockRejectedValueOnce(new ConnectError('one', Code.Unavailable))
      .mockRejectedValueOnce(new ConnectError('two', Code.DeadlineExceeded))
      .mockResolvedValueOnce({ status: OgGenerationStatus.READY });
    await expect(processorWith(deps)(GENERATION_ID)).resolves.toMatchObject({ state: 'completed' });
    expect(deps.sleep.mock.calls).toEqual([[250], [1_000]]);
    expect(deps.complete).toHaveBeenCalledTimes(3);
  });

  it('requeues the same generation after completion commit uncertainty', async () => {
    const deps = dependencies();
    deps.complete.mockRejectedValue(new ConnectError('down', Code.Unavailable));
    await expect(processorWith(deps)(GENERATION_ID)).rejects.toBeInstanceOf(RequeueMessageError);
    expect(deps.fail).not.toHaveBeenCalled();
    expect(mocks.logger.info).not.toHaveBeenCalledWith(
      expect.objectContaining({ event: 'job.succeeded' }),
      'System event'
    );
  });

  it('records a deterministic completion rejection as failed', async () => {
    const deps = dependencies();
    deps.complete.mockRejectedValueOnce(new ConnectError('bad lease', Code.InvalidArgument));
    await expect(processorWith(deps)(GENERATION_ID)).resolves.toEqual({ state: 'failed' });
    expect(deps.fail).toHaveBeenCalledWith(
      GENERATION_ID,
      'lease-token',
      'completion_rejected',
      'Backend rejected OG completion: [invalid_argument] bad lease'
    );
    expect(mocks.logger.error).toHaveBeenCalledWith(
      expect.objectContaining({ event: 'job.failed', reason: 'completion_rejected' }),
      'System event'
    );
  });

  it('records a permanent source rejection with the bounded source reason', async () => {
    const deps = dependencies();
    deps.fetchImage.mockRejectedValueOnce(
      new PermanentGenerationError('missing source', 'image_http_404')
    );

    await expect(processorWith(deps)(GENERATION_ID)).resolves.toEqual({ state: 'failed' });

    expect(mocks.logger.error).toHaveBeenCalledWith(
      expect.objectContaining({ event: 'job.failed', reason: 'source_rejected' }),
      'System event'
    );
  });

  it('records a deterministic rejection on the final completion attempt', async () => {
    const deps = dependencies();
    deps.complete
      .mockRejectedValueOnce(new ConnectError('one', Code.Unavailable))
      .mockRejectedValueOnce(new ConnectError('two', Code.Unavailable))
      .mockRejectedValueOnce(new ConnectError('bad lease', Code.InvalidArgument));
    await expect(processorWith(deps)(GENERATION_ID)).resolves.toEqual({ state: 'failed' });
    expect(deps.sleep).toHaveBeenCalledTimes(2);
  });

  it('uses no image fetch when the claimed snapshot has no asset URLs', async () => {
    const noAssets = claimed({
      featuredImage: undefined,
      renderConfig: { ...claimed().renderConfig, logoAsset: undefined },
    });
    const deps = dependencies(noAssets);
    await processorWith(deps)(GENERATION_ID);
    expect(deps.fetchImage).not.toHaveBeenCalled();
  });
});
