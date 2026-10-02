/**
 * What the browser does to an image before it is uploaded: which files it
 * refuses, how large the agent's copy is drawn, and the order of encodings
 * it tries until the copy fits its budget.
 */

import { expect, test } from "bun:test";
import { ATTACHMENT_LIMITS } from "@dude/domain";
import {
  BUDGET_SPENT, MIN_IMAGE_BUDGET, ShrinkError, budgetFor, deliveredName, encodings, fit, nextScale, prepare, refuse, sentChips, shrink,
  sniffImage, type Encoder,
} from "../src/images.ts";

const bytes = (...b: number[]) => new Uint8Array(b);
const PNG = bytes(0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0);

test("an image's type is read from its bytes, not its name", () => {
  expect(sniffImage(PNG)).toBe("image/png");
  expect(sniffImage(bytes(0xff, 0xd8, 0xff, 0xe0))).toBe("image/jpeg");
  expect(sniffImage(new TextEncoder().encode("GIF89a......"))).toBe("image/gif");
  expect(sniffImage(new TextEncoder().encode("RIFF\0\0\0\0WEBPVP8 "))).toBe("image/webp");
  expect(sniffImage(new TextEncoder().encode("<svg xmlns="))).toBeNull();
  expect(sniffImage(new TextEncoder().encode("%PDF-1.7"))).toBeNull();
});

test("a file that is not one of the four types, or over 10 MB, is refused with why", async () => {
  expect(await refuse(new File([new TextEncoder().encode("%PDF-1.7")], "spec.pdf"), ATTACHMENT_LIMITS))
    .toEqual({ short: "PDF not supported", detail: "only PNG, JPEG, WebP and GIF can be sent" });
  // Named .png, but not one.
  expect((await refuse(new File([new TextEncoder().encode("<svg/>")], "fake.png"), ATTACHMENT_LIMITS))?.detail)
    .toBe("only PNG, JPEG, WebP and GIF can be sent");
  const big = new File([PNG, new Uint8Array(38_000_000)], "big.png");
  expect(await refuse(big, ATTACHMENT_LIMITS)).toEqual({ short: "38 MB · max 10", detail: "one is over 10 MB" });
  expect(await refuse(new File([PNG, new Uint8Array(10_000_000 - PNG.length)], "edge.png"), ATTACHMENT_LIMITS)).toBeNull();
});

test("the agent's copy is at most 2000 px on its long side, never enlarged", () => {
  expect(fit({ width: 2400, height: 1520 }, 2000)).toEqual({ width: 2000, height: 1267 });
  expect(fit({ width: 1520, height: 4000 }, 2000)).toEqual({ width: 760, height: 2000 });
  expect(fit({ width: 900, height: 900 }, 2000)).toEqual({ width: 900, height: 900 });
  expect(fit({ width: 900, height: 900 }, 2000, 0.5)).toEqual({ width: 450, height: 450 });
});

test("a PNG is tried as a PNG first; everything else goes lossy, WebP then JPEG, quality falling", () => {
  expect(encodings("image/png")[0]).toEqual({ type: "image/png" });
  expect(encodings("image/gif")[0]).toEqual({ type: "image/webp", quality: 0.92 });
  const qualities = encodings("image/jpeg").map((e) => e.quality!);
  expect(qualities).toEqual([...qualities].sort((a, b) => b - a));
  expect(encodings("image/jpeg").every((e) => e.type !== "image/png")).toBe(true);
});

/** An encoder whose output weighs one byte per pixel, times a factor per type and quality. */
function weighing(factor: (type: string, quality?: number) => number, unsupported: string[] = []): Encoder & { tried: string[] } {
  const tried: string[] = [];
  return {
    tried,
    async encode(size, type, quality) {
      tried.push(`${type}@${quality ?? "-"} ${size.width}x${size.height}`);
      const actual = unsupported.includes(type) ? "image/png" : type;
      return new Blob([new Uint8Array(Math.round(size.width * size.height * factor(actual, quality)))], { type: actual });
    },
  };
}

