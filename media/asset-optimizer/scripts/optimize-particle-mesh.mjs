#!/usr/bin/env node

import { pathToFileURL } from "node:url";
import { NodeIO } from "@gltf-transform/core";
import { ALL_EXTENSIONS } from "@gltf-transform/extensions";
import {
  dequantize,
  draco,
  prune,
  simplify,
  weld,
} from "@gltf-transform/functions";
import draco3d from "draco3dgltf";
import {
  MeshoptDecoder,
  MeshoptEncoder,
  MeshoptSimplifier,
} from "meshoptimizer";

const USAGE = `Usage:
  optimize-particle-mesh <input.glb> <output.glb> --simplify false
  optimize-particle-mesh <input.glb> <output.glb> --simplify-ratio <0..1> --simplify-error <0..1>`;

function parseNumber(value, flag, { min, max, minInclusive = true }) {
  const parsed = Number(value);
  const belowMinimum = minInclusive ? parsed < min : parsed <= min;
  if (!Number.isFinite(parsed) || belowMinimum || parsed > max) {
    const lowerBound = minInclusive ? "[" : "(";
    throw new Error(`${flag} must be in ${lowerBound}${min}, ${max}]`);
  }
  return parsed;
}

export function parseParticleMeshArgs(argv) {
  if (argv.includes("--help") || argv.includes("-h")) {
    return { help: true };
  }
  if (argv.length < 4) {
    throw new Error(USAGE);
  }

  const [inputPath, outputPath, ...flags] = argv;
  let simplifyEnabled = true;
  let simplifyRatio;
  let simplifyError;

  for (let index = 0; index < flags.length; index += 2) {
    const flag = flags[index];
    const value = flags[index + 1];
    if (value === undefined) {
      throw new Error(`${flag} requires a value`);
    }
    switch (flag) {
      case "--simplify":
        if (value !== "false") {
          throw new Error("--simplify only accepts false");
        }
        simplifyEnabled = false;
        break;
      case "--simplify-ratio":
        simplifyRatio = parseNumber(value, flag, {
          min: 0,
          max: 1,
          minInclusive: false,
        });
        break;
      case "--simplify-error":
        simplifyError = parseNumber(value, flag, { min: 0, max: 1 });
        break;
      default:
        throw new Error(`unknown option: ${flag}`);
    }
  }

  if (!simplifyEnabled) {
    if (simplifyRatio !== undefined || simplifyError !== undefined) {
      throw new Error(
        "--simplify false cannot be combined with simplify ratio or error",
      );
    }
    return { inputPath, outputPath, simplify: false };
  }
  if (simplifyRatio === undefined || simplifyError === undefined) {
    throw new Error("--simplify-ratio and --simplify-error are required");
  }
  return {
    inputPath,
    outputPath,
    simplify: true,
    simplifyRatio,
    simplifyError,
  };
}

async function createIO() {
  const [decoder, encoder] = await Promise.all([
    draco3d.createDecoderModule(),
    draco3d.createEncoderModule(),
  ]);
  return new NodeIO().registerExtensions(ALL_EXTENSIONS).registerDependencies({
    "draco3d.decoder": decoder,
    "draco3d.encoder": encoder,
    "meshopt.decoder": MeshoptDecoder,
    "meshopt.encoder": MeshoptEncoder,
  });
}

function stripParticleMeshPayload(document) {
  const root = document.getRoot();

  for (const extension of [...root.listExtensionsUsed()]) {
    extension.dispose();
  }
  for (const animation of [...root.listAnimations()]) {
    animation.dispose();
  }
  for (const node of root.listNodes()) {
    node.setSkin(null);
    node.setWeights([]);
  }
  for (const skin of [...root.listSkins()]) {
    skin.dispose();
  }
  for (const mesh of root.listMeshes()) {
    mesh.setWeights([]);
    for (const primitive of mesh.listPrimitives()) {
      if (!primitive.getAttribute("POSITION")) {
        throw new Error(
          `mesh ${JSON.stringify(mesh.getName())} contains a primitive without POSITION`,
        );
      }
      primitive.setMaterial(null);
      for (const semantic of primitive.listSemantics()) {
        if (semantic !== "POSITION") {
          primitive.setAttribute(semantic, null);
        }
      }
      for (const target of [...primitive.listTargets()]) {
        target.dispose();
      }
    }
  }
  for (const material of [...root.listMaterials()]) {
    material.dispose();
  }
  for (const texture of [...root.listTextures()]) {
    texture.dispose();
  }
}

export async function optimizeParticleMeshDocument(document, options) {
  await document.transform(dequantize());
  stripParticleMeshPayload(document);
  await document.transform(prune(), weld({ overwrite: true }));

  if (options.simplify) {
    await document.transform(
      simplify({
        simplifier: MeshoptSimplifier,
        ratio: options.simplifyRatio,
        error: options.simplifyError,
      }),
    );
  }

  await document.transform(prune(), draco());
  return document;
}

export async function optimizeParticleMesh(inputPath, outputPath, options) {
  const io = await createIO();
  const document = await io.read(inputPath);
  await optimizeParticleMeshDocument(document, options);
  await io.write(outputPath, document);
}

async function main() {
  const options = parseParticleMeshArgs(process.argv.slice(2));
  if (options.help) {
    process.stdout.write(`${USAGE}\n`);
    return;
  }
  await optimizeParticleMesh(options.inputPath, options.outputPath, options);
}

const isMain =
  process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href;
if (isMain) {
  main().catch((error) => {
    const message =
      error instanceof Error ? error.stack || error.message : String(error);
    process.stderr.write(`${message}\n`);
    process.exitCode = 1;
  });
}
