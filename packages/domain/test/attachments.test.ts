import { describe, expect, test } from "bun:test";
import { attachmentMarkdown, attachmentReferences, taskAttachmentIds } from "../src/attachments.ts";

const ids = (text: string) => attachmentReferences(text).map((r) => r.id);

describe("attachmentReferences", () => {
  test("a plain reference, with its name and place", () => {
    const text = "See ![login screen](attachment:att_x1) here.";
    expect(attachmentReferences(text)).toEqual([{ id: "att_x1", alt: "login screen", from: 4, to: 38 }]);
    expect(text.slice(4, 38)).toBe("![login screen](attachment:att_x1)");
  });

  test("titles and angle-bracket URLs are references", () => {
    expect(ids(`![a](attachment:att_a "title") ![b](<attachment:att_b>) ![c](attachment:att_c 'x') ![d](<attachment:att_d> (t))`))
      .toEqual(["att_a", "att_b", "att_c", "att_d"]);
  });

  test("an escaped bracket stays in the name", () => {
    expect(attachmentReferences("![a \\] b](attachment:att_z)")[0]?.alt).toBe("a ] b");
  });

  test("a code span is text, not a reference", () => {
    expect(ids("`![a](attachment:att_in)` and ![b](attachment:att_out) and ``x ![c](attachment:att_in2) ` y``")).toEqual(["att_out"]);
  });

  test("an unclosed backtick does not hide what follows", () => {
    expect(ids("a ` b ![c](attachment:att_c)")).toEqual(["att_c"]);
  });

  test("a fenced block is text, not a reference, with backticks or tildes", () => {
    const text = [
      "![one](attachment:att_1)",
      "```md",
      "![two](attachment:att_2)",
      "```",
      "~~~~",
      "![three](attachment:att_3)",
      "```",
      "~~~~",
      "![four](attachment:att_4)",
    ].join("\n");
    expect(ids(text)).toEqual(["att_1", "att_4"]);
  });

  test("other images and links are not references", () => {
    expect(ids("![x](https://e.com/a.png) [y](attachment:att_l) ![z](attachment:other) ![w]()")).toEqual([]);
  });
});

describe("taskAttachmentIds", () => {
  test("the goal's, then the criteria's, by first appearance, once each", () => {
    const goal = "![b](attachment:att_b) then ![a](attachment:att_a) and ![b](attachment:att_b)";
    expect(taskAttachmentIds(goal, ["![c](attachment:att_c)", "![a](attachment:att_a) ![d](attachment:att_d)"]))
      .toEqual(["att_b", "att_a", "att_c", "att_d"]);
  });
});

test("attachmentMarkdown names it so it parses back", () => {
  const md = attachmentMarkdown("shot [1].png", "att_q");
  expect(md).toBe("![shot (1).png](attachment:att_q)");
  expect(attachmentReferences(md)).toEqual([{ id: "att_q", alt: "shot (1).png", from: 0, to: md.length }]);
});
