/**
 * Acceptance criteria as one Markdown list and back. The API keeps a list
 * of strings; what the editor opens with must read back as exactly those
 * strings, whatever a criterion holds.
 */

import { describe, expect, test } from "bun:test";
import { parseMarkdown } from "@dude/design-system";
import { criteriaFromMarkdown, criteriaToMarkdown, type ParsedCriteria } from "../src/screens/criteria.ts";

const from = (src: string) => criteriaFromMarkdown(src);
const roundTrip = (items: string[]) => from(criteriaToMarkdown(items)).items;

describe("criteriaFromMarkdown", () => {
  test("each top-level item is a criterion, whatever its marker", () => {
    expect(from("- one\n* two\n+ three\n1. four\n2) five").items).toEqual(["one", "two", "three", "four", "five"]);
  });

  test("a task marker comes off, open or done", () => {
    expect(from("- [ ] open\n- [x] done\n- [X] Done").items).toEqual(["open", "done", "Done"]);
  });

  test("only one task marker comes off: the rest is what it says", () => {
    expect(from("- [ ] [x] the literal text").items).toEqual(["[x] the literal text"]);
  });

  test("lines indented under an item stay with it, dedented", () => {
    const src = "- [ ] Each step fires its event once:\n  `plan_selected`, `payment_viewed`\n- [ ] Next";
    expect(from(src).items).toEqual(["Each step fires its event once:\n`plan_selected`, `payment_viewed`", "Next"]);
  });

  test("a nested list stays with its item", () => {
    expect(from("- parent\n  - child one\n  - child two\n- sibling").items).toEqual(["parent\n- child one\n- child two", "sibling"]);
  });

  test("an ordered item's continuation is indented to its content", () => {
    expect(from("10. ten\n    more\n11. eleven").items).toEqual(["ten\nmore", "eleven"]);
  });

  test("a blank line followed by indented text stays inside the item", () => {
    expect(from("- first\n\n  second paragraph\n- next").items).toEqual(["first\n\nsecond paragraph", "next"]);
  });

  test("blank lines between items separate them; trailing blanks are dropped", () => {
    expect(from("- a\n\n\n- b\n\n").items).toEqual(["a", "b"]);
  });

  test("a fence inside an item stays with it, even where its lines look like items", () => {
    const src = "- run it:\n  ```\n  - not an item\n  1. nor this\n\n  ```\n- after";
    expect(from(src).items).toEqual(["run it:\n```\n- not an item\n1. nor this\n\n```", "after"]);
  });

  test("empty items are dropped", () => {
    expect(from("- \n- [ ] \n- real\n-").items).toEqual(["real"]);
  });

  test("text outside a list item is stray, and not a criterion", () => {
    expect(from("Some intro\n- one")).toEqual({ items: ["one"], stray: true });
    expect(from("## Criteria\n\n- one")).toEqual({ items: ["one"], stray: true });
    expect(from("- one\n\nA closing note")).toEqual({ items: ["one"], stray: true });
    expect(from("- one\n- two")).toEqual({ items: ["one", "two"], stray: false });
    expect(from("")).toEqual({ items: [], stray: false });
    expect(from("\n  \n")).toEqual({ items: [], stray: false });
  });

  test("a stray fence is stray as a whole: list-like lines inside it are not criteria", () => {
    expect(from("```\n- inside\n```\n- outside")).toEqual({ items: ["outside"], stray: true });
  });

  test("a dash without a space is text, not an item", () => {
    expect(from("-not a list")).toEqual({ items: [], stray: true });
  });

  test("an unindented line straight after an item's text continues it, as Preview shows", () => {
    expect(from("- first line\ncontinuation line\n- second")).toEqual({ items: ["first line\ncontinuation line", "second"], stray: false });
    expect(from("1. one\ntwo\nthree")).toEqual({ items: ["one\ntwo\nthree"], stray: false });
    expect(from("- a\n  indented\nlazy")).toEqual({ items: ["a\nindented\nlazy"], stray: false });
  });

  test("a line that opens a block, or follows a blank line, does not continue the item", () => {
    expect(from("- one\n\nseparate")).toEqual({ items: ["one"], stray: true });
    expect(from("- one\n# heading")).toEqual({ items: ["one"], stray: true });
    expect(from("- one\n> quote")).toEqual({ items: ["one"], stray: true });
    expect(from("- one\n***")).toEqual({ items: ["one"], stray: true });
    expect(from("-\nafter an empty item")).toEqual({ items: [], stray: true });
  });

  test("a lazy continuation reads back indented, as the same criterion", () => {
    const first = from("- first line\ncontinuation line\n- second").items;
    expect(criteriaToMarkdown(first)).toBe("- [ ] first line\n  continuation line\n- [ ] second");
    expect(roundTrip(first)).toEqual(first);
  });

  test("a list written with CRLF line endings is the same criteria", () => {
    expect(from("- [ ] one\r\n- [x] two\r\n  more\r\n")).toEqual({ items: ["one", "two\r\nmore"], stray: false });
  });

  test("around a fence in an item, the saved criterion holds what Preview shows in it", () => {
    const cases: [string, ParsedCriteria][] = [
      ["- ```\n  code\nlazy", { items: ["```\ncode\nlazy"], stray: false }],
      ["- ```\n  code\n  ```\nlazy", { items: ["```\ncode\n```\nlazy"], stray: false }],
      ["- ```\n  code\n\ntext", { items: ["```\ncode"], stray: true }],
      ["- intro\n  ```\n  code\nlazy\n- next", { items: ["intro\n```\ncode\nlazy", "next"], stray: false }],
    ];
    for (const [src, expected] of cases) {
      const saved = from(src);
      expect(saved).toEqual(expected);
      const list = parseMarkdown(src)[0];
      if (list?.t !== "list") throw new Error(`no list parsed from ${JSON.stringify(src)}`);
      expect(saved.items.map((c) => parseMarkdown(c))).toEqual(list.items.map((item) => item.c));
    }
  });
});

