import { describe, expect, it, vi } from 'vitest';
import { createHealthRoutes } from './health.js';

describe('GET /health', () => {
  it('returns ok status with the current timestamp', async () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date('2026-06-25T00:00:00Z'));

    const response = await createHealthRoutes(async () => true).request('/health');

    expect(response.status).toBe(200);
    expect(await response.json()).toEqual({
      status: 'ok',
      postgresql: true,
      timestamp: '2026-06-25T00:00:00.000Z',
    });
    vi.useRealTimers();
  });

  it('returns unavailable when PostgreSQL cannot answer the health query', async () => {
    const response = await createHealthRoutes(async () => false).request('/health');

    expect(response.status).toBe(503);
    expect(await response.json()).toMatchObject({
      status: 'degraded',
      postgresql: false,
    });
  });
});
