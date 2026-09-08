import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const mocks = vi.hoisted(() => ({
  startRuntime: vi.fn(),
  emitServiceFailed: vi.fn(),
}));

vi.mock('./telemetry.js', () => ({}));
vi.mock('./runtime.js', () => ({ startRuntime: mocks.startRuntime }));
vi.mock('./system_logging.js', () => ({ emitServiceFailed: mocks.emitServiceFailed }));

describe('service entrypoint', () => {
  const originalExit = process.exit;

  beforeEach(() => {
    vi.resetModules();
    vi.clearAllMocks();
  });

  afterEach(() => {
    process.exit = originalExit;
  });

  it('starts the runtime through main', async () => {
    mocks.startRuntime.mockResolvedValue({ shutdown: vi.fn() });
    const module = await import('./index.js');
    await module.main();

    expect(mocks.startRuntime).toHaveBeenCalledTimes(2);
  });

  it('logs startup failure and exits nonzero', async () => {
    const error = new Error('startup failed');
    mocks.startRuntime.mockRejectedValue(error);
    const exit = vi.fn();
    process.exit = exit as never;

    await import('./index.js');
    await new Promise((resolve) => setTimeout(resolve, 0));

    expect(mocks.emitServiceFailed).toHaveBeenCalledWith(error);
    expect(exit).toHaveBeenCalledWith(1);
  });
});
