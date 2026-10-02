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
import { ATTACHMENT_TYPES, type AttachmentType } from "@dude/domain";
import { badRequest, notFound, readCapped } from "./http.ts";
import { EXTENSION, sniff } from "../images.ts";
import { deleteObject, getObject, putObject } from "../storage.ts";

/** Browsers resize before uploading, so this is a guard, not a quality setting. */
export const PHOTO_MAX_BYTES = 512 * 1024;

const TYPE_OF_EXT = new Map(ATTACHMENT_TYPES.map((type) => [EXTENSION[type], type]));

const tooBig = () => badRequest(`an image is at most ${PHOTO_MAX_BYTES / 1024} KB`);

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
  const type = (request.headers.get("content-type") ?? "").split(";")[0]!.trim().toLowerCase() as AttachmentType;
  if (!ATTACHMENT_TYPES.includes(type)) throw badRequest("an image is image/png, image/jpeg, image/webp or image/gif");
  const bytes = await readCapped(request, PHOTO_MAX_BYTES, tooBig);
  if (bytes.length === 0) throw badRequest("no image in the request");
  if (sniff(bytes) !== type) throw badRequest(`the body is not ${type}`);
  const token = randomBytes(12).toString("base64url");
  // The type rides in the key, so serving it is one read.
  const key = `${prefix}/${token}.${EXTENSION[type]}`;
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
