/**
 * What the browser does to an image before it is uploaded: which files it
 * refuses, how large the agent's copy is drawn, and the order of encodings
 * it tries until the copy fits its budget.
 */

import { expect, test } from "bun:test";
import { ATTACHMENT_LIMITS } from "@dude/domain";
import { deliveredName, encodings, fit, refuse, shrink, sniffImage, type Encoder } from "../src/images.ts";

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
  expect(made?.blob.type).toBe("image/png");
  expect(made?.size).toEqual({ width: 1200, height: 760 });
});

test("one too heavy as a PNG becomes WebP, and smaller still if it must, until it fits the budget", async () => {
  const e = weighing((type, q) => (type === "image/png" ? 4 : 2 * (q ?? 1)));
  const made = await shrink({ width: 2000, height: 2000 }, "image/png", e, 2000, 4 * 1024 * 1024);
  expect(made!.blob.size).toBeLessThanOrEqual(4 * 1024 * 1024);
  expect(made!.blob.type).toBe("image/webp");
  // A message's budget left small by earlier images: scaled down to fit.
  const tight = await shrink({ width: 2000, height: 2000 }, "image/png", e, 2000, 300_000);
  expect(tight!.blob.size).toBeLessThanOrEqual(300_000);
  expect(tight!.size.width).toBeLessThan(2000);
});

test("a browser that cannot encode WebP gives PNG for it: that is not taken, JPEG is", async () => {
  const e = weighing((type) => (type === "image/png" ? 4 : 1), ["image/webp"]);
  const made = await shrink({ width: 2000, height: 1000 }, "image/png", e, 2000, 3 * 1024 * 1024);
  expect(made!.blob.type).toBe("image/jpeg");
});

test("the agent's copy is named for what it is", () => {
  expect(deliveredName("checkout.png", "image/webp")).toBe("checkout.webp");
  expect(deliveredName("photo.JPEG", "image/jpeg")).toBe("photo.jpg");
  expect(deliveredName("anim.gif", "image/png")).toBe("anim.png");
});
