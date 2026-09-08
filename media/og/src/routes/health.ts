import { Hono } from 'hono';

async function checkPGMQHealth(): Promise<boolean> {
  const queue = await import('../services/queue.js');
  return queue.checkPGMQHealth();
}

export function createHealthRoutes(
  checkPostgreSQL: () => Promise<boolean> = checkPGMQHealth
): Hono {
  const app = new Hono();

  app.get('/health', async (c) => {
    const postgresql = await checkPostgreSQL();
    const body = {
      status: postgresql ? 'ok' : 'degraded',
      postgresql,
      timestamp: new Date().toISOString(),
    };
    return postgresql ? c.json(body, 200) : c.json(body, 503);
  });

  return app;
}

export default createHealthRoutes();
