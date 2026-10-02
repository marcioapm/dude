/**
 * An image's layout as its reference's title, and the edits Preview makes
 * to a field's source: run on the text, asserted on the text that results.
 */

import { describe, expect, test } from "bun:test";
import { cutReference, insertReference, layoutTitle, moveReference, moveReferenceTo, parseLayout, removeReference, slotCount, snapWidth, withLayout } from "../src/util/imageLayout.ts";
import { criteriaLines } from "../src/util/criteria.ts";

// What criteria.ts reads (kept here so the design system needs no app import): items by top-level marker.
const itemsOf = (src: string) => src.split("\n").filter((l) => /^[-*+] /.test(l)).length;

describe("layout words", () => {
  test("parse: defaults, unknown words ignored, full is centred, px clamped", () => {
    expect(parseLayout(undefined)).toEqual({ size: "medium", align: "center" });
    expect(parseLayout("small right")).toEqual({ size: "small", align: "right" });
    expect(parseLayout("wobbly 320  left zz")).toEqual({ size: 320, align: "left" });
    expect(parseLayout("full right")).toEqual({ size: "full", align: "center" });
    expect(parseLayout("40")).toEqual({ size: 120, align: "center" });
  });
  test("serialise drops defaults", () => {
    expect(layoutTitle({ size: "medium", align: "center" })).toBeUndefined();
    expect(layoutTitle({ size: "small", align: "right" })).toBe("small right");
    expect(layoutTitle({ size: 320, align: "left" })).toBe("320 left");
    expect(layoutTitle({ size: "full", align: "right" })).toBe("full");
    expect(layoutTitle({ size: "medium", align: "left" })).toBe("left");
  });
  test("withLayout rewrites only the title, the alt and angle brackets as written", () => {
    expect(withLayout('a ![x\\]y](<attachment:att_a> "wobbly") b', 0, { size: "small", align: "right" })).toBe('a ![x\\]y](<attachment:att_a> "small right") b');
    expect(withLayout("![x](attachment:att_a 'small') ![y](attachment:att_b)", 1, { size: "full", align: "left" })).toBe("![x](attachment:att_a 'small') ![y](attachment:att_b \"full\")");
    expect(withLayout('![x](attachment:att_a "small")', 0, { size: "medium", align: "center" })).toBe("![x](attachment:att_a)");
  });
  test("a dragged width snaps to Small and Medium within 10 px, and to Full at the edge", () => {
    expect(snapWidth(209, 700)).toBe("small");
    expect(snapWidth(211, 700)).toBe(211);
    expect(snapWidth(412, 700)).toBe("medium");
    expect(snapWidth(696, 700)).toBe("full");
    expect(snapWidth(80, 700)).toBe(120);
  });
});

const A = "![a.png](attachment:att_a)";
const B = "![b.png](attachment:att_b)";

describe("remove", () => {
  test("an image on its own paragraph leaves no stray blank lines", () => {
    expect(removeReference(`One.\n\n${A}\n\nTwo.`, 0)).toBe("One.\n\nTwo.");
    expect(removeReference(`One.\n\n${A}`, 0)).toBe("One.");
    expect(removeReference(`${A}\n\nOne.`, 0)).toBe("One.");
  });
  test("mid-sentence, the spaces close up", () => {
    expect(removeReference(`See ${A} here.`, 0)).toBe("See here.");
    expect(removeReference(`- [ ] Totals, as in ${A}\n- [ ] Next`, 0)).toBe("- [ ] Totals, as in\n- [ ] Next");
  });
  test("a criterion's continuation line goes, the list intact", () => {
    expect(removeReference(`- [ ] One\n  ${A}\n- [ ] Two`, 0)).toBe("- [ ] One\n- [ ] Two");
  });
  test("an image that is a criterion's whole text takes the empty item with it", () => {
    expect(removeReference(`- [ ] a\n- [ ] ${A}\n- [ ] b`, 0)).toBe("- [ ] a\n- [ ] b");
    expect(removeReference(`- a\n- ${A}\n- b`, 0)).toBe("- a\n- b");
    expect(removeReference(`9. a\n10. ${A}\n11. b`, 0)).toBe("9. a\n11. b");
    expect(moveReference(`- [ ] a\n- [ ] ${A}\n- [ ] b`, 0, 1, "criteria")).toEqual({ text: `- [ ] a\n- [ ] b\n  ${A}`, index: 0 });
    expect(moveReference(`- a\n- ${A}\n- b`, 0, -1, "criteria")).toEqual({ text: `- a\n  ${A}\n- b`, index: 0 });
    expect(moveReference(`9. a\n10. ${A}\n11. b`, 0, 1, "criteria")).toEqual({ text: `9. a\n11. b\n    ${A}`, index: 0 });
  });
  test("with lines under it, the item stays and they become its text", () => {
    expect(removeReference(`- [ ] ${A}\n  more`, 0)).toBe("- [ ]\n  more");
    expect(criteriaLines(removeReference(`- [ ] ${A}\n  more`, 0)).items.map((i) => i.lines.join("\n").trim())).toEqual(["more"]);
  });
});

