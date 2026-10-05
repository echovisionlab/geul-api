#!/usr/bin/env node

import { pathToFileURL } from "node:url";
import { parseArgs } from "node:util";
import { Logger, Verbosity } from "@gltf-transform/core";
import {
  dedup,
  draco,
  flatten,
  inspect,
  instance,
  join,
  palette,
  prune,
  resample,
  simplify,
  sparse,
  textureCompress,
  unpartition,
  weld,
} from "@gltf-transform/functions";
import { stringify } from "csv-stringify/sync";
import {
  ready as resampleReady,
  resample as resampleWASM,
} from "keyframe-resample";
import { MeshoptSimplifier } from "meshoptimizer";
import sharp from "sharp";
import { createIO, parseParticleMeshArgs } from "./optimize-particle-mesh.mjs";

const USAGE = `Usage:
  asset-transform inspect <input.glb> --format csv
  asset-transform optimize <input.glb> <output.glb> --compress draco --texture-compress webp --texture-size 1024 --simplify false
  asset-transform optimize <input.glb> <output.glb> --compress draco --texture-compress webp --texture-size 1024 --simplify-ratio <0..1> --simplify-error <0..1>`;

export function parseTransformArgs(argv) {
  if (argv.length === 1 && ["--help", "-h"].includes(argv[0])) {
    return { help: true };
  }
  const [command, ...args] = argv;
  if (!["inspect", "optimize"].includes(command)) {
    throw new Error(USAGE);
  }
  const optionNames =
    command === "inspect"
      ? ["format"]
      : [
          "compress",
          "texture-compress",
          "texture-size",
          "simplify",
          "simplify-ratio",
          "simplify-error",
        ];
  const { values, positionals } = parseArgs({
    args,
    options: Object.fromEntries(
      optionNames.map((name) => [name, { type: "string" }]),
    ),
    allowPositionals: true,
  });
  if (command === "inspect") {
    if (positionals.length !== 1 || values.format !== "csv") {
      throw new Error("inspect requires one input and --format csv");
    }
    return { command, inputPath: positionals[0] };
  }
  if (
    positionals.length !== 2 ||
    values.compress !== "draco" ||
    values["texture-compress"] !== "webp" ||
    values["texture-size"] !== "1024"
  ) {
    throw new Error(
      "optimize requires two paths, Draco, WebP and texture size 1024",
    );
  }
  const simplifyArgs = ["simplify", "simplify-ratio", "simplify-error"].flatMap(
    (name) => (values[name] === undefined ? [] : [`--${name}`, values[name]]),
  );
  return {
    command,
    ...parseParticleMeshArgs([...positionals, ...simplifyArgs]),
  };
}

function csvSection(name, headers, rows) {
  return `\n ${name.toUpperCase()}\n ────────────────────────────────────────────\n${stringify([headers, ...rows])}\n\n`;
}

function csvValue(value) {
  if (Array.isArray(value)) return value.join(", ");
  if (typeof value === "boolean") return value ? "✓" : "";
  return value;
}

function xmpValue(value) {
  if (!value) return "";
  if (typeof value !== "object") return value.toString();
  if (value["@list"]) {
    const list = value["@list"];
    return list.join(list.some((item) => item.indexOf(",") > 0) ? "; " : ", ");
  }
  if (value["@type"] === "rdf:Alt") return value["rdf:_1"]["@value"];
  return JSON.stringify(value);
}

export async function inspectCSV(inputPath) {
  const io = (await createIO()).setLogger(new Logger(Verbosity.WARN));
  const jsonDocument = await io.readAsJSON(inputPath);
  const json = jsonDocument.json;
  let output = csvSection(
    "overview",
    ["key", "value"],
    [
      ["version", json.asset.version],
      ["generator", json.asset.generator || ""],
      ["extensionsUsed", (json.extensionsUsed || []).join(", ") || "none"],
      [
        "extensionsRequired",
        (json.extensionsRequired || []).join(", ") || "none",
      ],
    ],
  );
  const document = await io.readJSON(jsonDocument);
  const packet = document.getRoot().getExtension("KHR_xmp_json_ld");
  if (packet && packet.listProperties().length) {
    output += csvSection(
      "metadata",
      ["key", "value"],
      packet
        .listProperties()
        .map((name) => [name, xmpValue(packet.getProperty(name))]),
    );
  }
  for (const [name, section] of Object.entries(inspect(document))) {
    const rows = section.properties.map((property, index) => ({
      "#": index,
      ...property,
    }));
    if (!rows.length) {
      output += `\n ${name.toUpperCase()}\n ────────────────────────────────────────────\nNo ${name} found.\n\n`;
      continue;
    }
    output += csvSection(
      name,
      Object.keys(rows[0]),
      rows.map((row) => Object.values(row).map(csvValue)),
    );
    for (const warning of section.warnings || []) {
      document.getLogger().warn(warning);
    }
  }
  return output;
}

function updateMetadata(document) {
  const root = document.getRoot();
  const extension = root
    .listExtensionsUsed()
    .find((item) => item.extensionName === "KHR_xmp_json_ld");
  if (!extension) return;
  const packet =
    root.getExtension("KHR_xmp_json_ld") || extension.createPacket();
  const date = new Date().toISOString().substring(0, 10);
  packet
    .setContext({ ...packet.getContext(), xmp: "http://ns.adobe.com/xap/1.0/" })
    .setProperty("xmp:ModifyDate", date)
    .setProperty("xmp:MetadataDate", date);
}

export async function optimize(inputPath, outputPath, options) {
  const logger = new Logger(Verbosity.WARN);
  const io = (await createIO()).setLogger(logger);
  const document = (await io.read(inputPath)).setLogger(logger);
  for (const name of [
    "KHR_draco_mesh_compression",
    "EXT_meshopt_compression",
  ]) {
    if (document.hasExtension(name)) {
      document.disposeExtension(name);
      logger.warn(`Decoded ${name}. Further compression will be lossy.`);
    }
  }
  const transforms = [
    dedup(),
    instance({ min: 5 }),
    palette({ min: 5, keepAttributes: false }),
    flatten(),
    join({ keepNamed: false, keepMeshes: false }),
    weld(),
  ];
  if (options.simplify) {
    transforms.push(
      simplify({
        simplifier: MeshoptSimplifier,
        ratio: options.simplifyRatio,
        error: options.simplifyError,
        lockBorder: false,
      }),
    );
  }
  transforms.push(
    resample({ ready: resampleReady, resample: resampleWASM }),
    prune({
      keepAttributes: false,
      keepIndices: false,
      keepLeaves: false,
      keepSolidTextures: false,
    }),
    sparse(),
    textureCompress({
      encoder: sharp,
      resize: [1024, 1024],
      targetFormat: "webp",
      limitInputPixels: true,
    }),
    draco(),
  );
  await document.transform(...transforms);
  updateMetadata(document);
  await document.transform(unpartition());
  await io.write(outputPath, document);
}

async function main() {
  const options = parseTransformArgs(process.argv.slice(2));
  if (options.help) {
    process.stdout.write(`${USAGE}\n`);
  } else if (options.command === "inspect") {
    process.stdout.write(await inspectCSV(options.inputPath));
  } else {
    await optimize(options.inputPath, options.outputPath, options);
  }
}

if (
  process.argv[1] &&
  import.meta.url === pathToFileURL(process.argv[1]).href
) {
  main().catch((error) => {
    process.stderr.write(
      `${error instanceof Error ? error.stack || error.message : String(error)}\n`,
    );
    process.exitCode = 1;
  });
}
