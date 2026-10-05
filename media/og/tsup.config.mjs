import { defineConfig } from 'tsup';

export default defineConfig({
  entry: ['src/index.ts'],
  format: ['esm'],
  clean: true,
  noExternal: [
    '@echovisionlab/geul-event',
    '@echovisionlab/geul-proto',
    '@echovisionlab/geul-telemetry',
  ],
  external: [
    '@aws-sdk/client-s3',
    '@bufbuild/protobuf',
    '@hono/node-server',
    'pg',
    'hono',
    'pino',
    'satori',
    'sharp',
  ],
});
