import { expect, test } from "bun:test";
import { canChat, effectiveSessionRole, sessionAccessSchema, sessionMentionsSchema } from "../src/sessions.ts";

test("an organisation session lifts a reader to chat; a private one keeps each role", () => {
  expect(effectiveSessionRole("read", "organisation")).toBe("chat");
  expect(effectiveSessionRole("read", "private")).toBe("read");
  expect(effectiveSessionRole("owner", "organisation")).toBe("owner");
  expect(canChat("read", "private")).toBe(false);
  expect(canChat("read", "organisation")).toBe(true);
  expect(canChat("chat", "private")).toBe(true);
  expect(canChat(null, "organisation")).toBe(false);
});

test("access is private or organisation, nothing else", () => {
  expect(sessionAccessSchema.safeParse("organisation").success).toBe(true);
  expect(sessionAccessSchema.safeParse("everyone").success).toBe(false);
});

test("a message names at most 20 people to mention and 20 to share with, each a non-empty id", () => {
  expect(sessionMentionsSchema.safeParse({ mentions: ["per_a"], shareWith: ["per_a"] }).success).toBe(true);
  expect(sessionMentionsSchema.safeParse({ mentions: [""] }).success).toBe(false);
  expect(sessionMentionsSchema.safeParse({ mentions: Array.from({ length: 21 }, (_, i) => `per_${i}`) }).success).toBe(false);
});
