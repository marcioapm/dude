/**
 * Object storage: the S3 bucket people's and projects' photos live in.
 *
 * The database keeps only an object's key; the bytes are here. Any
 * S3-compatible store works (AWS, MinIO, versitygw); a custom endpoint is
 * addressed path-style. Unconfigured, uploads answer 503 and nothing else
 * changes: faces fall back to initials.
 */

import { S3Client } from "bun";
import { HttpError } from "./api/http.ts";

let client: S3Client | null | undefined;

function configured(): S3Client | null {
  if (client !== undefined) return client;
  const bucket = process.env.DUDE_S3_BUCKET;
  client = bucket
    ? new S3Client({
        bucket,
        region: process.env.DUDE_S3_REGION || "us-east-1",
        ...(process.env.DUDE_S3_ENDPOINT ? { endpoint: process.env.DUDE_S3_ENDPOINT } : {}),
        // Unset, Bun reads the usual AWS environment (AWS_ACCESS_KEY_ID…).
        ...(process.env.DUDE_S3_ACCESS_KEY ? { accessKeyId: process.env.DUDE_S3_ACCESS_KEY } : {}),
        ...(process.env.DUDE_S3_SECRET_KEY ? { secretAccessKey: process.env.DUDE_S3_SECRET_KEY } : {}),
      })
    : null;
  return client;
}

function required(): S3Client {
  const s3 = configured();
  if (!s3) throw new HttpError(503, "photo storage is not configured (DUDE_S3_BUCKET)", "storage_unconfigured");
  return s3;
}

/** Store an object; it is never rewritten: a new photo is a new key. */
export async function putObject(key: string, bytes: Uint8Array, type: string): Promise<void> {
  await required().write(key, bytes, { type });
}

/** An object's bytes and type, or null when there is no such object. */
export async function getObject(key: string): Promise<{ bytes: ArrayBuffer; type: string } | null> {
  const file = required().file(key);
  try {
    const [bytes, stat] = await Promise.all([file.arrayBuffer(), file.stat()]);
    return { bytes, type: stat.type };
  } catch (err) {
    if ((err as { code?: string }).code === "NoSuchKey") return null;
    throw err;
  }
}

/** Remove an object, best effort: one left behind costs a few KB, never a wrong face. */
export async function deleteObject(key: string): Promise<void> {
  const s3 = configured();
  if (!s3) return;
  await s3.delete(key).catch((err) => console.error(`storage: could not delete ${key}:`, err));
}
