/**
 * What an image file is, read from its bytes: its type by magic number and
 * its pixel size from its header. Nothing is decoded. A file that claims to
 * be an image and is not one, or is one of another type, is refused before
 * anything is stored.
 */

export type ImageType = "image/png" | "image/jpeg" | "image/webp" | "image/gif";

export const IMAGE_TYPES: readonly ImageType[] = ["image/png", "image/jpeg", "image/webp", "image/gif"];

export const EXTENSION: Record<ImageType, string> = {
  "image/png": "png",
  "image/jpeg": "jpg",
  "image/webp": "webp",
  "image/gif": "gif",
};

export interface ImageInfo {
  type: ImageType;
  width: number;
  height: number;
}

const ascii = (b: Uint8Array, at: number, text: string) =>
  [...text].every((c, i) => b[at + i] === c.charCodeAt(0));

/** The image type of `b` by its magic number, or null for anything else. */
export function sniff(b: Uint8Array): ImageType | null {
  if (b.length >= 8 && b[0] === 0x89 && ascii(b, 1, "PNG\r\n\x1a\n")) return "image/png";
  if (b.length >= 3 && b[0] === 0xff && b[1] === 0xd8 && b[2] === 0xff) return "image/jpeg";
  if (b.length >= 6 && (ascii(b, 0, "GIF87a") || ascii(b, 0, "GIF89a"))) return "image/gif";
  if (b.length >= 12 && ascii(b, 0, "RIFF") && ascii(b, 8, "WEBP")) return "image/webp";
  return null;
}

const u16be = (b: Uint8Array, at: number) => (b[at]! << 8) | b[at + 1]!;
const u16le = (b: Uint8Array, at: number) => b[at]! | (b[at + 1]! << 8);
const u24le = (b: Uint8Array, at: number) => b[at]! | (b[at + 1]! << 8) | (b[at + 2]! << 16);
const u32be = (b: Uint8Array, at: number) => ((b[at]! << 24) >>> 0) + (b[at + 1]! << 16) + (b[at + 2]! << 8) + b[at + 3]!;

function jpegSize(b: Uint8Array): [number, number] | null {
  let at = 2;
  while (at + 9 < b.length) {
    if (b[at] !== 0xff) return null;
    const marker = b[at + 1]!;
    // Fill bytes before a marker.
    if (marker === 0xff) {
      at++;
      continue;
    }
    // Markers with no length.
    if (marker === 0xd8 || marker === 0x01 || (marker >= 0xd0 && marker <= 0xd7)) {
      at += 2;
      continue;
    }
    const length = u16be(b, at + 2);
    // Start of frame (baseline, progressive, …), not DHT, JPG or DAC.
    if (marker >= 0xc0 && marker <= 0xcf && marker !== 0xc4 && marker !== 0xc8 && marker !== 0xcc) {
      return [u16be(b, at + 7), u16be(b, at + 5)];
    }
    if (marker === 0xda || length < 2) return null;
    at += 2 + length;
  }
  return null;
}

function webpSize(b: Uint8Array): [number, number] | null {
  if (b.length < 30) return null;
  if (ascii(b, 12, "VP8 ")) {
    if (b[23] !== 0x9d || b[24] !== 0x01 || b[25] !== 0x2a) return null;
    return [u16le(b, 26) & 0x3fff, u16le(b, 28) & 0x3fff];
  }
  if (ascii(b, 12, "VP8L")) {
    if (b[20] !== 0x2f) return null;
    const bits = b[21]! | (b[22]! << 8) | (b[23]! << 16) | (b[24]! << 24);
    return [(bits & 0x3fff) + 1, ((bits >>> 14) & 0x3fff) + 1];
  }
  if (ascii(b, 12, "VP8X")) return [u24le(b, 24) + 1, u24le(b, 27) + 1];
  return null;
}

/** The type and pixel size of `b`, or null when it is not a well-formed header of one of the four types. */
export function imageInfo(b: Uint8Array): ImageInfo | null {
  const type = sniff(b);
  let size: [number, number] | null = null;
  switch (type) {
    case "image/png":
      size = b.length >= 24 && ascii(b, 12, "IHDR") ? [u32be(b, 16), u32be(b, 20)] : null;
      break;
    case "image/gif":
      size = b.length >= 10 ? [u16le(b, 6), u16le(b, 8)] : null;
      break;
    case "image/jpeg":
      size = jpegSize(b);
      break;
    case "image/webp":
      size = webpSize(b);
      break;
    case null:
      return null;
  }
  if (!size || !(size[0] > 0) || !(size[1] > 0)) return null;
  return { type, width: size[0], height: size[1] };
}
