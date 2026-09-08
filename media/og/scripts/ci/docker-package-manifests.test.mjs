import assert from 'node:assert/strict';
import test from 'node:test';

import { createBuildManifest, createDependencyManifest } from './docker-package-manifests.mjs';

test('Docker dependency manifest ignores release and build-only metadata', () => {
  const source = {
    name: '@echovisionlab/geul-og',
    version: '0.1.0',
    private: true,
    type: 'module',
    scripts: { build: 'tsup', test: 'vitest run' },
    dependencies: { sharp: '0.35.2' },
    devDependencies: { tsup: '8.5.1' },
    repository: { type: 'git', url: 'https://example.test/repository.git' },
  };

  assert.deepEqual(createDependencyManifest(source), {
    name: '@echovisionlab/geul-og',
    private: true,
    type: 'module',
    dependencies: { sharp: '0.35.2' },
    devDependencies: { tsup: '8.5.1' },
  });
});

test('Docker dependency manifest preserves install lifecycle semantics', () => {
  const source = {
    name: '@echovisionlab/geul-og',
    version: '0.1.0',
    scripts: { preinstall: 'node preinstall.mjs', build: 'tsup' },
  };

  assert.deepEqual(createDependencyManifest(source), {
    name: '@echovisionlab/geul-og',
    version: '0.1.0',
    scripts: { preinstall: 'node preinstall.mjs' },
  });
});

test('Docker build manifest excludes only the release version', () => {
  const source = {
    name: '@echovisionlab/geul-og',
    version: '0.1.0',
    scripts: { build: 'tsup' },
    dependencies: { sharp: '0.35.2' },
  };

  assert.deepEqual(createBuildManifest(source), {
    name: '@echovisionlab/geul-og',
    scripts: { build: 'tsup' },
    dependencies: { sharp: '0.35.2' },
  });
  assert.equal(source.version, '0.1.0');
});