test("a PNG that fits stays a PNG at full size", async () => {
  const e = weighing(() => 0.5);
  const made = await shrink({ width: 1200, height: 760 }, "image/png", e, 2000, 4.5 * 1024 * 1024);
  expect(made.blob.type).toBe("image/png");
  expect(made.size).toEqual({ width: 1200, height: 760 });
});

test("one too heavy as a PNG becomes WebP, and smaller still if it must, until it fits the budget", async () => {
  const e = weighing((type, q) => (type === "image/png" ? 4 : 2 * (q ?? 1)));
  const made = await shrink({ width: 2000, height: 2000 }, "image/png", e, 2000, 4 * 1024 * 1024);
  expect(made.blob.size).toBeLessThanOrEqual(4 * 1024 * 1024);
  expect(made.blob.type).toBe("image/webp");
  // A message's budget left small by earlier images: scaled down to fit.
  const tight = await shrink({ width: 2000, height: 2000 }, "image/png", e, 2000, 300_000);
  expect(tight.blob.size).toBeLessThanOrEqual(300_000);
  expect(tight.size.width).toBeLessThan(2000);
});

test("a browser that cannot encode WebP gives PNG for it: that is not taken, JPEG is", async () => {
  const e = weighing((type) => (type === "image/png" ? 4 : 1), ["image/webp"]);
  const made = await shrink({ width: 2000, height: 1000 }, "image/png", e, 2000, 3 * 1024 * 1024);
  expect(made.blob.type).toBe("image/jpeg");
});

test("the next scale is estimated from how far over the last try came, and always smaller", () => {
  // Four times over: half the side, aiming under.
  expect(nextScale(1, 4_000_000, 1_000_000)).toBeCloseTo(0.45);
  // Barely over: still a tenth smaller.
  expect(nextScale(0.5, 1_010_000, 1_000_000)).toBeCloseTo(0.45);
});

const MiB = 1024 * 1024;
const LIMITS = { ...ATTACHMENT_LIMITS, messageBytes: 5 * MiB, deliveredBytes: 4.5 * MiB };

test("an image's budget is what the message's other images leave, shared with those still being made", () => {
  const self = { id: "me", state: "uploading" as const };
  // Two made at 2 MiB each: 1 MiB left.
  expect(budgetFor([{ id: "a", state: "ready", deliveredBytes: 2 * MiB }, { id: "b", state: "ready", deliveredBytes: 2 * MiB }, self], "me", 5 * MiB)).toBe(MiB);
  // A refused one weighs nothing, even once made.
  expect(budgetFor([{ id: "a", state: "ready", deliveredBytes: 2 * MiB }, { id: "x", state: "error", deliveredBytes: 2 * MiB }, self], "me", 5 * MiB)).toBe(3 * MiB);
  // Its own earlier size does not count against it.
  expect(budgetFor([{ id: "a", state: "ready", deliveredBytes: 2 * MiB }, { ...self, deliveredBytes: 3 * MiB }], "me", 5 * MiB)).toBe(3 * MiB);
  // Two more waiting to be made: a third each of what is left.
  expect(budgetFor([{ id: "a", state: "ready", deliveredBytes: 2 * MiB }, self, { id: "w1", state: "uploading" }, { id: "w2", state: "uploading" }], "me", 5 * MiB)).toBe(MiB);
  // Over: nothing.
  expect(budgetFor([{ id: "a", state: "ready", deliveredBytes: 6 * MiB }, self], "me", 5 * MiB)).toBe(0);
});

const jpegFile = (name: string) => new File([bytes(0xff, 0xd8, 0xff, 0xe0, 0, 0)], name);
const pngFile = (name: string) => new File([PNG], name);

/** A decoder standing in for createImageBitmap: the image's size, and an encoder over it. */
const decoded = (size: { width: number; height: number }, encoder: Encoder) => async () => ({ size, encoder, close() {} });

