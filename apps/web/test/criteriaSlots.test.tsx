/**
 * A drop under the n-th criterion Preview draws lands in the n-th criterion
 * `criteriaFromMarkdown` saves: Preview's slots, the image moves and the
 * saved criteria read items by one rule. The inputs are those where an
 * item-by-marker walk disagreed with it: a stray paragraph or heading after
 * an item, a fence left open in an item, and a fence closed outdented.
 */

import { expect, test } from "bun:test";
import { attachmentReferences } from "@dude/domain";
import { renderToStaticMarkup } from "react-dom/server";
import { Markdown } from "@dude/design-system/components";
import { imageLayout } from "@dude/design-system";
import { criteriaFromMarkdown } from "../src/screens/criteria.ts";

const REF = "![x.png](attachment:att_x)";

/** The criteria Preview draws, as the slot finder counts them: the root's top-level list items. */
function drawnItems(text: string): number {
  const root = document.createElement("div");
  root.innerHTML = renderToStaticMarkup(<Markdown source={text} unmeasured />);
  return root.firstElementChild!.querySelectorAll(":scope > ul > li, :scope > ol > li").length;
}

const cases: Array<[string, string, number]> = [
  ["a stray paragraph after an item", "- [ ] a\n\nnote\n- [ ] b", 2],
  ["a heading straight after an item", "- [ ] a\n# head\n- [ ] b", 2],
  ["a fence left open in an item", "- [ ] a\n  ```\n  code\n- [ ] b", 2],
  ["a fence closed outdented", "- [ ] a\n  ```\n  x\n```\n- [ ] b", 1],
];

for (const [name, text, count] of cases) {
  test(`${name}: one slot per drawn criterion, and a drop under criterion N lands in criterion N`, () => {
    const before = criteriaFromMarkdown(text);
    expect(drawnItems(text)).toBe(count);
    expect(before.items).toHaveLength(count);
    expect(imageLayout.slotCount(text, "criteria")).toBe(count);
    for (let n = 0; n < count; n++) {
      const put = imageLayout.insertReference(text, REF, n, "criteria");
      const after = criteriaFromMarkdown(put.text);
      expect(after.items).toHaveLength(count);
      after.items.forEach((item, i) => expect(item.includes(REF)).toBe(i === n));
      // The image is a reference in that criterion, not code in a fence.
      expect(attachmentReferences(after.items[n]!).map((r) => r.id)).toEqual(["att_x"]);
      expect(drawnItems(put.text)).toBe(count);
    }
  });
}
