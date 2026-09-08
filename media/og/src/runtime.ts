import { serve } from '@hono/node-server';
import { Hono } from 'hono';
import { cors } from 'hono/cors';
import { env } from './env.js';
import healthRoutes from './routes/health.js';
import { closePGMQ, startConsumer } from './services/queue.js';
import { processOgGenerate } from './services/processor.js';
import {
  emitServiceFailed,
  emitServiceReady,
  emitServiceStopping,
} from './system_logging.js';

interface RuntimeDependencies {
  serveHttp: typeof serve;
  startGenerationConsumer: typeof startConsumer;
  closeQueue: typeof closePGMQ;
  processEvent: typeof processOgGenerate;
  registerSignal: (
    signal: 'SIGTERM' | 'SIGINT',
    listener: () => void | Promise<void>
  ) => unknown;
  exit: (code: number) => void;
}

type RuntimeServer = ReturnType<typeof serve>;

const defaultDependencies: RuntimeDependencies = {
  serveHttp: serve,
  startGenerationConsumer: startConsumer,
  closeQueue: closePGMQ,
  processEvent: processOgGenerate,
  registerSignal: (signal, listener) => process.on(signal, listener),
  exit: (code) => process.exit(code),
};

function closeHttpServer(server: RuntimeServer): Promise<void> {
  return new Promise((resolve, reject) => {
    server.close((error?: Error) => {
      if (error) {
        reject(error);
        return;
      }
      resolve();
    });
  });
}

export function createApp(): Hono {
  const app = new Hono();
  app.use('*', cors());
  app.route('/', healthRoutes);
  app.onError((_error, context) => {
    return context.json({ error: 'Internal server error' }, 500);
  });
  return app;
}

export async function startRuntime(
  overrides: Partial<RuntimeDependencies> = {}
): Promise<{ shutdown: () => Promise<void> }> {
  const dependencies = { ...defaultDependencies, ...overrides };

  await dependencies.startGenerationConsumer(dependencies.processEvent);

  const app = createApp();
  const server = dependencies.serveHttp(
    {
      fetch: app.fetch,
      hostname: env.HOST,
      port: env.PORT,
    },
    () => emitServiceReady()
  );

  let shutdownPromise: Promise<void> | undefined;
  const shutdown = (): Promise<void> => {
    shutdownPromise ??= (async () => {
      emitServiceStopping();
      try {
        const results = await Promise.allSettled([
          closeHttpServer(server),
          dependencies.closeQueue(),
        ]);
        const failure = results.find(
          (result): result is PromiseRejectedResult => result.status === 'rejected'
        );
        if (failure) {
          throw failure.reason;
        }
        dependencies.exit(0);
      } catch (error) {
        emitServiceFailed(error);
        dependencies.exit(1);
      }
    })();
    return shutdownPromise;
  };

  dependencies.registerSignal('SIGTERM', shutdown);
  dependencies.registerSignal('SIGINT', shutdown);
  return { shutdown };
}
