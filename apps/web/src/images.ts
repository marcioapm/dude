/**
 * Images a person attaches, made ready in the browser before they are
 * uploaded: checked, scaled and re-encoded into the variant the agent is
 * sent. The original goes up as it was picked.
 *
 * The delivered variant is drawn into a canvas — so EXIF and any other
 * metadata are gone, and a GIF is its first frame — at most `maxSide` on its
 * long side. A PNG stays a PNG when it fits; otherwise WebP, then JPEG, at
 * decreasing quality, then a smaller scale estimated from how far over it
 * came, until it is at most `deliveredBytes` and its share of what the
 * message's `messageBytes` has left (budgetFor).
 */

import type { AttachmentType } from "@dude/domain";

export interface Limits {
  perMessage: number;
  originalBytes: number;
  deliveredBytes: number;
  messageBytes: number;
  maxSide: number;
}

/** The type of an image file by its first bytes, or null for anything else. */
export function sniffImage(head: Uint8Array): AttachmentType | null {
  const at = (i: number, text: string) => [...text].every((c, j) => head[i + j] === c.charCodeAt(0));
  if (head[0] === 0x89 && at(1, "PNG\r\n\x1a\n")) return "image/png";
  if (head[0] === 0xff && head[1] === 0xd8 && head[2] === 0xff) return "image/jpeg";
  if (at(0, "GIF87a") || at(0, "GIF89a")) return "image/gif";
  if (at(0, "RIFF") && at(8, "WEBP")) return "image/webp";
  return null;
}

/** Why a picked file cannot be sent, short for its chip and longer for the warning line; null when it can. */
export async function refuse(file: File, limits: Limits): Promise<{ short: string; detail: string } | null> {
  const type = sniffImage(new Uint8Array(await file.slice(0, 16).arrayBuffer()));
  if (!type) {
    const ext = /\.([a-z0-9]{1,5})$/i.exec(file.name)?.[1]?.toUpperCase();
    return { short: ext ? `${ext} not supported` : "Not an image", detail: "only PNG, JPEG, WebP and GIF can be sent" };
  }
  if (file.size > limits.originalBytes) {
    const mb = Math.round(file.size / 1e6);
    return { short: `${mb} MB · max ${limits.originalBytes / 1e6}`, detail: `one is over ${limits.originalBytes / 1e6} MB` };
  }
  return null;
}

/** One image as it is uploaded. */
export interface Prepared {
  name: string;
  original: Blob;
  delivered: Blob;
  width: number;
  height: number;
}

/** An image's pixel size. */
export interface Size {
  width: number;
  height: number;
}

/** The size to draw `from` at: at most `maxSide` on its long side, then `scale` of that. */
export function fit(from: Size, maxSide: number, scale = 1): Size {
  const ratio = Math.min(1, maxSide / Math.max(from.width, from.height)) * scale;
  return { width: Math.max(1, Math.round(from.width * ratio)), height: Math.max(1, Math.round(from.height * ratio)) };
}

/**
 * The encodings tried, in order, for a source of `type`: the same type
 * first for a PNG (lossless stays lossless when it fits), then WebP and
 * JPEG at falling quality.
 */
export function encodings(type: string): Array<{ type: "image/png" | "image/webp" | "image/jpeg"; quality?: number }> {
  const lossy: Array<{ type: "image/webp" | "image/jpeg"; quality: number }> = [];
  for (const quality of [0.92, 0.85, 0.75, 0.6]) lossy.push({ type: "image/webp", quality }, { type: "image/jpeg", quality });
  return type === "image/png" ? [{ type: "image/png" }, ...lossy] : lossy;
}

/** How a canvas encodes, so tests can stand in for one. */
export interface Encoder {
  /** Draws the image at `size` and encodes it, or null when the browser cannot. */
  encode(size: Size, type: string, quality?: number): Promise<Blob | null>;
}

/** Below this a message has no room left for one more readable image. */
export const MIN_IMAGE_BUDGET = 64 * 1024;

/** Why an image was not made: the message's budget is spent, or it could not be made to fit. */
export class ShrinkError extends Error {}
export const BUDGET_SPENT = "This message's images are full";

/** Steps down, at most, before giving up. */
const MAX_SCALES = 8;

/**
 * The next scale to try after an encoding came out at `size` bytes over
 * `budget`: area falls with the square of the side, and 0.9 aims under.
 * Always at least a tenth smaller, so a stubborn encoder still converges.
 */
export function nextScale(scale: number, size: number, budget: number): number {
  return scale * Math.min(0.9, Math.sqrt(budget / size) * 0.9);
}

/**
 * The delivered variant within `budget`. A PNG source tries PNG once, at
 * full size (lossless when it fits). Then, at each scale, one lossy
 * encoding at its best quality — WebP, or JPEG where the browser cannot
 * make WebP; when that comes out at most LADDER over the budget, once more
 * at LADDER_QUALITY; otherwise the next scale is estimated from how far
 * over it came (nextScale). About three draws an image. Throws a
 * ShrinkError when the budget is below MIN_IMAGE_BUDGET, or nothing fits
 * after MAX_SCALES steps.
 */
