import { expect, test } from "bun:test";
import { FEEDBACK_LIMITS, OVERLAY_LIMITS, feedbackDraftSchema, overSize, overlayMessageSchema } from "../src/index.ts";

const anchor = { kind: "preview", url: "https://web.preview.example/cart", selector: "button.pay", viewport: { width: 1280, height: 720 } };

test("the overlay's limits are the API's: the overlay bundles no zod and repeats them", () => {
  expect(OVERLAY_LIMITS.body).toBe(FEEDBACK_LIMITS.body);
  expect(OVERLAY_LIMITS.previewText).toBe(FEEDBACK_LIMITS.previewText);
  expect(OVERLAY_LIMITS.previewHtml).toBe(FEEDBACK_LIMITS.previewHtml);
});

test("a draft message's anchor is checked by the API's own preview anchor rule, picture apart", () => {
  const ok = { type: "draft", v: 1, nonce: "n1", body: "Green", anchor, picture: "data:image/png;base64,iVBORw0KGgo=" };
  expect(overlayMessageSchema.safeParse(ok).success).toBe(true);
  // Every anchor the overlay may send, the API takes as a draft's.
  expect(feedbackDraftSchema.safeParse({ body: "Green", anchor }).success).toBe(true);
  for (const bad of [
    { ...ok, anchor: { ...anchor, attachmentId: "att_someone_elses" } },
    { ...ok, anchor: { ...anchor, url: "javascript:alert(1)" } },
    { ...ok, anchor: { ...anchor, extra: 1 } },
    { ...ok, anchor: { ...anchor, text: "x".repeat(FEEDBACK_LIMITS.previewText + 1) } },
    { ...ok, picture: "data:image/svg+xml;base64,PHN2Zz4=" },
    { ...ok, picture: `data:image/png;base64,${"A".repeat(OVERLAY_LIMITS.picture)}` },
    { ...ok, body: "   " },
    { ...ok, v: 2 },
    { ...ok, token: "x" },
  ]) expect(overlayMessageSchema.safeParse(bad).success).toBe(false);
});

test("overSize counts a message's strings and stops once past the cap", () => {
  expect(overSize({ a: "x".repeat(100) }, 200)).toBe(false);
  expect(overSize({ a: "x".repeat(300) }, 200)).toBe(true);
  expect(overSize([["x".repeat(150)], ["y".repeat(150)]], 200)).toBe(true);
  let deep: unknown = "x";
  for (let i = 0; i < 40; i++) deep = [deep];
  expect(overSize(deep, 10_000)).toBe(true);
});