test("an image is never made over the delivered limit, whatever the message has left", async () => {
  // WebP at full size weighs 4.8 MiB: over 4.5 MiB, under the 5 MiB budget.
  const e = weighing((type, q) => (type === "image/png" ? 3 : (4.8 * MiB) / (2000 * 1500) * ((q ?? 1) / 0.92)));
  const made = await prepare(jpegFile("big.jpg"), LIMITS, 5 * MiB, decoded({ width: 2000, height: 1500 }, e));
  expect(made.delivered.size).toBeLessThanOrEqual(LIMITS.deliveredBytes);
});

test("a message with no room left refuses an image before drawing it", async () => {
  const e = weighing(() => 1);
  let decodedOnce = false;
  const decode = async () => {
    decodedOnce = true;
    return { size: { width: 10, height: 10 }, encoder: e, close() {} };
  };
  await expect(prepare(pngFile("late.png"), LIMITS, MIN_IMAGE_BUDGET - 1, decode)).rejects.toThrow(new ShrinkError(BUDGET_SPENT));
  expect(decodedOnce).toBe(false);
  expect(e.tried).toEqual([]);
});

/**
 * Six images attached at once, made one after another as the tray does:
 * each with its budgetFor share. The size model is the performance
 * review's: a PNG weighs `png` bytes a pixel, a lossy encoding 0.45 × its
 * quality.
 */
async function attachSix(source: { width: number; height: number }, file: () => File, png: number) {
  const chips: Array<{ id: string; state: "uploading" | "ready" | "error"; deliveredBytes?: number }> =
    Array.from({ length: 6 }, (_, i) => ({ id: `c${i}`, state: "uploading" }));
  const out: Array<{ encodes: number; long: number; bytes: number }> = [];
  for (const chip of chips) {
    const e = weighing((type, q) => (type === "image/png" ? png : 0.45 * (q ?? 1)));
    const made = await prepare(file(), LIMITS, budgetFor(chips, chip.id, LIMITS.messageBytes), decoded(source, e));
    chip.state = "ready";
    chip.deliveredBytes = made.delivered.size;
    out.push({ encodes: e.tried.length, long: Math.max(made.width, made.height), bytes: made.delivered.size });
  }
  return out;
}

test("six phone photos: at most 3 draws each, none starved, together within the message", async () => {
  const six = await attachSix({ width: 4032, height: 3024 }, () => jpegFile("photo.jpg"), 3);
  expect(Math.max(...six.map((i) => i.encodes))).toBeLessThanOrEqual(3);
  expect(Math.min(...six.map((i) => i.long))).toBeGreaterThanOrEqual(1400);
  expect(six.reduce((n, i) => n + i.bytes, 0)).toBeLessThanOrEqual(LIMITS.messageBytes);
});

test("six retina screenshots: at most 3 draws each, none starved, together within the message", async () => {
  const six = await attachSix({ width: 3024, height: 1964 }, () => pngFile("shot.png"), 1.2);
  expect(Math.max(...six.map((i) => i.encodes))).toBeLessThanOrEqual(3);
  expect(Math.min(...six.map((i) => i.long))).toBeGreaterThanOrEqual(1400);
  expect(six.reduce((n, i) => n + i.bytes, 0)).toBeLessThanOrEqual(LIMITS.messageBytes);
});

test("a message's chips are cleared only for the images it took", () => {
  const chips = [{ id: "c1", attachmentId: "att_1" }, { id: "c2", attachmentId: "att_2" }, { id: "c3" }];
  // c2 was pasted while the message was on its way.
  expect(sentChips(chips, ["att_1"]).map((c) => c.id)).toEqual(["c1"]);
});

test("the agent's copy is named for what it is", () => {
  expect(deliveredName("checkout.png", "image/webp")).toBe("checkout.webp");
  expect(deliveredName("photo.JPEG", "image/jpeg")).toBe("photo.jpg");
  expect(deliveredName("anim.gif", "image/png")).toBe("anim.png");
});
