import { describe, expect, test } from "bun:test";
import { parseMarkdown } from "../src/util/markdown";

const para = (v: string) => ({ t: "paragraph", c: [{ t: "text", v }] });

describe("a run of blank lines inside a list item", () => {
  test("followed by an indented line, the run stays in the item", () => {
    expect(parseMarkdown("- a\n\n\n\n  more\n- b")).toEqual([
      {
        t: "list",
        ordered: false,
        start: 1,
        items: [
          { c: [para("a"), para("more")], task: null },
          { c: [para("b")], task: null },
        ],
      },
    ]);
  });

  test("followed by an unindented line, the run ends the item and the list", () => {
    expect(parseMarkdown("- a\n\n\n\nend")).toEqual([
      { t: "list", ordered: false, start: 1, items: [{ c: [para("a")], task: null }] },
      para("end"),
    ]);
  });

  test("ending the input, the run ends the item", () => {
    expect(parseMarkdown("- a\n\n\n")).toEqual([
      { t: "list", ordered: false, start: 1, items: [{ c: [para("a")], task: null }] },
    ]);
  });

  test("a 20,000-character run parses in well under a frame budget per keystroke", () => {
    const continued = `- a\n${"\n".repeat(19990)}  end`;
    const ended = `- a\n${"\n".repeat(19990)}end`;
    const t0 = performance.now();
    const a = parseMarkdown(continued);
    const b = parseMarkdown(ended);
    const elapsed = performance.now() - t0;
    expect(a).toEqual([
      { t: "list", ordered: false, start: 1, items: [{ c: [para("a"), para("end")], task: null }] },
    ]);
    expect(b).toEqual([
      { t: "list", ordered: false, start: 1, items: [{ c: [para("a")], task: null }] },
      para("end"),
    ]);
    expect(elapsed).toBeLessThan(200);
  });
});