describe("move in the goal", () => {
  const goal = `First.\n\nSecond.\n\n${A}\n\nThird.`;
  test("up swaps it with the paragraph above; down with the one below", () => {
    expect(moveReference(goal, 0, -1, "goal")).toEqual({ text: `First.\n\n${A}\n\nSecond.\n\nThird.`, index: 0 });
    expect(moveReference(goal, 0, 1, "goal")).toEqual({ text: `First.\n\nSecond.\n\nThird.\n\n${A}`, index: 0 });
    expect(moveReference(`${A}\n\nFirst.`, 0, -1, "goal")).toBeNull();
    expect(moveReference(`First.\n\n${A}`, 0, 1, "goal")).toBeNull();
  });
  test("mid-sentence, up takes it out before its paragraph as one of its own", () => {
    expect(moveReference(`Zero.\n\nSee ${A} here.`, 0, -1, "goal")!.text).toBe(`Zero.\n\n${A}\n\nSee here.`);
  });
  test("a slot is between paragraphs", () => {
    expect(slotCount(goal, "goal")).toBe(5);
    expect(moveReferenceTo(goal, 0, 0, "goal")!.text).toBe(`${A}\n\nFirst.\n\nSecond.\n\nThird.`);
    expect(moveReferenceTo(goal, 0, 2, "goal")).toBeNull();
  });
});

describe("move in the criteria", () => {
  const crit = `- [ ] One\n- [ ] Two\n  ${A}\n- [ ] Three\n  more of three`;
  test("down puts it under the next criterion as a continuation; the count holds", () => {
    const r = moveReference(crit, 0, 1, "criteria")!;
    expect(r.text).toBe(`- [ ] One\n- [ ] Two\n- [ ] Three\n  more of three\n  ${A}`);
    expect(itemsOf(r.text)).toBe(3);
  });
  test("up goes under the criterion above", () => {
    expect(moveReference(crit, 0, -1, "criteria")!.text).toBe(`- [ ] One\n  ${A}\n- [ ] Two\n- [ ] Three\n  more of three`);
    expect(moveReference(`- [ ] One\n  ${A}`, 0, -1, "criteria")).toBeNull();
  });
  test("an inline image moves to its own criterion's end first", () => {
    expect(moveReference(`- [ ] Totals ${A} line up\n- [ ] Two`, 0, 1, "criteria")!.text).toBe(`- [ ] Totals line up\n  ${A}\n- [ ] Two`);
  });
  test("a numbered list's continuation is indented to its content", () => {
    expect(insertReference("10. Ten\n11. Eleven", A, 0, "criteria").text).toBe(`10. Ten\n    ${A}\n11. Eleven`);
  });
});

describe("between fields", () => {
  test("the goal's image cut and put under criterion 2", () => {
    const cut = cutReference(`Intro.\n\n${A}\n\nOutro ${B}.`, 0)!;
    expect(cut.text).toBe(`Intro.\n\nOutro ${B}.`);
    const put = insertReference("- [ ] One\n- [ ] Two\n- [ ] Three", cut.ref, 1, "criteria");
    expect(put.text).toBe(`- [ ] One\n- [ ] Two\n  ${A}\n- [ ] Three`);
    expect(put.text.slice(put.at, put.at + A.length)).toBe(A);
  });
  test("into empty criteria it becomes the first criterion", () => {
    expect(insertReference("", A, 0, "criteria").text).toBe(`- [ ] ${A}`);
  });
});
