import assert from "node:assert/strict";
import { mkdtemp, rm, stat } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { Accessor, Document, NodeIO } from "@gltf-transform/core";
import { KHRDracoMeshCompression } from "@gltf-transform/extensions";
import { draco } from "@gltf-transform/functions";
import draco3d from "draco3dgltf";
import {
  optimizeParticleMesh,
  parseParticleMeshArgs,
} from "./optimize-particle-mesh.mjs";

const PIXEL_PNG = Buffer.from(
  "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAusB9Y9ZQmcAAAAASUVORK5CYII=",
  "base64",
);

test("parseParticleMeshArgs accepts bounded simplification or an explicit skip", () => {
  assert.deepEqual(
    parseParticleMeshArgs([
      "input.glb",
      "output.glb",
      "--simplify-ratio",
      "0.50",
      "--simplify-error",
      "0.001",
    ]),
    {
      inputPath: "input.glb",
      outputPath: "output.glb",
      simplify: true,
      simplifyRatio: 0.5,
      simplifyError: 0.001,
    },
  );
  assert.deepEqual(
    parseParticleMeshArgs(["input.glb", "output.glb", "--simplify", "false"]),
    {
      inputPath: "input.glb",
      outputPath: "output.glb",
      simplify: false,
    },
  );
  assert.deepEqual(parseParticleMeshArgs(["--help"]), { help: true });

  assert.throws(
    () =>
      parseParticleMeshArgs([
        "input.glb",
        "output.glb",
        "--simplify-ratio",
        "0",
      ]),
    /simplify-ratio/,
  );
  assert.throws(
    () =>
      parseParticleMeshArgs(["input.glb", "output.glb", "--simplify", "true"]),
    /only accepts false/,
  );
  assert.throws(
    () =>
      parseParticleMeshArgs(["input.glb", "output.glb", "--unknown", "value"]),
    /unknown option/,
  );
});

test("particle profile preserves selectable mesh nodes and writes position-only Draco geometry", async (t) => {
  const paths = await fixturePaths(t);
  const source = createFixtureDocument();
  await new NodeIO().write(paths.input, source);

  await optimizeParticleMesh(paths.input, paths.output, { simplify: false });
  const [inputStat, outputStat] = await Promise.all([
    stat(paths.input),
    stat(paths.output),
  ]);
  assert.ok(
    outputStat.size < inputStat.size,
    `${outputStat.size} should be smaller than ${inputStat.size}`,
  );
  const output = await readDracoDocument(paths.output);
  const root = output.getRoot();

  assert.deepEqual(
    root
      .listNodes()
      .filter((node) => node.getMesh())
      .map((node) => node.getName()),
    ["Selectable Flower", "Selectable Stem"],
  );
  assert.deepEqual(
    root.listMeshes().map((mesh) => mesh.getName()),
    ["FlowerGeometry", "StemGeometry"],
  );
  assert.equal(root.listMaterials().length, 0);
  assert.equal(root.listTextures().length, 0);
  assert.equal(root.listAnimations().length, 0);
  assert.equal(root.listSkins().length, 0);

  for (const mesh of root.listMeshes()) {
    assert.deepEqual(mesh.getWeights(), []);
    for (const primitive of mesh.listPrimitives()) {
      assert.deepEqual(primitive.listSemantics(), ["POSITION"]);
      assert.ok(primitive.getIndices());
      assert.equal(primitive.getMaterial(), null);
      assert.equal(primitive.listTargets().length, 0);
    }
  }
  for (const node of root.listNodes()) {
    assert.equal(node.getSkin(), null);
    assert.deepEqual(node.getWeights(), []);
  }

  assert.deepEqual(
    root.listExtensionsRequired().map((extension) => extension.extensionName),
    ["KHR_draco_mesh_compression"],
  );
});

test("particle profile applies the requested simplification ratio after stripping split attributes", async (t) => {
  const paths = await fixturePaths(t);
  const source = createFixtureDocument();
  const originalIndexCount = totalIndexCount(source);
  await writeDracoDocument(paths.input, source);

  await optimizeParticleMesh(paths.input, paths.output, {
    simplify: true,
    simplifyRatio: 0.5,
    simplifyError: 0.001,
  });
  const output = await readDracoDocument(paths.output);

  assert.ok(totalIndexCount(output) < originalIndexCount);
});

