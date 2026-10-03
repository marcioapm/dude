/**
 * A task's acceptance criteria as one Markdown list. The API keeps them as
 * a list of strings; the task dialog edits them as the list a person would
 * write. Each top-level list item is one criterion.
 */

import { criteriaLines } from "@dude/design-system";

export interface ParsedCriteria {
  /** One string per top-level list item, task marker removed, trimmed; empty items dropped. */
  readonly items: string[];
  /** Some text sits outside every list item, and is not saved. */
  readonly stray: boolean;
}

/**
 * The criteria in a Markdown list, by `criteriaLines`' rule: a top-level
 * item (`-`, `*`, `+`, `1.`, `1)`), less an optional `[ ]` / `[x]`, starts a
 * criterion, and the lines under it — dedented by the item's content
 * column — belong to it. Text anywhere else is `stray`.
 */
export function criteriaFromMarkdown(source: string): ParsedCriteria {
  const { items, stray } = criteriaLines(source);
  return { items: items.map((i) => i.lines.join("\n").trim()).filter(Boolean), stray: stray.length > 0 };
}

/**
 * The criteria as the list the editor opens with: `- [ ] ` and each
 * criterion's first line, its other lines indented two spaces so they stay
 * with it. `criteriaFromMarkdown` reads back the same trimmed criteria.
 */
export function criteriaToMarkdown(items: ReadonlyArray<string>): string {
  return items
    .map((c) => c.trim())
    .filter(Boolean)
    .map((c) => {
      const [first, ...rest] = c.split("\n");
      return ["- [ ] " + first, ...rest.map((l) => (l === "" ? "" : "  " + l))].join("\n");
    })
    .join("\n");
}
