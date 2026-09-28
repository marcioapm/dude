/** Presence is cheap: a person's row is touched at most once a minute. */

import { expect, test } from "bun:test";
import { due } from "../src/api/presence.ts";

test("a person is touched at most once a minute", () => {
  const seen = new Map<string, number>();
  expect(due(seen, "a", 0)).toBe(true);
  expect(due(seen, "a", 59_999)).toBe(false);
  expect(due(seen, "b", 30_000)).toBe(true);
  expect(due(seen, "a", 60_000)).toBe(true);
  expect(due(seen, "a", 90_000)).toBe(false);
});