async function fixturePaths(t) {
  const directory = await mkdtemp(join(tmpdir(), "asset-optimizer-particle-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  return {
    input: join(directory, "input.glb"),
    output: join(directory, "output.glb"),
  };
}

async function readDracoDocument(path) {
  const decoder = await draco3d.createDecoderModule();
  const io = new NodeIO()
    .registerExtensions([KHRDracoMeshCompression])
    .registerDependencies({ "draco3d.decoder": decoder });
  return io.read(path);
}

async function writeDracoDocument(path, document) {
  const encoder = await draco3d.createEncoderModule();
  const io = new NodeIO()
    .registerExtensions([KHRDracoMeshCompression])
    .registerDependencies({ "draco3d.encoder": encoder });
  await document.transform(draco());
  await io.write(path, document);
}

function createFixtureDocument() {
  const document = new Document();
  const buffer = document.createBuffer("fixture-buffer");
  const scene = document.createScene("ParticleScene");
  document.getRoot().setDefaultScene(scene);

  const texture = document
    .createTexture("EmbeddedTexture")
    .setImage(PIXEL_PNG)
    .setMimeType("image/png");
  const material = document
    .createMaterial("TexturedMaterial")
    .setBaseColorTexture(texture);
  const joint = document.createNode("FixtureJoint");
  scene.addChild(joint);
  const skin = document.createSkin("FixtureSkin").addJoint(joint);

  const flower = createGridMesh(document, buffer, material, "FlowerGeometry");
  const flowerNode = document
    .createNode("Selectable Flower")
    .setMesh(flower)
    .setSkin(skin)
    .setWeights([0.25]);
  const stem = createGridMesh(document, buffer, material, "StemGeometry");
  const stemNode = document
    .createNode("Selectable Stem")
    .setMesh(stem)
    .setTranslation([0, 0, 2]);
  scene.addChild(flowerNode).addChild(stemNode);

  const animationInput = createAccessor(
    document,
    buffer,
    "AnimationInput",
    Accessor.Type.SCALAR,
    new Float32Array([0, 1]),
  );
  const animationOutput = createAccessor(
    document,
    buffer,
    "AnimationOutput",
    Accessor.Type.VEC3,
    new Float32Array([0, 0, 0, 0, 1, 0]),
  );
  const sampler = document
    .createAnimationSampler("TranslationSampler")
    .setInput(animationInput)
    .setOutput(animationOutput);
  const channel = document
    .createAnimationChannel("TranslationChannel")
    .setTargetNode(flowerNode)
    .setTargetPath("translation")
    .setSampler(sampler);
  document
    .createAnimation("FixtureAnimation")
    .addSampler(sampler)
    .addChannel(channel);

  return document;
}

function createGridMesh(document, buffer, material, name) {
  const segments = 6;
  const width = segments + 1;
  const vertexCount = width * width;
  const positions = new Float32Array(vertexCount * 3);
  const normals = new Float32Array(vertexCount * 3);
  const tangents = new Float32Array(vertexCount * 4);
  const texcoords = new Float32Array(vertexCount * 2);
  const colors = new Float32Array(vertexCount * 3);
  const joints = new Uint16Array(vertexCount * 4);
  const weights = new Float32Array(vertexCount * 4);
  const morphPositions = new Float32Array(vertexCount * 3);

  for (let y = 0; y < width; y += 1) {
    for (let x = 0; x < width; x += 1) {
      const vertex = y * width + x;
      positions.set([x / segments - 0.5, y / segments - 0.5, 0], vertex * 3);
      normals.set([0, 0, 1], vertex * 3);
      tangents.set([1, 0, 0, 1], vertex * 4);
      texcoords.set([x / segments, y / segments], vertex * 2);
      colors.set([1, x / segments, y / segments], vertex * 3);
      joints.set([0, 0, 0, 0], vertex * 4);
      weights.set([1, 0, 0, 0], vertex * 4);
      morphPositions.set([0, 0, 0.05], vertex * 3);
    }
  }

  const indices = new Uint16Array(segments * segments * 6);
  let index = 0;
  for (let y = 0; y < segments; y += 1) {
    for (let x = 0; x < segments; x += 1) {
      const topLeft = y * width + x;
      const topRight = topLeft + 1;
      const bottomLeft = topLeft + width;
      const bottomRight = bottomLeft + 1;
      indices.set(
        [topLeft, bottomLeft, topRight, topRight, bottomLeft, bottomRight],
        index,
      );
      index += 6;
    }
  }

  const primitive = document
    .createPrimitive(`${name}Primitive`)
    .setIndices(
      createAccessor(
        document,
        buffer,
        `${name}Indices`,
        Accessor.Type.SCALAR,
        indices,
      ),
    )
    .setAttribute(
      "POSITION",
      createAccessor(
        document,
        buffer,
        `${name}Position`,
        Accessor.Type.VEC3,
        positions,
      ),
    )
    .setAttribute(
      "NORMAL",
      createAccessor(
        document,
        buffer,
        `${name}Normal`,
        Accessor.Type.VEC3,
        normals,
      ),
    )
    .setAttribute(
      "TANGENT",
      createAccessor(
        document,
        buffer,
        `${name}Tangent`,
        Accessor.Type.VEC4,
        tangents,
      ),
    )
    .setAttribute(
      "TEXCOORD_0",
      createAccessor(
        document,
        buffer,
        `${name}Texcoord`,
        Accessor.Type.VEC2,
        texcoords,
      ),
    )
    .setAttribute(
      "COLOR_0",
      createAccessor(
        document,
        buffer,
        `${name}Color`,
        Accessor.Type.VEC3,
        colors,
      ),
    )
    .setAttribute(
      "JOINTS_0",
      createAccessor(
        document,
        buffer,
        `${name}Joints`,
        Accessor.Type.VEC4,
        joints,
      ),
    )
    .setAttribute(
      "WEIGHTS_0",
      createAccessor(
        document,
        buffer,
        `${name}Weights`,
        Accessor.Type.VEC4,
        weights,
      ),
    )
    .setMaterial(material);
  const target = document
    .createPrimitiveTarget(`${name}Morph`)
    .setAttribute(
      "POSITION",
      createAccessor(
        document,
        buffer,
        `${name}MorphPosition`,
        Accessor.Type.VEC3,
        morphPositions,
      ),
    );
  primitive.addTarget(target);

  return document.createMesh(name).addPrimitive(primitive).setWeights([0.25]);
}

function createAccessor(document, buffer, name, type, array) {
  return document
    .createAccessor(name)
    .setType(type)
    .setArray(array)
    .setBuffer(buffer);
}

function totalIndexCount(document) {
  return document
    .getRoot()
    .listMeshes()
    .flatMap((mesh) => mesh.listPrimitives())
    .reduce(
      (total, primitive) => total + (primitive.getIndices()?.getCount() ?? 0),
      0,
    );
}
