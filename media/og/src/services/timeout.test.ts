import { afterEach, describe, expect, it, vi } from 'vitest';
import { sleep } from './timeout.js';

describe('timeout utilities', () => {
  afterEach(() => vi.useRealTimers());

  it('resolves the bounded sleep after its delay', async () => {
    vi.useFakeTimers();
    const operation = sleep(25);

    await vi.advanceTimersByTimeAsync(25);

    await expect(operation).resolves.toBeUndefined();
  });
});
