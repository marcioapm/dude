/**
 * Images a person attaches, made ready in the browser before they are
 * uploaded: checked, scaled and re-encoded into the variant the agent is
 * sent. The original goes up as it was picked.
 *
 * The delivered variant is drawn into a canvas — so EXIF and any other
 * metadata are gone, and a GIF is its first frame — at most `maxSide` on its
 * long side. A PNG stays a PNG when it fits; otherwise WebP, then JPEG, at
 * decreasing quality, then a smaller scale, until it is at most
 * `deliveredBytes` and the message's images together stay under
 * `messageBytes`.
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

/**
 * The smallest acceptable delivered variant: the first encoding at full
 * scale that fits `budget`, else the same at 0.8, 0.64 … of the size.
 */
export async function shrink(source: Size, sourceType: string, encoder: Encoder, maxSide: number, budget: number): Promise<{ blob: Blob; size: Size } | null> {
  for (let scale = 1; scale > 0.05; scale *= 0.8) {
    const size = fit(source, maxSide, scale);
    for (const e of encodings(sourceType)) {
      const blob = await encoder.encode(size, e.type, e.quality);
      // A browser that cannot encode WebP gives a PNG instead: not what was asked.
      if (blob && blob.type === e.type && blob.size <= budget) return { blob, size };
    }
  }
  return null;
}

/** An Encoder drawing a decoded image into a canvas. */
function canvasEncoder(bitmap: ImageBitmap): Encoder {
  return {
    async encode(size, type, quality) {
      const canvas = document.createElement("canvas");
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

/** The name a re-encoded image goes by: its own, with the delivered type's extension. */
export function deliveredName(name: string, type: string): string {
  const ext = type === "image/jpeg" ? "jpg" : type.split("/")[1];
  return `${name.replace(/\.[a-z0-9]{1,5}$/i, "") || "image"}.${ext}`;
}

/**
 * Make the delivered variant of `file` within `budget` bytes. GIF and every
 * other type are decoded to their first frame by createImageBitmap.
 */
export async function prepare(file: File, limits: Limits, budget: number): Promise<Prepared> {
  const type = sniffImage(new Uint8Array(await file.slice(0, 16).arrayBuffer()));
  const bitmap = await createImageBitmap(file);
  try {
    const source = { width: bitmap.width, height: bitmap.height };
    const made = await shrink(source, type ?? file.type, canvasEncoder(bitmap), limits.maxSide, Math.min(budget, limits.deliveredBytes));
    if (!made) throw new Error("it could not be made small enough");
    const name = file.name && file.name !== "image.png" ? file.name : `pasted-${new Date().toISOString().slice(11, 19).replaceAll(":", "")}.png`;
    return { name: deliveredName(name, made.blob.type), original: file, delivered: made.blob, ...made.size };
  } finally {
    bitmap.close();
  }
}
