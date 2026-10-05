import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { parse } from "yaml";

const composePath = new URL(
  "../../compose/asset-optimizer.yml",
  import.meta.url,
);

test("keeps the Geul asset optimizer Compose runtime boundary scoped", async () => {
  const compose = parse(await readFile(composePath, "utf8"));
  const services = compose?.services;

  assert.deepEqual(Object.keys(services ?? {}), ["geul-asset-optimizer-prod"]);

  const service = services["geul-asset-optimizer-prod"];
  assert.equal(
    service.image,
    "${GEUL_ASSET_OPTIMIZER_IMAGE:?set GEUL_ASSET_OPTIMIZER_IMAGE to a full image reference}",
  );
  assert.equal(service.restart, "unless-stopped");
  assert.deepEqual(service.networks, { default: {}, runtime: {} });
  assert.equal(compose.networks?.runtime?.external, true);
});
