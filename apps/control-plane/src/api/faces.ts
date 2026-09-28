/**
 * Faces: people's photos and projects' images, as objects in storage.
 *
 * An upload is the image itself as the request body (image/png, jpeg,
 * webp or gif, at most PHOTO_MAX_BYTES; browsers resize before sending).
 * Each upload is a new object under a new key with a new token, so a URL
 * handed out for one image never shows another and can be cached forever.
 * The `<img>` that loads it sends no key, so its URL carries the token.
 */

import { randomBytes } from "node:crypto";
import { badRequest, notFound } from "./http.ts";
import { deleteObject, getObject, putObject } from "../storage.ts";

/** Browsers resize before uploading, so this is a guard, not a quality setting. */
export const PHOTO_MAX_BYTES = 512 * 1024;

/** Each image type: the key's extension, and whether bytes are one. */
const TYPES: Record<string, { ext: string; is: (b: Uint8Array) => boolean }> = {
  "image/png": { ext: "png", is: (b) => b[0] === 0x89 && b[1] === 0x50 && b[2] === 0x4e && b[3] === 0x47 },
  "image/jpeg": { ext: "jpg", is: (b) => b[0] === 0xff && b[1] === 0xd8 && b[2] === 0xff },
  "image/gif": { ext: "gif", is: (b) => b[0] === 0x47 && b[1] === 0x49 && b[2] === 0x46 },
  "image/webp": { ext: "webp", is: (b) => b[0] === 0x52 && b[1] === 0x49 && b[2] === 0x46 && b[3] === 0x46 && b[8] === 0x57 && b[9] === 0x45 },
};
const TYPE_OF_EXT = new Map(Object.entries(TYPES).map(([type, { ext }]) => [ext, type]));

const tooBig = () => badRequest(`an image is at most ${PHOTO_MAX_BYTES / 1024} KB`);

/**
 * The body, read no further than the cap: a client that sends no length,
 * or a false one, is cut off at PHOTO_MAX_BYTES rather than held whole.
 */
async function readCapped(request: Request): Promise<Uint8Array> {
  const declared = request.headers.get("content-length");
  if (declared !== null && !(Number(declared) <= PHOTO_MAX_BYTES)) throw tooBig();
  if (!request.body) return new Uint8Array();
  const chunks: Uint8Array[] = [];
  let size = 0;
  const reader = request.body.getReader();
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    size += value.length;
    if (size > PHOTO_MAX_BYTES) {
      await reader.cancel();
      throw tooBig();
    }
    chunks.push(value);
  }
  const bytes = new Uint8Array(size);
  let at = 0;
  for (const c of chunks) {
    bytes.set(c, at);
    at += c.length;
  }
  return bytes;
}

/**
 * Store an uploaded image under `prefix` (built from ids the caller has
 * already found in its organization, so it names nothing else) and record
 * it with `record`, which gets the new key and token and returns the key it
 * replaced. The content type must be an image type and the bytes must be
 * one: what is served back is never anything a browser would run. Should
 * recording fail, the new object is removed; once it succeeds, the old one.
 */
export async function replaceImage<T>(
  request: Request,
  prefix: string,
  record: (image: { key: string; token: string }) => Promise<{ result: T; old: string | null | undefined }>,
): Promise<T> {
  const type = (request.headers.get("content-type") ?? "").split(";")[0]!.trim().toLowerCase();
  const known = TYPES[type];
  if (!known) throw badRequest("an image is image/png, image/jpeg, image/webp or image/gif");
  const bytes = await readCapped(request);
  if (bytes.length === 0) throw badRequest("no image in the request");
  if (!known.is(bytes)) throw badRequest(`the body is not ${type}`);
  const token = randomBytes(12).toString("base64url");
  // The type rides in the key, so serving it is one read.
  const key = `${prefix}/${token}.${known.ext}`;
  await putObject(key, bytes, type);
  let recorded: { result: T; old: string | null | undefined };
  try {
    recorded = await record({ key, token });
  } catch (err) {
    await deleteObject(key);
    throw err;
  }
  if (recorded.old) await deleteObject(recorded.old);
  return recorded.result;
}

/** An image for an `<img>`, from the key its token resolved to. */
export async function serveImage(key: string | null | undefined): Promise<Response> {
  const type = TYPE_OF_EXT.get(key?.split(".").pop() ?? "");
  const bytes = key && type ? await getObject(key) : null;
  if (!bytes || !type) throw notFound("no such image");
  return new Response(bytes, {
    headers: {
      // Stored only after its bytes matched this type (replaceImage).
      "content-type": type,
      // The key and token change with the image, so a URL's image never does.
      "cache-control": "private, max-age=31536000, immutable",
      "x-content-type-options": "nosniff",
      "content-security-policy": "default-src 'none'",
    },
  });
}
