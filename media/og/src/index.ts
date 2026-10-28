import { shutdownTelemetry } from './telemetry.js';

import { emitServiceFailed } from './system_logging.js';

export async function main(): Promise<void> {
  // Import the runtime only after telemetry registers its ESM loader hook.
  const { startRuntime } = await import('./runtime.js');
  await startRuntime();
}

void main().catch(async (err: unknown) => {
  emitServiceFailed(err);
  await shutdownTelemetry();
  process.exit(1);
});
