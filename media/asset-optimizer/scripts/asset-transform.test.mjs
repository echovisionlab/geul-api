import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { mkdtemp, rm } from "node:fs/promises";
import { join } from "node:path";
import { promisify } from "node:util";
import test from "node:test";
import { Document } from "@gltf-transform/core";
import { draco, inspect, meshopt } from "@gltf-transform/functions";
import { MeshoptEncoder } from "meshoptimizer";
import sharp from "sharp";
import {
  inspectCSV,
  optimize,
  parseTransformArgs,
} from "./asset-transform.mjs";
import { createOptimizationFixture } from "./fixtures/asset-transform-fixture.mjs";
import { createIO } from "./optimize-particle-mesh.mjs";

const run = promisify(execFile);
const scriptPath = new URL("./asset-transform.mjs", import.meta.url).pathname;

test("accepts the Go command contract and rejects unsupported profiles or options", () => {
  assert.deepEqual(
    parseTransformArgs(["inspect", "input.glb", "--format", "csv"]),
    { command: "inspect", inputPath: "input.glb" },
  );
  const base = [
    "optimize",
    "input.glb",
    "output.glb",
    "--compress",
    "draco",
    "--texture-compress",
    "webp",
    "--texture-size",
    "1024",
  ];
  assert.equal(
    parseTransformArgs([...base, "--simplify", "false"]).simplify,
    false,
  );
  assert.equal(
    parseTransformArgs([
      ...base,
      "--simplify-ratio",
      "0.50",
      "--simplify-error",
      "0.001",
    ]).simplifyRatio,
    0.5,
  );
  assert.throws(
    () =>
      parseTransformArgs([
        ...base,
        "--simplify-ratio",
        "0",
        "--simplify-error",
        "0.001",
      ]),
    /simplify-ratio/,
  );
  assert.throws(
    () =>
      parseTransformArgs([
        ...base,
        "--simplify",
        "false",
        "--simplify-ratio",
        "0.5",
      ]),
    /cannot be combined/,
  );
  assert.throws(
    () => parseTransformArgs([...base, "--simplify", "false", "--slots", "*"]),
    /Unknown option/,
  );
  assert.throws(
    () => parseTransformArgs(["inspect", "input.glb", "--format", "pretty"]),
    /--format csv/,
  );
});

test("inspect prints the original overview and official SDK reports as quoted CSV", async (t) => {
  const paths = await fixturePaths(t);
  const io = await createIO();
  await io.write(paths.input, await createOptimizationFixture());
  const csv = await inspectCSV(paths.input);
  assert.match(csv, /version,2\.0/);
  assert.match(csv, /uploadVertexCount/);
  assert.match(csv, /meshPrimitives,glPrimitives,vertices/);
  assert.match(csv, /"Scene, ""one"""/);
  assert.match(csv, /"Texture, ""wide"""/);
  assert.match(csv, /xmp:ModifyDate,2000-01-01/);
  const { stdout, stderr } = await run(process.execPath, [
    scriptPath,
    "inspect",
    paths.input,
    "--format",
    "csv",
  ]);
  assert.equal(stdout, csv);
  assert.equal(stderr, "");
  await io.write(paths.input, new Document());
  assert.match(await inspectCSV(paths.input), /No meshes found\./);
});

test("Draco/WebP profile preserves animation and updates existing metadata", async (t) => {
  const paths = await fixturePaths(t);
  const io = await createIO();
  await io.write(paths.input, await createOptimizationFixture());
  await optimize(paths.input, paths.output, { simplify: false });
  const output = await io.read(paths.output);
  const root = output.getRoot();
  assert.ok(output.hasExtension("KHR_draco_mesh_compression"));
  assert.equal(root.listScenes().length, 1);
  assert.equal(root.listAnimations().length, 1);
  assert.equal(
    root.listAnimations()[0].listSamplers()[0].getInput().getCount(),
    2,
  );
  assert.deepEqual(
    root.listAnimations()[0].listSamplers()[0].getOutput().getArray(),
    new Float32Array([0, 0, 0, 4, 0, 0]),
  );
  const metadata = root.getExtension("KHR_xmp_json_ld");
  assert.equal(
    metadata.getProperty("xmp:ModifyDate"),
    new Date().toISOString().slice(0, 10),
  );
  assert.equal(
    metadata.getProperty("xmp:MetadataDate"),
    new Date().toISOString().slice(0, 10),
  );
  assert.equal(root.listBuffers().length, 1);
  for (const texture of root.listTextures()) {
    assert.equal(texture.getMimeType(), "image/webp");
    const dimensions = await sharp(texture.getImage()).metadata();
    assert.equal(dimensions.width, 1024);
    assert.ok(dimensions.height <= 1024);
  }
});

test("static repeated meshes become GPU instances", async (t) => {
  const paths = await fixturePaths(t);
  const io = await createIO();
  const source = await createOptimizationFixture();
  for (const animation of source.getRoot().listAnimations())
    animation.dispose();
  await io.write(paths.input, source);
  await optimize(paths.input, paths.output, { simplify: false });
  const output = await io.read(paths.output);
  assert.ok(output.hasExtension("EXT_mesh_gpu_instancing"));
  assert.equal(output.getRoot().listAnimations().length, 0);
  assert.ok(output.hasExtension("KHR_draco_mesh_compression"));
});

test("recompresses existing Draco and meshopt inputs and respects simplification", async (t) => {
  const paths = await fixturePaths(t);
  const io = await createIO();
  for (const compression of [
    draco(),
    meshopt({ encoder: MeshoptEncoder, level: "high" }),
  ]) {
    const source = await createOptimizationFixture();
    await source.transform(compression);
    await io.write(paths.input, source);
    await optimize(paths.input, paths.output, {
      simplify: true,
      simplifyRatio: 0.5,
      simplifyError: 0.001,
    });
    const output = await io.read(paths.output);
    assert.ok(output.hasExtension("KHR_draco_mesh_compression"));
    assert.equal(output.hasExtension("EXT_meshopt_compression"), false);
    const report = inspect(output);
    assert.ok(report.meshes.properties[0].glPrimitives < 512);
    assert.equal(report.animations.properties.length, 1);
  }
});

test("palette profile merges solid materials and handles real executable failures", async (t) => {
  const paths = await fixturePaths(t);
  const io = await createIO();
  await io.write(
    paths.input,
    await createOptimizationFixture({ palette: true }),
  );
  await optimize(paths.input, paths.output, { simplify: false });
  const root = (await io.read(paths.output)).getRoot();
  assert.ok(root.listMaterials().length < 7);
  assert.ok(root.listTextures().length >= 2);
  await assert.rejects(
    run(process.execPath, [
      scriptPath,
      "inspect",
      join(paths.directory, "missing.glb"),
      "--format",
      "csv",
    ]),
    (error) => error.code === 1 && /ENOENT/.test(error.stderr),
  );
});

async function fixturePaths(t) {
  const directory = await mkdtemp(
    new URL("../node_modules/.asset-transform-test-", import.meta.url),
  );
  t.after(() => rm(directory, { recursive: true, force: true }));
  return {
    directory,
    input: join(directory, "input.glb"),
    output: join(directory, "output.glb"),
  };
}
