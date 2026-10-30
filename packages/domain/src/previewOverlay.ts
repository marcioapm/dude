/**
 * dude's check of what a preview's overlay posts (previewOverlayProtocol.ts
 * says what each message is). The anchor is checked by the very schema a
 * draft's anchor is checked by in the API (feedback.ts), so nothing the
 * page sends reaches dude's API unless the API would take it as a person's
 * own; the picture is a PNG or JPEG data URL under its cap.
 */

import { z } from "zod";
import { FEEDBACK_LIMITS, FEEDBACK_STATUSES, previewAnchorInput } from "./feedback.ts";
import { OVERLAY_LIMITS, OVERLAY_THEMES, overlayWindowName, type OverlayTheme, type OverlayToDude } from "./previewOverlayProtocol.ts";

export * from "./previewOverlayProtocol.ts";

const v1 = z.literal(1);
const pageUrl = z.string().url().max(2_048).refine((u) => /^https?:\/\//.test(u), "an http(s) URL");

export const overlayAnchorSchema = previewAnchorInput.omit({ attachmentId: true }).strict();

export const overlayPictureSchema = z.string().max(OVERLAY_LIMITS.picture)
  .regex(/^data:image\/(png|jpeg);base64,[A-Za-z0-9+/]+={0,2}$/, "a PNG or JPEG data URL");

export const overlayMessageSchema: z.ZodType<OverlayToDude> = z.discriminatedUnion("type", [
  z.object({ type: z.literal("hello"), v: v1, url: pageUrl }).strict(),
  z.object({ type: z.literal("navigated"), v: v1, url: pageUrl }).strict(),
  z.object({
    type: z.literal("draft"), v: v1, nonce: z.string().min(1).max(100),
    body: z.string().trim().min(1).max(FEEDBACK_LIMITS.body),
    anchor: overlayAnchorSchema,
    picture: overlayPictureSchema.optional(),
  }).strict(),
  z.object({ type: z.literal("prefs"), v: v1, showPins: z.boolean() }).strict(),
  z.object({ type: z.literal("open"), v: v1, itemId: z.string().min(1).max(100) }).strict(),
  z.object({ type: z.literal("left"), v: v1 }).strict(),
]) as z.ZodType<OverlayToDude>;

const overlayPinSchema = z.object({
  id: z.string().min(1).max(100), n: z.number().int().positive(), status: z.enum(FEEDBACK_STATUSES), author: z.string().max(OVERLAY_LIMITS.name),
  mine: z.boolean(), body: z.string().max(OVERLAY_LIMITS.pinBody), selector: z.string().max(2_000), when: z.string().max(OVERLAY_LIMITS.name),
  replies: z.array(z.object({ who: z.string().max(OVERLAY_LIMITS.name), kind: z.enum(["reply", "resolve", "reopen"]), body: z.string().max(OVERLAY_LIMITS.replyBody) }).strict())
    .max(OVERLAY_LIMITS.replies),
}).strict();

/**
 * What dude's session message to the overlay may be: strict, `theme` one of
 * OVERLAY_THEMES or absent, `dudeWindow` an overlayWindowName or absent.
 * dude builds it itself; this holds it to the protocol (the overlay, which
 * bundles no zod, guards the same by hand).
 */
export const overlaySessionSchema = z.object({
  type: z.literal("session"), v: v1, task: z.string().max(100), url: pageUrl,
  pins: z.array(overlayPinSchema).max(OVERLAY_LIMITS.pins), drafts: z.number().int().nonnegative(), showPins: z.boolean(),
  theme: z.enum(OVERLAY_THEMES as [OverlayTheme, ...OverlayTheme[]]).optional(),
  dudeWindow: z.string().refine(overlayWindowName, "a window name").optional(),
  mode: z.literal("branch").optional(),
}).strict();

const MAX_ENTRIES = 2_000;

/**
 * Whether `value` is larger than `cap`: its strings counted in UTF-16 code
 * units, every other value and key as 8. Only plain objects, arrays and
 * primitives are walked; anything else (a typed array, an ArrayBuffer, a
 * Map, a Blob) is over, since no overlay message carries one. Keys are
 * visited one at a time with `for…in` and the walk stops as soon as it is
 * over, past MAX_ENTRIES values, or past a depth of 32, so measuring costs
 * no more than the cap.
 */
export function overSize(value: unknown, cap: number): boolean {
  let total = 0;
  // Entries visited in all: an overlay message has a few dozen, so a page's many-keyed object is over long before the cap.
  let entries = 0;
  const walk = (v: unknown, depth: number): boolean => {
    if (depth > 32 || ++entries > MAX_ENTRIES) return true;
    if (typeof v === "string") total += v.length;
    else if (v !== null && typeof v === "object") {
      const array = Array.isArray(v);
      if (!array && Object.getPrototypeOf(v) !== Object.prototype && Object.getPrototypeOf(v) !== null) return true;
      total += 8;
      if (total > cap) return true;
      if (array) {
        for (let i = 0; i < v.length; i++) {
          total += 8;
          if (total > cap || walk(v[i], depth + 1)) return true;
        }
      } else {
        for (const k in v) {
          if (!Object.hasOwn(v, k)) continue;
          total += k.length;
          if (total > cap || walk((v as Record<string, unknown>)[k], depth + 1)) return true;
        }
      }
    } else total += 8;
    return total > cap;
  };
  return walk(value, 0);
}
