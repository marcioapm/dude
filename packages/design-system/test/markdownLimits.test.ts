import { describe, expect, test } from "bun:test";
import { parseMarkdown, plain, type Block } from "../src/util/markdown";

const LIMIT = 65_536;
// Generous against the ~20 ms these take on a laptop; a quadratic scan takes seconds.
const BUDGET_MS = 250;

function text(blocks: readonly Block[]): string {
  return blocks.map((b) => (b.t === "paragraph" ? plain(b.c) : "")).join("");
}

function timed(src: string): { blocks: Block[]; ms: number } {
  const t0 = performance.now();
  const blocks = parseMarkdown(src);
  return { blocks, ms: performance.now() - t0 };
}

const fill = (unit: string) => unit.repeat(Math.floor(LIMIT / unit.length));

describe("unmatched inline markers at the 64K goal limit", () => {
  const adversarial: Array<[string, string]> = [
    ["[", fill("[")],
    ["![", fill("![")],
    ["[](", fill("[](")],
    ["[a](", fill("[a](")],
    ["*", fill("*")],
    ["*a ", fill("*a ")],
    ["_", fill("_")],
    ["_a", fill("_a")],
    ["**a", fill("**a")],
    ["~~a", fill("~~a")],
    ["`", fill("`")],
    ["`a", fill("`a")],
    ["``a", fill("``a")],
    ["<", fill("<")],
    ["<https://", fill("<https://")],
  ];

  for (const [name, src] of adversarial) {
    test(`${JSON.stringify(name)} repeated parses in under ${BUDGET_MS} ms`, () => {
      parseMarkdown(src.slice(0, 1000));
      const { ms } = timed(src);
      expect(ms).toBeLessThan(BUDGET_MS);
    });
  }

  test("unmatched brackets, parens and code ticks render literally", () => {
    for (const src of [fill("["), fill("!["), fill("[a]("), fill("<"), "[a] b (c", "a ``b", "![x](y"]) {
      expect(text(parseMarkdown(src))).toBe(src);
    }
  });

  test("unmatched emphasis markers render literally", () => {
    for (const src of ["*a", "_a b", "**a *b", "~~a"]) expect(text(parseMarkdown(src))).toBe(src);
  });

  test("a matched link after 64K of unmatched brackets still links", () => {
    const src = `${"[".repeat(LIMIT - 20)}[ok](https://x.io)`;
    const { blocks, ms } = timed(src);
    expect(ms).toBeLessThan(BUDGET_MS);
    const para = blocks[0];
    expect(para?.t).toBe("paragraph");
    if (para?.t !== "paragraph") return;
    expect(para.c.at(-1)).toEqual({ t: "link", href: "https://x.io", c: [{ t: "text", v: "ok" }] });
    expect(para.c[0]).toEqual({ t: "text", v: "[".repeat(LIMIT - 20) });
  });

  test("nested links, escaped brackets and code spans still resolve", () => {
    expect(parseMarkdown("[a [b] c](u) \\[x](v) `` a`b `` *e*")).toEqual([
      {
        t: "paragraph",
        c: [
          { t: "link", href: "u", c: [{ t: "text", v: "a [b] c" }] },
          { t: "text", v: " [x](v) " },
          { t: "code", v: "a`b" },
          { t: "text", v: " " },
          { t: "em", c: [{ t: "text", v: "e" }] },
        ],
      },
    ]);
  });
});
