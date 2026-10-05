import { PgmqClient, Queues } from '@echovisionlab/geul-event';
import { Pool } from 'pg';
import { env } from '../env.js';
import { logger } from '../logger.js';
import { emitPostgresDegraded } from '../system_logging.js';
import {
  handleDelivery,
  OG_VISIBILITY_TIMEOUT_SECONDS,
  type GenerationHandler,
} from './queue_delivery.js';

const POLL_INTERVAL_MS = 250;
const POSTGRES_CONNECTION_TIMEOUT_MS = 2_000;
const POSTGRES_QUERY_TIMEOUT_MS = 2_000;

let pool: Pool | null = null;
let pollPromise: Promise<void> | null = null;
let shuttingDown = false;
let closePromise: Promise<void> | null = null;
let wakePoll: (() => void) | null = null;
const inFlightDeliveries = new Set<Promise<void>>();

function getPool(): Pool {
  if (pool) return pool;
  const created = new Pool({
    connectionString: env.DATABASE_DSN,
    max: Math.max(2, env.OG_GENERATE_WORKERS + 1),
    application_name: 'geul-og',
    connectionTimeoutMillis: POSTGRES_CONNECTION_TIMEOUT_MS,
    query_timeout: POSTGRES_QUERY_TIMEOUT_MS,
    statement_timeout: POSTGRES_QUERY_TIMEOUT_MS,
  });
  created.on('error', (error) => {
    if (shuttingDown) return;
    emitPostgresDegraded('pool', error);
    logger.error({ error }, 'PostgreSQL OG pool error');
  });
  pool = created;
  return pool;
}

export async function checkPGMQHealth(): Promise<boolean> {
  if (!pool || shuttingDown) return false;
  try {
    await pool.query('SELECT 1');
    return true;
  } catch {
    return false;
  }
}

function waitForPoll(): Promise<void> {
  return new Promise((resolve) => {
    const timeout = setTimeout(() => {
      wakePoll = null;
      resolve();
    }, POLL_INTERVAL_MS);
    wakePoll = () => {
      clearTimeout(timeout);
      wakePoll = null;
      resolve();
    };
  });
}

async function poll(handler: GenerationHandler): Promise<void> {
  const client = new PgmqClient();
  const database = getPool();
  while (!shuttingDown) {
    try {
      const available = Math.max(0, env.OG_GENERATE_WORKERS - inFlightDeliveries.size);
      if (available === 0) {
        await waitForPoll();
        continue;
      }
      const messages = await client.read(database, Queues.ogGenerate, {
        visibilityTimeoutSeconds: OG_VISIBILITY_TIMEOUT_SECONDS,
        batch: available,
      });
      if (messages.length === 0) {
        await waitForPoll();
        continue;
      }
      for (const message of messages) {
        const delivery = handleDelivery(database, client, message, handler).catch(
          (error: unknown) => {
            emitPostgresDegraded('delivery', error);
            logger.error(
              { error, messageId: String(message.transportId) },
              'PGMQ OG delivery handoff failed'
            );
          }
        );
        inFlightDeliveries.add(delivery);
        void delivery.finally(() => inFlightDeliveries.delete(delivery));
      }
    } catch (error) {
      if (!shuttingDown) {
        emitPostgresDegraded('read', error);
        logger.error({ error }, 'PGMQ OG consumer failed');
        await waitForPoll();
      }
    }
  }
}

async function waitForPromise(promise: Promise<unknown>, timeoutMs: number): Promise<boolean> {
  if (timeoutMs <= 0) return false;
  let timeout: NodeJS.Timeout | undefined;
  try {
    return await Promise.race([
      promise.then(() => true),
      new Promise<false>((resolve) => {
        timeout = setTimeout(() => resolve(false), timeoutMs);
      }),
    ]);
  } finally {
    clearTimeout(timeout);
  }
}

export async function startConsumer(handler: GenerationHandler): Promise<void> {
  const database = getPool();
  await database.query('SELECT total_messages FROM pgmq.metrics($1)', [Queues.ogGenerate]);
  pollPromise ??= poll(handler);
}

async function waitForInFlightDeliveries(timeoutMs: number): Promise<boolean> {
  if (inFlightDeliveries.size === 0) return true;
  let timeout: NodeJS.Timeout | undefined;
  const timeoutPromise = new Promise<false>((resolve) => {
    timeout = setTimeout(() => resolve(false), timeoutMs);
  });
  try {
    return await Promise.race([
      Promise.allSettled([...inFlightDeliveries]).then(() => true),
      timeoutPromise,
    ]);
  } finally {
    clearTimeout(timeout);
  }
}

async function drainAndClosePGMQ(): Promise<void> {
  shuttingDown = true;
  wakePoll?.();
  const deadline = Date.now() + env.OG_SHUTDOWN_TIMEOUT_MS;
  const remaining = (): number => Math.max(0, deadline - Date.now());
  const pollStopped = await waitForPromise(pollPromise ?? Promise.resolve(), remaining());
  const drained = await waitForInFlightDeliveries(remaining());
  const activePool = pool;
  pool = null;
  const poolClosed = activePool
    ? await waitForPromise(activePool.end(), remaining())
    : true;
  if (!pollStopped || !drained || !poolClosed) {
    logger.warn(
      {
        inFlightCount: inFlightDeliveries.size,
        pollStopped,
        poolClosed,
        timeoutMs: env.OG_SHUTDOWN_TIMEOUT_MS,
      },
      'PGMQ OG shutdown timed out; unfinished messages return after visibility timeout'
    );
  }
}

export function closePGMQ(): Promise<void> {
  closePromise ??= drainAndClosePGMQ();
  return closePromise;
}
