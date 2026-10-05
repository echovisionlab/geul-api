import { Accessor, Document } from "@gltf-transform/core";
import { KHRXMP } from "@gltf-transform/extensions";
import sharp from "sharp";

export async function createOptimizationFixture({ palette = false } = {}) {
  const document = new Document();
  const buffer = document.createBuffer();
  const scene = document.createScene('Scene, "one"');
  document.getRoot().setDefaultScene(scene);
  const pixels = Buffer.alloc(1536 * 512 * 3);
  for (let index = 0; index < pixels.length; index += 3) {
    pixels[index] = (index / 3) % 251;
    pixels[index + 1] = 120;
    pixels[index + 2] = 200;
  }
  const texture = document
    .createTexture('Texture, "wide"')
    .setImage(
      await sharp(pixels, { raw: { width: 1536, height: 512, channels: 3 } })
        .png()
        .toBuffer(),
    )
    .setMimeType("image/png");
  const material = document
    .createMaterial("Textured")
    .setBaseColorTexture(texture);
  const mesh = createGridMesh(document, buffer, material, "Grid");
  const parent = document.createNode("Parent").setTranslation([2, 3, 4]);
  scene.addChild(parent);
  const animatedNode = document.createNode('Animated, "grid"').setMesh(mesh);
  parent.addChild(animatedNode);
  for (let index = 0; index < 6; index++) {
    const instanceMesh = palette
      ? createGridMesh(
          document,
          buffer,
          document
            .createMaterial(`Solid${index}`)
            .setBaseColorFactor([index / 6, 0.5, 1, 1]),
          `SolidGrid${index}`,
        )
      : mesh;
    parent.addChild(
      document
        .createNode(`Instance${index}`)
        .setMesh(instanceMesh)
        .setTranslation([index * 2, 0, 0]),
    );
  }
  const times = accessor(
    document,
    buffer,
    Accessor.Type.SCALAR,
    new Float32Array([0, 1, 2, 3, 4]),
  );
  const values = accessor(
    document,
    buffer,
    Accessor.Type.VEC3,
    new Float32Array([0, 0, 0, 1, 0, 0, 2, 0, 0, 3, 0, 0, 4, 0, 0]),
  );
  const sampler = document
    .createAnimationSampler()
    .setInput(times)
    .setOutput(values)
    .setInterpolation("LINEAR");
  const channel = document
    .createAnimationChannel()
    .setTargetNode(animatedNode)
    .setTargetPath("translation")
    .setSampler(sampler);
  document.createAnimation("Movement").addSampler(sampler).addChannel(channel);
  const xmp = document.createExtension(KHRXMP);
  const packet = xmp
    .createPacket()
    .setContext({ xmp: "http://ns.adobe.com/xap/1.0/" })
    .setProperty("xmp:ModifyDate", "2000-01-01");
  document.getRoot().setExtension("KHR_xmp_json_ld", packet);
  return document;
}

function accessor(document, buffer, type, array) {
  return document
    .createAccessor()
    .setType(type)
    .setArray(array)
    .setBuffer(buffer);
}

function createGridMesh(document, buffer, material, name) {
  const segments = 16;
  const width = segments + 1;
  const positions = new Float32Array(width * width * 3);
  const normals = new Float32Array(width * width * 3);
  const texcoords = new Float32Array(width * width * 2);
  for (let y = 0; y < width; y++) {
    for (let x = 0; x < width; x++) {
      const vertex = y * width + x;
      positions.set(
        [x / segments, y / segments, Math.sin((x / segments) * Math.PI) * 0.01],
        vertex * 3,
      );
      normals.set([0, 0, 1], vertex * 3);
      texcoords.set([x / segments, y / segments], vertex * 2);
    }
  }
  const indices = new Uint16Array(segments * segments * 6);
  let offset = 0;
  for (let y = 0; y < segments; y++) {
    for (let x = 0; x < segments; x++) {
      const topLeft = y * width + x;
      indices.set(
        [
          topLeft,
          topLeft + width,
          topLeft + 1,
          topLeft + 1,
          topLeft + width,
          topLeft + width + 1,
        ],
        offset,
      );
      offset += 6;
    }
  }
  const primitive = document
    .createPrimitive()
    .setAttribute(
      "POSITION",
      accessor(document, buffer, Accessor.Type.VEC3, positions),
    )
    .setAttribute(
      "NORMAL",
      accessor(document, buffer, Accessor.Type.VEC3, normals),
    )
    .setAttribute(
      "TEXCOORD_0",
      accessor(document, buffer, Accessor.Type.VEC2, texcoords),
    )
    .setIndices(accessor(document, buffer, Accessor.Type.SCALAR, indices))
    .setMaterial(material);
  return document.createMesh(name).addPrimitive(primitive);
}
