/**
 * What dude goes by on a task: one of his names, the same one every time
 * for a task, and not the same one for every task.
 */

import { describe, expect, test } from "bun:test";
import { DUDE_NAMES, dudeName } from "../src/DudeMark.tsx";

describe("dudeName", () => {
  test("a task always hears from the same dude", () => {
    expect(dudeName("wi_0mulrn85h3b8addb14d52496e")).toBe(dudeName("wi_0mulrn85h3b8addb14d52496e"));
  });

  test("is one of his names, and tasks get all of them", () => {
    const seen = new Set(Array.from({ length: 200 }, (_, i) => dudeName(`wi_${i.toString(36)}task`)));
    expect([...seen].every((n) => (DUDE_NAMES as readonly string[]).includes(n))).toBe(true);
    expect(seen.size).toBe(DUDE_NAMES.length);
  });
});
