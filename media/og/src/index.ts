import './telemetry.js';

import { startRuntime } from './runtime.js';
import { emitServiceFailed } from './system_logging.js';

export async function main(): Promise<void> {
  await startRuntime();
}

void main().catch((err: unknown) => {
  emitServiceFailed(err);
  process.exit(1);
});