describe("criteriaToMarkdown", () => {
  test("each criterion an open task, its other lines indented two spaces", () => {
    expect(criteriaToMarkdown(["one", "two\nmore", "three\n\nafter a gap"])).toBe("- [ ] one\n- [ ] two\n  more\n- [ ] three\n\n  after a gap");
  });

  test("nothing is an empty editor", () => {
    expect(criteriaToMarkdown([])).toBe("");
  });
});

describe("the round trip is stable", () => {
  const shapes: string[][] = [
    ["Greets with the full name"],
    ["Card, SEPA and Invoice all appear", "Invoice appears **only** for annual plans"],
    ["A criterion\nover two lines"],
    ["- starts with a dash"],
    ["* starts with a star", "+ starts with a plus"],
    ["1. starts with a number", "2) and another"],
    ["line one\n- a dash line\n1. a number line\n  indented already"],
    ["uses `code spans` and `- dashes` inside"],
    ["[ ] looks like a task marker", "[x] looks like a done one"],
    ["a fence\n```\n- item-like\n```\nafter it"],
    ["an unterminated fence\n```\nstill code"],
    ["```", "a criterion after an open fence"],
    ["text then\n~~~ open", "next"],
    ["~~~\ntilde fence\n~~~"],
    ["two paragraphs\n\nwith a blank line"],
    ["tabs\tinside\n\tand leading"],
    ["# a heading-like line", "> a quote-like line"],
    ["trailing spaces  \nhard break"],
    ["Windows\r\nline ending"],
    ["unicode: café — ✓ 日本語"],
    ["x".repeat(2000)],
  ];
  for (const items of shapes) {
    test(JSON.stringify(items).slice(0, 70), () => {
      expect(roundTrip(items)).toEqual(items);
      expect(from(criteriaToMarkdown(items)).stray).toBe(false);
    });
  }

  test("criteria are trimmed and empties dropped on the way in, then stable", () => {
    expect(roundTrip(["  padded  ", "", "   ", "ok"])).toEqual(["padded", "ok"]);
  });

  test("random criteria built from Markdown's awkward pieces survive", () => {
    // A seeded generator (mulberry32), so a failure names the same case every run.
    let seed = 0x2f6e2b1;
    const rand = (n: number) => {
      seed = (seed + 0x6d2b79f5) | 0;
      let t = Math.imul(seed ^ (seed >>> 15), 1 | seed);
      t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
      return (((t ^ (t >>> 14)) >>> 0) / 4294967296) * n | 0;
    };
    const pieces = ["-", "- ", "* ", "+ ", "1. ", "2) ", "[ ] ", "[x] ", "```", "~~~", "`x`", "**b**", "  ", "\t", "#", "> ", "word", "text", "\n", "\n\n", "\n  ", "\n- ", "\n1. ", "\n```\n", " "];
    for (let n = 0; n < 2000; n++) {
      const items = Array.from({ length: 1 + rand(4) }, () => Array.from({ length: 1 + rand(8) }, () => pieces[rand(pieces.length)]).join(""))
        .map((c) => c.trim())
        .filter(Boolean);
      const md = criteriaToMarkdown(items);
      const back = from(md);
      if (JSON.stringify(back.items) !== JSON.stringify(items)) {
        throw new Error(`round trip changed ${JSON.stringify(items)} via ${JSON.stringify(md)} into ${JSON.stringify(back.items)}`);
      }
      expect(back.stray).toBe(false);
    }
  });
});

describe("the saved criteria are never longer than their source", () => {
  // The editor bounds the source; the server bounds the criteria's total. This holds the second under the first.
  test("random sources built from Markdown's awkward pieces", () => {
    let seed = 0x51ce5ad;
    const rand = (n: number) => {
      seed = (seed + 0x6d2b79f5) | 0;
      let t = Math.imul(seed ^ (seed >>> 15), 1 | seed);
      t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
      return (((t ^ (t >>> 14)) >>> 0) / 4294967296) * n | 0;
    };
    const pieces = ["- ", "* ", "1. ", "10. ", "- [ ] ", "[x] ", "```", "~~~", "  ", "    ", "\t", "\t\t", "> ", "# ", "word", "é", "😀", "\n", "\n\n", "\n  ", "\n\t", "\n- ", "\r\n", " "];
    for (let n = 0; n < 3000; n++) {
      const source = Array.from({ length: 1 + rand(30) }, () => pieces[rand(pieces.length)]).join("");
      const saved = from(source).items.reduce((sum, c) => sum + c.length, 0);
      if (saved > source.length) throw new Error(`${JSON.stringify(source)} saves ${saved} characters from ${source.length}`);
    }
  });
});
