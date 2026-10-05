import {
  CreateBucketCommand,
  DeleteBucketCommand,
  DeleteObjectsCommand,
  HeadObjectCommand,
  PutObjectCommand,
  S3Client,
} from "@aws-sdk/client-s3";
import { create } from "@bufbuild/protobuf";
import { AssetDisposition } from "@echovisionlab/geul-event";
import { AssetWriteTargetSchema } from "@echovisionlab/geul-proto/common/media_pb.ts";
import { randomUUID } from "node:crypto";
import { afterAll, beforeAll, describe, expect, it } from "vitest";

const endpoint =
  process.env.OG_S3_INTEGRATION_ENDPOINT ?? "http://127.0.0.1:19000";
const accessKeyId =
  process.env.OG_S3_INTEGRATION_ACCESS_KEY ?? "test-access-key";
const secretAccessKey =
  process.env.OG_S3_INTEGRATION_SECRET_KEY ?? "test-secret-key";
const bucket = `og-integration-${randomUUID()}`;
const objectKeys: string[] = [];

describe("OG S3 MinIO integration", () => {
  const client = new S3Client({
    endpoint,
    region: "us-east-1",
    credentials: { accessKeyId, secretAccessKey },
    forcePathStyle: true,
  });

  beforeAll(async () => {
    process.env.DATABASE_DSN = "postgres://unused@localhost/geul";
    process.env.S3_ENDPOINT = endpoint;
    process.env.S3_MEDIA_BUCKET = bucket;
    process.env.S3_ACCESS_KEY_ID = accessKeyId;
    process.env.S3_SECRET_ACCESS_KEY = secretAccessKey;
    process.env.BACKEND_URL = "http://unused";
    process.env.TOKEN_SIGNING_SECRET = "test-only-token-signing-secret";
    await client.send(new CreateBucketCommand({ Bucket: bucket }));
  });

  afterAll(async () => {
    if (objectKeys.length > 0) {
      await client.send(
        new DeleteObjectsCommand({
          Bucket: bucket,
          Delete: { Objects: objectKeys.map((Key) => ({ Key })) },
        }),
      );
    }
    await client.send(new DeleteBucketCommand({ Bucket: bucket }));
    client.destroy();
  });

  function target() {
    const assetId = randomUUID();
    const objectKey = `asset/${assetId}.webp`;
    objectKeys.push(objectKey);
    return create(AssetWriteTargetSchema, {
      assetId,
      objectKey,
      extension: "webp",
      mimeType: "image/webp",
      disposition: AssetDisposition.INLINE,
    });
  }

  it("conditionally creates once and reuses only an identical object", async () => {
    const output = target();
    const bytes = Buffer.from("identical-render-result");
    const { writeOgAssetWithClient } = await import("./s3.js");
    const first = await writeOgAssetWithClient(
      client as never,
      output,
      bytes,
      bucket,
    );
    const replay = await writeOgAssetWithClient(
      client as never,
      output,
      bytes,
      bucket,
    );
    expect(replay).toEqual(first);

    const head = await client.send(
      new HeadObjectCommand({
        Bucket: bucket,
        Key: output.objectKey,
        ChecksumMode: "ENABLED",
      }),
    );
    expect(head.ContentLength).toBe(bytes.length);
    expect(head.ContentType).toBe("image/webp");
    expect(head.Metadata?.sha256).toBe(
      Buffer.from(first.sha256).toString("hex"),
    );
  });

  it("refuses to overwrite a conflicting immutable object", async () => {
    const output = target();
    await client.send(
      new PutObjectCommand({
        Bucket: bucket,
        Key: output.objectKey,
        Body: Buffer.from("wrong-object"),
        ContentType: "image/webp",
        Metadata: { sha256: "wrong" },
      }),
    );
    const { writeOgAssetWithClient } = await import("./s3.js");
    await expect(
      writeOgAssetWithClient(
        client as never,
        output,
        Buffer.from("expected-object"),
        bucket,
      ),
    ).rejects.toMatchObject({
      name: "IntegrityError",
      errorCode: "integrity_failure",
    });
  });
});
