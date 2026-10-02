/**
 * Images a person sends an agent (migration 066).
 *
 * The browser scales each image before it uploads (apps/web/src/images.ts)
 * and sends two files: the original as picked, and the variant the agent is
 * sent, each with its type in a field of its own (`originalType`,
 * `deliveredType`) and an optional `name`. Both are checked here by their
 * bytes — type by magic number, size from the header — against what they
 * claim, and stored in the photo
 * bucket. The row is the task's, and unattached until a steer, an answer or
 * the task's prompt carries its id (the orchestrator attaches it). An
 * upload never sent is swept after a day (sweeper.ts).
 */

import { createHash, randomBytes } from "node:crypto";
import { ATTACHMENT_LIMITS, ATTACHMENT_TYPES, newId } from "@dude/domain";
import { withOrg } from "../../db/client.ts";
import { EXTENSION, imageInfo, type ImageType } from "../../images.ts";
import { deleteObject, getObject, putObject, storageConfigured } from "../../storage.ts";
import { HttpError, badRequest, conflict, forbidden, json, noContent, notFound, readCapped } from "../http.ts";
import type { RequestContext, Router } from "../router.ts";

/** The JSON shape of an attachment (AttachmentInfo), from a row of `attachments` aliased `a`. */
const ATTACHMENT_JSON = `json_build_object(
  'id', a.id, 'name', a.name, 'contentType', a.content_type,
  'width', a.width, 'height', a.height, 'bytes', a.bytes,
  'original', json_build_object('contentType', a.original_content_type, 'width', a.original_width,
    'height', a.original_height, 'bytes', a.original_bytes))`;

const unconfigured = () =>
  new HttpError(503, "image storage is not configured (DUDE_S3_BUCKET)", "storage_unconfigured");

// Both files and the form's own bytes.
const MAX_BODY = ATTACHMENT_LIMITS.originalBytes + ATTACHMENT_LIMITS.deliveredBytes + 64 * 1024;
const tooBig = () => badRequest(`an upload is at most ${Math.round(MAX_BODY / 1e6)} MB`);

/**
 * A name lux takes and a person recognises: path separators and control
 * characters become spaces, at most 255 bytes, and the extension is the
 * delivered type's — what the agent gets is that type, whatever it was picked as.
 */
export function attachmentName(raw: string, type: ImageType): string {
  // eslint-disable-next-line no-control-regex
  let base = raw.replace(/[/\\\u0000-\u001f\u007f-\u009f]/g, " ").trim();
  base = base.replace(/\.(png|jpe?g|webp|gif)$/i, "").trim() || "image";
  const ext = `.${EXTENSION[type]}`;
  const encoder = new TextEncoder();
  while (encoder.encode(base + ext).length > 255) base = [...base].slice(0, -1).join("");
  return base + ext;
}

/** A file part's bytes, checked to be the image type it claims. */
async function imagePart(form: FormData, field: string): Promise<{ bytes: Uint8Array; type: ImageType; width: number; height: number; fileName: string }> {
  const part = form.get(field);
  if (!(part instanceof File)) throw badRequest(`the form has no ${field} file`);
  const bytes = new Uint8Array(await part.arrayBuffer());
  if (bytes.length === 0) throw badRequest(`${field}: the file is empty`);
  // Its own field: Bun's multipart parser takes a part's type from its
  // filename's extension, not from the part's Content-Type.
  const declared = form.get(`${field}Type`);
  const claimed = (typeof declared === "string" ? declared : "").split(";")[0]!.trim().toLowerCase();
  if (!(ATTACHMENT_TYPES as readonly string[]).includes(claimed)) {
    throw badRequest(`${field}: an image is PNG, JPEG, WebP or GIF, not ${claimed || "an unnamed type"}`);
  }
  const info = imageInfo(bytes);
  if (!info) throw badRequest(`${field}: the file is not a PNG, JPEG, WebP or GIF image`);
  if (info.type !== claimed) throw badRequest(`${field}: the file says ${claimed} but is ${info.type}`);
  return { bytes, ...info, fileName: part.name };
}

