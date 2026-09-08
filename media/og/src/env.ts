import type { Env } from './types/config.js';

function getEnvVar(name: string, defaultValue?: string): string {
  const value = process.env[name] ?? defaultValue;
  if (!value) {
    throw new Error(`Missing required environment variable: ${name}`);
  }
  return value;
}

function getEnvVarInt(name: string, defaultValue: number): number {
  const value = process.env[name];
  if (!value) {
    return defaultValue;
  }
  const parsed = parseInt(value, 10);
  if (isNaN(parsed)) {
    throw new Error(`Invalid integer value for ${name}: ${value}`);
  }
  return parsed;
}

function getPositiveEnvVarInt(name: string, defaultValue: number): number {
  const value = getEnvVarInt(name, defaultValue);
  if (value <= 0) {
    throw new Error(`Invalid positive integer value for ${name}: ${value}`);
  }
  return value;
}

export const env: Env = {
  HOST: getEnvVar('HOST', '0.0.0.0'),
  PORT: getEnvVarInt('PORT', 3010),
  OG_GENERATE_WORKERS: getEnvVarInt('OG_GENERATE_WORKERS', 12),
  OG_SHUTDOWN_TIMEOUT_MS: getPositiveEnvVarInt('OG_SHUTDOWN_TIMEOUT_MS', 120_000),
  DATABASE_DSN: getEnvVar('DATABASE_DSN'),
  S3_ENDPOINT: getEnvVar('S3_ENDPOINT'),
  S3_MEDIA_BUCKET: getEnvVar('S3_MEDIA_BUCKET'),
  S3_REGION: getEnvVar('S3_REGION', 'us-east-1'),
  S3_ACCESS_KEY_ID: getEnvVar('S3_ACCESS_KEY_ID'),
  S3_SECRET_ACCESS_KEY: getEnvVar('S3_SECRET_ACCESS_KEY'),
  BACKEND_URL: getEnvVar('BACKEND_URL'),
  TOKEN_SIGNING_SECRET: getEnvVar('TOKEN_SIGNING_SECRET'),
};
