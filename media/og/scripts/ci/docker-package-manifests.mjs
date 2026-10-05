import { readFile, writeFile } from 'node:fs/promises';
import { pathToFileURL } from 'node:url';

const DEPENDENCY_FIELDS = [
  'name',
  'private',
  'type',
  'packageManager',
  'engines',
  'devEngines',
  'os',
  'cpu',
  'libc',
  'config',
  'pnpm',
  'dependencies',
  'devDependencies',
  'optionalDependencies',
  'peerDependencies',
  'peerDependenciesMeta',
  'dependenciesMeta',
  'bundledDependencies',
  'bundleDependencies',
];

const INSTALL_LIFECYCLE_SCRIPTS = ['preinstall', 'install', 'postinstall', 'prepare'];

export function createDependencyManifest(packageJson) {
  const manifest = {};
  for (const field of DEPENDENCY_FIELDS) {
    if (Object.hasOwn(packageJson, field)) {
      manifest[field] = packageJson[field];
    }
  }

  const lifecycleScripts = Object.fromEntries(
    INSTALL_LIFECYCLE_SCRIPTS.filter((name) => packageJson.scripts?.[name]).map((name) => [name, packageJson.scripts[name]]),
  );
  if (Object.keys(lifecycleScripts).length > 0) {
    manifest.version = packageJson.version;
    manifest.scripts = lifecycleScripts;
  }

  return manifest;
}

export function createBuildManifest(packageJson) {
  const manifest = structuredClone(packageJson);
  delete manifest.version;
  return manifest;
}

async function main() {
  const [sourcePath, dependencyPath, buildPath] = process.argv.slice(2);
  if (!sourcePath || !dependencyPath || !buildPath) {
    throw new Error('usage: docker-package-manifests.mjs <source> <dependency-output> <build-output>');
  }

  const packageJson = JSON.parse(await readFile(sourcePath, 'utf8'));
  await Promise.all([
    writeFile(dependencyPath, `${JSON.stringify(createDependencyManifest(packageJson), null, 2)}\n`),
    writeFile(buildPath, `${JSON.stringify(createBuildManifest(packageJson), null, 2)}\n`),
  ]);
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  await main();
}