async function upload(ctx: RequestContext): Promise<Response> {
  // Before the body is read: nothing could be kept.
  if (!storageConfigured()) throw unconfigured();
  const taskId = ctx.params.id!;
  const { organizationId } = ctx.principal;
  const task = await withOrg(organizationId, async ({ sql }) =>
    (await sql`SELECT id FROM tasks WHERE id = ${taskId}`)[0]);
  if (!task) throw notFound(`task ${taskId} not found`);

  const body = await readCapped(ctx.request, MAX_BODY, tooBig);
  let form: FormData;
  try {
    form = (await new Response(body, { headers: { "content-type": ctx.request.headers.get("content-type") ?? "" } }).formData()) as FormData;
  } catch {
    throw badRequest("the body is not a multipart form");
  }
  const original = await imagePart(form, "original");
  const delivered = await imagePart(form, "delivered");
  if (original.bytes.length > ATTACHMENT_LIMITS.originalBytes) {
    throw badRequest(`original: an image is at most ${ATTACHMENT_LIMITS.originalBytes / 1e6} MB`);
  }
  if (delivered.bytes.length > ATTACHMENT_LIMITS.deliveredBytes) {
    throw badRequest(`delivered: what the agent is sent is at most 4.5 MiB`);
  }
  if (Math.max(delivered.width, delivered.height) > ATTACHMENT_LIMITS.maxSide) {
    throw badRequest(`delivered: what the agent is sent is at most ${ATTACHMENT_LIMITS.maxSide} px on its long side`);
  }
  const rawName = form.get("name");
  const name = attachmentName(typeof rawName === "string" && rawName.trim() ? rawName : original.fileName, delivered.type);

  const id = newId("attachment");
  // A random part, so a key names nothing guessable; ids alone sort by time.
  const prefix = `attachments/${organizationId}/${taskId}/${id}-${randomBytes(9).toString("base64url")}`;
  const objectKey = `${prefix}-delivered.${EXTENSION[delivered.type]}`;
  const originalKey = `${prefix}-original.${EXTENSION[original.type]}`;
  await putObject(objectKey, delivered.bytes, delivered.type);
  try {
    await putObject(originalKey, original.bytes, original.type);
  } catch (err) {
    await deleteObject(objectKey);
    throw err;
  }
  const sha256 = createHash("sha256").update(delivered.bytes).digest("hex");
  try {
    const row = await withOrg(organizationId, async ({ sql }) => (await sql`
      INSERT INTO attachments AS a (id, organization_id, task_id, uploaded_by, name,
        content_type, width, height, bytes, sha256, object_key,
        original_content_type, original_width, original_height, original_bytes, original_key)
      VALUES (${id}, ${organizationId}, ${taskId}, (SELECT id FROM people WHERE id = ${ctx.principal.personId}), ${name},
        ${delivered.type}, ${delivered.width}, ${delivered.height}, ${delivered.bytes.length}, ${sha256}, ${objectKey},
        ${original.type}, ${original.width}, ${original.height}, ${original.bytes.length}, ${originalKey})
      RETURNING ${sql.unsafe(ATTACHMENT_JSON)} AS info`)[0] as { info: unknown });
    return json(row.info, 201);
  } catch (err) {
    await deleteObject(objectKey);
    await deleteObject(originalKey);
    throw err;
  }
}

/** Content-Disposition for `name`: an ASCII fallback and the name itself (RFC 6266). */
function disposition(kind: "inline" | "attachment", name: string): string {
  const fallback = name.replace(/[^\x20-\x7e]|["\\]/g, "_");
  return `${kind}; filename="${fallback}"; filename*=UTF-8''${encodeURIComponent(name)}`;
}

async function serve(ctx: RequestContext): Promise<Response> {
  const variant = ctx.url.searchParams.get("variant") ?? "delivered";
  if (variant !== "delivered" && variant !== "original") throw badRequest("variant is delivered or original");
  const row = await withOrg(ctx.principal.organizationId, async ({ sql }) => (await sql`
    SELECT name, content_type, object_key, original_content_type, original_key
    FROM attachments WHERE id = ${ctx.params.id!}`)[0] as
    { name: string; content_type: string; object_key: string; original_content_type: string; original_key: string } | undefined);
  if (!row) throw notFound("no such attachment");
  const [key, type] = variant === "original" ? [row.original_key, row.original_content_type] : [row.object_key, row.content_type];
  // The row admits only the four raster types; never anything a browser would run.
  if (!(ATTACHMENT_TYPES as readonly string[]).includes(type)) throw notFound("no such attachment");
  const bytes = await getObject(key);
  if (!bytes) throw notFound("the image is gone from storage");
  const ext = EXTENSION[type as ImageType];
  const name = row.name.replace(/\.[a-z]+$/i, "") + `.${ext}`;
  return new Response(bytes, {
    headers: {
      "content-type": type,
      "content-disposition": disposition(ctx.url.searchParams.get("download") ? "attachment" : "inline", name),
      "x-content-type-options": "nosniff",
      "content-security-policy": "default-src 'none'",
      // An attachment's bytes never change.
      "cache-control": "private, max-age=31536000, immutable",
    },
  });
}

/** Remove an upload not sent yet: its chip's ✕. Once sent, it is the message's. */
async function remove(ctx: RequestContext): Promise<Response> {
  const id = ctx.params.id!;
  await withOrg(ctx.principal.organizationId, async ({ sql }) => {
    const row = (await sql`SELECT uploaded_by, attached_at FROM attachments WHERE id = ${id} FOR UPDATE`)[0] as
      { uploaded_by: string | null; attached_at: Date | null } | undefined;
    if (!row) throw notFound("no such attachment");
    if (row.attached_at) throw conflict("this image was sent; it stays with its message");
    if (row.uploaded_by !== ctx.principal.personId) throw forbidden("only who uploaded it can remove it");
    // The trigger queues its objects for the sweeper.
    await sql`DELETE FROM attachments WHERE id = ${id}`;
  });
  return noContent();
}

/** What the composer needs to know before anything is picked. */
async function limits(): Promise<Response> {
  return json({ enabled: storageConfigured(), types: ATTACHMENT_TYPES, ...ATTACHMENT_LIMITS });
}

export function registerAttachmentRoutes(router: Router): void {
  router.get("/v1/attachment-limits", limits);
  router.post("/v1/tasks/:id/attachments", upload);
  router.get("/v1/attachments/:id", serve);
  router.delete("/v1/attachments/:id", remove);
}
