import { OgGenerationStatus } from '@echovisionlab/geul-event';
import { vi } from 'vitest';
import { claimed, written } from './claim.test-fixture.js';
import { createOgProcessor } from './processor.js';

export { GENERATION_ID, claimed, target, written } from './claim.test-fixture.js';

export function dependencies(claim = claimed()) {
  return {
    claim: vi.fn(async () => claim),
    fetchImage: vi.fn(
      async (_url: string, usage: 'featured' | 'logo' | 'label-logo') =>
        `data:${usage}`
    ),
    generateContent: vi.fn(async () => Buffer.from('webp')),
    generateHome: vi.fn(async () => Buffer.from('home')),
    generateLabel: vi.fn(async () => Buffer.from('label')),
    writeAsset: vi.fn(async () => written),
    complete: vi.fn(async () => ({ status: OgGenerationStatus.READY })),
    fail: vi.fn(async () => ({ status: OgGenerationStatus.FAILED })),
    sleep: vi.fn(async () => undefined),
  };
}

export function processorWith(deps: ReturnType<typeof dependencies>) {
  return createOgProcessor(
    deps as unknown as Parameters<typeof createOgProcessor>[0]
  );
}
