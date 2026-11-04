import { describe, expect, test } from "bun:test";
import { reviewFindingsNote } from "../src/screens/TaskScreen.tsx";

describe("a review step's findings note", () => {
  test("counts every finding at the fix bar as blocking: blocking, high, medium and a low marked trivial", () => {
    expect(reviewFindingsNote([
      { severity: "blocking" },
      { severity: "high" },
      { severity: "medium" },
      { severity: "low", trivial: true },
      { severity: "low", trivial: false },
      { severity: "low" },
      { severity: "note" },
    ])).toBe("4 blocking, 3 more");
  });

  test("a medium alone is blocking, with nothing more", () => {
    expect(reviewFindingsNote([{ severity: "medium" }])).toBe("1 blocking");
  });

  test("no findings says so", () => {
    expect(reviewFindingsNote([])).toBe("no findings");
  });
});
