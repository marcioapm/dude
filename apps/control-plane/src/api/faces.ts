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
import { getObject, putObject } from "../storage.ts";

/** Browsers resize before uploading, so this is a guard, not a quality setting. */
export const PHOTO_MAX_BYTES = 512 * 1024;

const TYPES: Record<string, (b: Uint8Array) => boolean> = {
  "image/png": (b) => b[0] === 0x89 && b[1] === 0x50 && b[2] === 0x4e && b[3] === 0x47,
  "image/jpeg": (b) => b[0] === 0xff && b[1] === 0xd8 && b[2] === 0xff,
  "image/gif": (b) => b[0] === 0x47 && b[1] === 0x49 && b[2] === 0x46,
  "image/webp": (b) => b[0] === 0x52 && b[1] === 0x49 && b[2] === 0x46 && b[3] === 0x46 && b[8] === 0x57 && b[9] === 0x45,
};

/**
 * Read an uploaded image and store it under `prefix`. The content type
 * must be an image type and the bytes must be one: what is served back is
 * never anything a browser would run.
 */
export async function storeImage(request: Request, prefix: string): Promise<{ key: string; token: string }> {
  const type = (request.headers.get("content-type") ?? "").split(";")[0]!.trim().toLowerCase();
  const looksLike = TYPES[type];
  if (!looksLike) throw badRequest("an image is image/png, image/jpeg, image/webp or image/gif");
  const declared = Number(request.headers.get("content-length") ?? "0");
  if (declared > PHOTO_MAX_BYTES) throw badRequest(`an image is at most ${PHOTO_MAX_BYTES / 1024} KB`);
  const bytes = new Uint8Array(await request.arrayBuffer());
  if (bytes.length === 0) throw badRequest("no image in the request");
  if (bytes.length > PHOTO_MAX_BYTES) throw badRequest(`an image is at most ${PHOTO_MAX_BYTES / 1024} KB`);
  if (!looksLike(bytes)) throw badRequest(`the body is not ${type}`);
  const token = randomBytes(12).toString("base64url");
  const key = `${prefix}/${token}`;
  await putObject(key, bytes, type);
  return { key, token };
}

/** An image for an `<img>`, from the key its token resolved to. */
export async function serveImage(key: string | null | undefined): Promise<Response> {
  const found = key ? await getObject(key) : null;
  if (!found) throw notFound("no such image");
  return new Response(found.bytes, {
    headers: {
      // Stored only after its bytes matched an image type (storeImage).
      "content-type": found.type,
      // The key and token change with the image, so a URL's image never does.
      "cache-control": "private, max-age=31536000, immutable",
      "x-content-type-options": "nosniff",
      "content-security-policy": "default-src 'none'",
    },
  });
}