export async function shrink(source: Size, sourceType: string, encoder: Encoder, maxSide: number, budget: number): Promise<{ blob: Blob; size: Size }> {
  if (budget < MIN_IMAGE_BUDGET) throw new ShrinkError(BUDGET_SPENT);
  const order = encodings(sourceType);
  let size = fit(source, maxSide);
  if (order[0]?.type === "image/png") {
    const blob = await encoder.encode(size, "image/png");
    if (blob?.type === "image/png" && blob.size <= budget) return { blob, size };
  }
  // The first lossy encoding the browser can make: WebP, else JPEG.
  let lossy = order.find((e) => e.type === "image/webp")!;
  const encode = async (quality: number) => {
    let blob = await encoder.encode(size, lossy.type, quality);
    // A browser that cannot encode WebP gives a PNG instead: not what was asked.
    if (blob?.type !== lossy.type && lossy.type === "image/webp") {
      lossy = order.find((e) => e.type === "image/jpeg")!;
      blob = await encoder.encode(size, lossy.type, quality);
    }
    return blob?.type === lossy.type ? blob : null;
  };
  let scale = 1;
  for (let step = 0; step < MAX_SCALES; step++) {
    const best = await encode(lossy.quality!);
    if (!best) break;
    if (best.size <= budget) return { blob: best, size };
    if (best.size <= budget * LADDER) {
      const lower = await encode(LADDER_QUALITY);
      if (lower && lower.size <= budget) return { blob: lower, size };
    }
    scale = nextScale(scale, best.size, budget);
    size = fit(source, maxSide, scale);
  }
  throw new ShrinkError("it could not be made small enough");
}

/** How far over its budget a lossy encoding may come and be tried once more at LADDER_QUALITY instead of smaller. */
const LADDER = 1.15;
const LADDER_QUALITY = 0.75;

/** An Encoder drawing a decoded image into one canvas, reused for every try. */
function canvasEncoder(bitmap: ImageBitmap): Encoder {
  const canvas = document.createElement("canvas");
  return {
    async encode(size, type, quality) {
      canvas.width = size.width;
      canvas.height = size.height;
      const ctx = canvas.getContext("2d");
      if (!ctx) return null;
      // JPEG has no alpha: transparent pixels would turn black.
      if (type === "image/jpeg") {
        ctx.fillStyle = "#ffffff";
        ctx.fillRect(0, 0, size.width, size.height);
      }
      ctx.imageSmoothingQuality = "high";
      ctx.drawImage(bitmap, 0, 0, size.width, size.height);
      return new Promise((resolve) => canvas.toBlob((b) => resolve(b), type, quality));
    },
  };
}

/** A tray chip, as the message's budget counts it. */
export interface BudgetChip {
  id: string;
  state: "uploading" | "ready" | "error";
  /** What its delivered variant weighs, once made. */
  deliveredBytes?: number | undefined;
}

/**
 * What chip `self` may take of the message's `messageBytes`: what the
 * other chips' made images leave, shared evenly with the chips still
 * waiting to be made (no deliveredBytes yet, not refused), so the first
 * images cannot starve the last. A refused chip counts for nothing.
 */
export function budgetFor(chips: ReadonlyArray<BudgetChip>, self: string, messageBytes: number): number {
  const live = chips.filter((c) => c.id !== self && c.state !== "error");
  const used = live.reduce((n, c) => n + (c.deliveredBytes ?? 0), 0);
  const waiting = live.filter((c) => c.deliveredBytes === undefined).length;
  return Math.max(0, Math.floor((messageBytes - used) / (1 + waiting)));
}

/** The chips a message took with it: those uploaded as one of `sent`. */
export function sentChips<C extends { attachmentId?: string | undefined }>(chips: ReadonlyArray<C>, sent: ReadonlyArray<string>): C[] {
  return chips.filter((c) => c.attachmentId !== undefined && sent.includes(c.attachmentId));
}

/** The name a re-encoded image goes by: its own, with the delivered type's extension. */
export function deliveredName(name: string, type: string): string {
  const ext = type === "image/jpeg" ? "jpg" : type.split("/")[1];
  return `${name.replace(/\.[a-z0-9]{1,5}$/i, "") || "image"}.${ext}`;
}

/** A decoded image to draw from: its size, an encoder over it, and how to let it go. */
export interface Decoded {
  size: Size;
  encoder: Encoder;
  close(): void;
}

/** Decodes `file` (a GIF to its first frame) and draws it into one canvas. */
async function decodeInCanvas(file: File): Promise<Decoded> {
  const bitmap = await createImageBitmap(file);
  return { size: { width: bitmap.width, height: bitmap.height }, encoder: canvasEncoder(bitmap), close: () => bitmap.close() };
}

/**
 * Make the delivered variant of `file` within `budget` bytes, and never
 * over `limits.deliveredBytes` whatever the budget.
 */
export async function prepare(file: File, limits: Limits, budget: number, decode: (file: File) => Promise<Decoded> = decodeInCanvas): Promise<Prepared> {
  const type = sniffImage(new Uint8Array(await file.slice(0, 16).arrayBuffer()));
  const room = Math.min(budget, limits.deliveredBytes);
  if (room < MIN_IMAGE_BUDGET) throw new ShrinkError(BUDGET_SPENT);
  const image = await decode(file);
  try {
    const made = await shrink(image.size, type ?? file.type, image.encoder, limits.maxSide, room);
    const name = file.name && file.name !== "image.png" ? file.name : `pasted-${new Date().toISOString().slice(11, 19).replaceAll(":", "")}.png`;
    return { name: deliveredName(name, made.blob.type), original: file, delivered: made.blob, ...made.size };
  } finally {
    image.close();
  }
}
