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

function depthOf(blocks: readonly Block[]): number {
  let d = 0;
  for (const b of blocks) {
    if (b.t === "quote") d = Math.max(d, 1 + depthOf(b.c));
    if (b.t === "list") for (const it of b.items) d = Math.max(d, 1 + depthOf(it.c));
  }
  return d;
}

function deepest(blocks: readonly Block[]): Block | undefined {
  const b = blocks.at(-1);
  if (b?.t === "quote") return deepest(b.c);
  if (b?.t === "list") return deepest(b.items.at(-1)?.c ?? []);
  return b;
}

describe("nesting at the 64K goal limit", () => {
  test("32,766 quote markers parse without throwing; the rest shows as literal text", () => {
    const src = `${"> ".repeat(32_766)}text`;
    expect(src.length).toBe(LIMIT);
    const { blocks, ms } = timed(src);
    expect(ms).toBeLessThan(BUDGET_MS);
    expect(depthOf(blocks)).toBe(33);
    const leaf = deepest(blocks);
    expect(leaf?.t).toBe("paragraph");
    if (leaf?.t !== "paragraph") return;
    expect(plain(leaf.c).endsWith("> > text")).toBe(true);
    expect(plain(leaf.c).length).toBe(LIMIT - 33 * 2);
  });

  test("lists nested by marker or by indentation parse; the rest shows as literal text", () => {
    const byMarker = `${"- ".repeat(32_766)}text`;
    const byIndent = Array.from({ length: 300 }, (_, i) => `${" ".repeat(i * 2)}- item ${i}`).join("\n").slice(0, LIMIT);
    const mixed = `${"> 1. ".repeat(13_106)}text`;
    for (const src of [byMarker, byIndent, mixed]) {
      const { blocks, ms } = timed(src);
      expect(ms).toBeLessThan(BUDGET_MS);
      expect(depthOf(blocks)).toBe(33);
      expect(deepest(blocks)?.t).toBe("paragraph");
    }
    const leaf = deepest(parseMarkdown(byIndent));
    expect(leaf?.t === "paragraph" && plain(leaf.c).startsWith("item 32\n- item 33\n  - item 34")).toBe(true);
  });

  test("ordinary nesting is unchanged", () => {
    expect(depthOf(parseMarkdown("> - a\n>   > b"))).toBe(3);
    expect(deepest(parseMarkdown("> > > x"))).toEqual({ t: "paragraph", c: [{ t: "text", v: "x" }] });
  });
});

describe("the nesting bound does not leak between paths that reach the same text", () => {
  // A failed emphasis scan parses the links one level deeper and caches the
  // result; the top-level pass that follows must not reuse the deeper,
  // earlier-truncated parse.
  const links = (n: number) => "[".repeat(n) + "x" + "](u)".repeat(n);
  const inner = (blocks: readonly Block[]) => {
    const p = blocks[0];
    if (p?.t !== "paragraph") throw new Error("expected a paragraph");
    return [...p.c];
  };

  test.each([1, 2, 16, 31, 32])("%i nested links read the same after an unmatched star", (n) => {
    const alone = inner(parseMarkdown(links(n)));
    const starred = inner(parseMarkdown("*" + links(n)));
    expect(starred[0]).toEqual({ t: "text", v: "*" });
    expect(starred.slice(1)).toEqual(alone);
  });

  test("so does emphasis inside them", () => {
    const src = "[".repeat(30) + "**x**" + "](u)".repeat(30);
    expect(inner(parseMarkdown("_" + src)).slice(1)).toEqual(inner(parseMarkdown(src)));
  });
});
