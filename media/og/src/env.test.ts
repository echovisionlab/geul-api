import { afterEach, describe, expect, it, vi } from 'vitest';

const ORIGINAL_ENV = process.env;

function setRequiredEnv(overrides: NodeJS.ProcessEnv = {}) {
  process.env = {
    DATABASE_DSN: 'postgres://og@postgres/geul',
    S3_ENDPOINT: 'http://minio:9000',
    S3_MEDIA_BUCKET: 'media',
    S3_ACCESS_KEY_ID: 'access',
    S3_SECRET_ACCESS_KEY: 'secret',
    BACKEND_URL: 'http://backend:8080',
    TOKEN_SIGNING_SECRET: 'test-only-token-signing-secret',
    ...overrides,
  };
}

async function importEnv() {
  vi.resetModules();
  return import('./env.js');
}

describe('env', () => {
  afterEach(() => {
    process.env = ORIGINAL_ENV;
    vi.unstubAllEnvs();
  });

  it('loads required values and defaults optional host, port, region, worker count, and shutdown timeout', async () => {
    setRequiredEnv();

    const { env } = await importEnv();

    expect(env).toMatchObject({
      HOST: '0.0.0.0',
      PORT: 3010,
      OG_GENERATE_WORKERS: 12,
      OG_SHUTDOWN_TIMEOUT_MS: 120_000,
      DATABASE_DSN: 'postgres://og@postgres/geul',
      S3_REGION: 'us-east-1',
      TOKEN_SIGNING_SECRET: 'test-only-token-signing-secret',
    });
  });

  it('parses integer overrides', async () => {
    setRequiredEnv({
      HOST: '127.0.0.1',
      PORT: '4010',
      OG_GENERATE_WORKERS: '4',
      OG_SHUTDOWN_TIMEOUT_MS: '9000',
    });

    const { env } = await importEnv();

    expect(env.PORT).toBe(4010);
    expect(env.OG_GENERATE_WORKERS).toBe(4);
    expect(env.OG_SHUTDOWN_TIMEOUT_MS).toBe(9000);
  });

  it('throws when a required value is missing or an integer is invalid', async () => {
    setRequiredEnv({ DATABASE_DSN: '' });
    await expect(importEnv()).rejects.toThrow(
      'Missing required environment variable: DATABASE_DSN'
    );

    setRequiredEnv({ PORT: 'not-a-number' });
    await expect(importEnv()).rejects.toThrow('Invalid integer value for PORT: not-a-number');

    setRequiredEnv({ OG_SHUTDOWN_TIMEOUT_MS: '0' });
    await expect(importEnv()).rejects.toThrow(
      'Invalid positive integer value for OG_SHUTDOWN_TIMEOUT_MS: 0'
    );

    setRequiredEnv({
      TOKEN_SIGNING_SECRET: '',
    });
    await expect(importEnv()).rejects.toThrow(
      'Missing required environment variable: TOKEN_SIGNING_SECRET'
    );
  });
});
