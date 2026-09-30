import { describe, expect, test } from "bun:test";
import { parseMarkdown, type Block, type Inline } from "../src/util/markdown";

const t = (v: string): Inline => ({ t: "text", v });
const br: Inline = { t: "br" };
const para = (...c: Inline[]): Block => ({ t: "paragraph", c });
const on = (src: string) => parseMarkdown(src, { breaks: true });

describe("parseMarkdown breaks", () => {
  test("off (the default), a single newline in a paragraph is a space", () => {
    expect(parseMarkdown("one\ntwo")).toEqual([para(t("one two"))]);
    expect(parseMarkdown("one\ntwo", { breaks: false })).toEqual([para(t("one two"))]);
  });

  test("on, it is a line break in a paragraph, a list item and a quote", () => {
    expect(on("one\ntwo")).toEqual([para(t("one"), br, t("two"))]);
    expect(on("- one\n  two\n- three")).toEqual([
      { t: "list", ordered: false, start: 1, items: [{ c: [para(t("one"), br, t("two"))], task: null }, { c: [para(t("three"))], task: null }] },
    ]);
    expect(on("> one\n> two")).toEqual([{ t: "quote", c: [para(t("one"), br, t("two"))] }]);
  });

  test("on, a lazy continuation line of a list item breaks too", () => {
    expect(on("- [ ] one\ntwo")).toEqual([{ t: "list", ordered: false, start: 1, items: [{ c: [para(t("one"), br, t("two"))], task: false }] }]);
  });

  test("on, a break inside emphasis is a break, and the text stays emphasised", () => {
    expect(on("**one\ntwo**")).toEqual([para({ t: "strong", c: [t("one"), br, t("two")] })]);
  });

  test("a code block, a code span and a heading are unaffected", () => {
    expect(on("```\na\nb\n```")).toEqual([{ t: "code", lang: "", v: "a\nb", open: false }]);
    expect(on("`a\nb`")).toEqual([para({ t: "code", v: "a b" })]);
    expect(on("# Title\nbody")).toEqual([
      { t: "heading", level: 1, c: [t("Title")], id: "title" },
      para(t("body")),
    ]);
  });

  test("the hard break works either way, and on it is one break, not two", () => {
    for (const breaks of [false, true]) {
      expect(parseMarkdown("one  \ntwo", { breaks })).toEqual([para(t("one"), br, t("two"))]);
      expect(parseMarkdown("one\\\ntwo", { breaks })).toEqual([para(t("one"), br, t("two"))]);
    }
  });

  test("blank lines still separate paragraphs", () => {
    expect(on("one\n\ntwo")).toEqual([para(t("one")), para(t("two"))]);
  });
});
