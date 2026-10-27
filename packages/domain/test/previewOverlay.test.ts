import { expect, test } from "bun:test";
import { FEEDBACK_LIMITS, OVERLAY_LIMITS, feedbackDraftSchema, overSize, overlayMessageSchema, overlaySessionSchema, overlayWindowName } from "../src/index.ts";

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

test("a session names dude's theme as light or dark, or none for an older dude; any other value is refused", () => {
  const session = { type: "session", v: 1, task: "WC-1", url: "https://web.preview.example/", pins: [], drafts: 0, showPins: true };
  expect(overlaySessionSchema.safeParse(session).success).toBe(true);
  for (const theme of ["light", "dark"]) expect(overlaySessionSchema.safeParse({ ...session, theme }).success).toBe(true);
  for (const theme of ["system", "Dark", "", 1, null, "sepia"]) expect(overlaySessionSchema.safeParse({ ...session, theme }).success).toBe(false);
  expect(overlaySessionSchema.safeParse({ ...session, colour: "red" }).success).toBe(false);
});

test("a session names dude's tab's window within OVERLAY_LIMITS.windowName, or none for an older dude", () => {
  const session = { type: "session", v: 1, task: "WC-1", url: "https://web.preview.example/", pins: [], drafts: 0, showPins: true };
  for (const dudeWindow of ["dude-tab-0b7c", "a", "x".repeat(OVERLAY_LIMITS.windowName)]) {
    expect(overlayWindowName(dudeWindow)).toBe(true);
    expect(overlaySessionSchema.safeParse({ ...session, dudeWindow }).success).toBe(true);
  }
  // `_blank` and the like would open a new tab; a space, a slash or an over-long name is no name dude gives itself.
  for (const dudeWindow of ["", "_blank", "_top", "dude tab", "dude/tab", "x".repeat(OVERLAY_LIMITS.windowName + 1), 7, null]) {
    expect(overlayWindowName(dudeWindow)).toBe(false);
    expect(overlaySessionSchema.safeParse({ ...session, dudeWindow }).success).toBe(false);
  }
});

test("overSize counts a message's strings and stops once past the cap", () => {
  expect(overSize({ a: "x".repeat(100) }, 200)).toBe(false);
  expect(overSize({ a: "x".repeat(300) }, 200)).toBe(true);
  expect(overSize([["x".repeat(150)], ["y".repeat(150)]], 200)).toBe(true);
  let deep: unknown = "x";
  for (let i = 0; i < 40; i++) deep = [deep];
  expect(overSize(deep, 10_000)).toBe(true);
});

test("overSize refuses a typed array or buffer outright, and stops counting after a message's worth of entries", () => {
  const typed = new Uint8Array(10_000_000);
  // Allow prototype checks, but reject property reads and key enumeration.
  const untouchable = (target: object) =>
    new Proxy(target, {
      get: () => { throw new Error("an element was read"); },
      has: () => { throw new Error("a key was tested"); },
      ownKeys: () => { throw new Error("the keys were listed"); },
      getOwnPropertyDescriptor: () => { throw new Error("a property was described"); },
    });
  expect(overSize({ type: "draft", x: untouchable(typed) }, OVERLAY_LIMITS.message)).toBe(true);
  expect(overSize({ type: "draft", x: untouchable(typed.buffer) }, OVERLAY_LIMITS.message)).toBe(true);
  expect(overSize({ type: "draft", x: typed }, OVERLAY_LIMITS.message)).toBe(true);
  expect(overSize({ type: "draft", x: typed.buffer }, OVERLAY_LIMITS.message)).toBe(true);
  // Even one byte is refused under a huge cap: type, not size, decides.
  expect(overSize({ x: new Uint8Array(1) }, 1e9)).toBe(true);
  expect(overSize({ x: new ArrayBuffer(1) }, 1e9)).toBe(true);
  expect(overSize(new Map([["a", 1]]), OVERLAY_LIMITS.message)).toBe(true);
  // 3,000 tiny entries are far under the cap in size, and still over: no overlay message has that many.
  const many: Record<string, number> = {};
  for (let i = 0; i < 3_000; i++) many[`k${i}`] = i;
  expect(overSize(many, 1e9)).toBe(true);
  expect(overSize(Array.from({ length: 3_000 }, () => 0), 1e9)).toBe(true);
  // A million keys may be listed, but at most 2,000 values may be read.
  const keys: Record<string, number> = {};
  for (let i = 0; i < 1_000_000; i++) keys[`k${i}`] = i;
  let reads = 0;
  const counted = new Proxy(keys, { get: (o, k) => { reads++; return Reflect.get(o, k); } });
  expect(overSize({ type: "draft", x: counted }, 1e9)).toBe(true);
  expect(reads).toBeLessThanOrEqual(2_000);
  // A plain message under the cap is not over.
  expect(overSize(Object.assign(Object.create(null), { a: "x" }), 100)).toBe(false);
});
