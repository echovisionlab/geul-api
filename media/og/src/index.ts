import './telemetry.js';

import { emitServiceFailed } from './system_logging.js';

export async function main(): Promise<void> {
  // Import the runtime only after telemetry registers its ESM loader hook.
  const { startRuntime } = await import('./runtime.js');
  await startRuntime();
}

void main().catch((err: unknown) => {
  emitServiceFailed(err);
  process.exit(1);
});
