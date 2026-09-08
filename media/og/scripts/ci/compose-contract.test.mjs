import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

test("maps the canonical token signing secret into the Geul OG container", async () => {
  const compose = await readFile(
    new URL("../../compose/og.yml", import.meta.url),
    "utf8",
  );

  assert.match(
    compose,
    /TOKEN_SIGNING_SECRET: \$\{GEUL_BACKEND_TOKEN_SIGNING_SECRET:\?set GEUL_BACKEND_TOKEN_SIGNING_SECRET\}/,
  );
  assert.match(compose, /name: geul-og/);
  assert.match(compose, /image: \$\{GEUL_OG_IMAGE:\?set GEUL_OG_IMAGE to a full image reference\}/);
});
