/**
 * dude's check of what a preview's overlay posts (previewOverlayProtocol.ts
 * says what each message is). The anchor is checked by the very schema a
 * draft's anchor is checked by in the API (feedback.ts), so nothing the
 * page sends reaches dude's API unless the API would take it as a person's
 * own; the picture is a PNG or JPEG data URL under its cap.
 */

import { z } from "zod";
import { FEEDBACK_LIMITS, previewAnchorInput } from "./feedback.ts";
import { OVERLAY_LIMITS, type OverlayToDude } from "./previewOverlayProtocol.ts";

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
]) as z.ZodType<OverlayToDude>;

/**
 * Whether `value` is larger than `cap`: its strings counted in UTF-16 code
 * units, every other value and key as 8. The walk stops as soon as it is
 * over, and at a depth past 32 (a page's message has no such nesting), so
 * a hostile message costs no more than the cap to measure.
 */
export function overSize(value: unknown, cap: number): boolean {
  let total = 0;
  const walk = (v: unknown, depth: number): boolean => {
    if (depth > 32) return true;
    if (typeof v === "string") total += v.length;
    else if (v !== null && typeof v === "object") {
      total += 8;
      if (total > cap) return true;
      for (const [k, x] of Array.isArray(v) ? v.entries() : Object.entries(v)) {
        total += typeof k === "string" ? k.length : 8;
        if (total > cap || walk(x, depth + 1)) return true;
      }
    } else total += 8;
    return total > cap;
  };
  return walk(value, 0);
}
