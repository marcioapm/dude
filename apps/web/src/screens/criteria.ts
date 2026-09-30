/**
 * A task's acceptance criteria as one Markdown list. The API keeps them as
 * a list of strings; the task dialog edits them as the list a person would
 * write. Each top-level list item is one criterion.
 */

import { isMarkdownBlockStart } from "@dude/design-system";

/** The server's limit on one criterion (`z.string().max(2000)` in the task routes). */
export const CRITERION_MAX = 2000;

export interface ParsedCriteria {
  /** One string per top-level list item, task marker removed, trimmed; empty items dropped. */
  readonly items: string[];
  /** Some text sits outside every list item, and is not saved. */
  readonly stray: boolean;
}

// A list marker at the start of a line: `-`, `*`, `+`, `1.` or `1)`, then
// space or the end of the line. Group 1 is the indent, 2 the marker.
const MARKER = /^([ \t]*)([-*+]|\d{1,9}[.)])(?:[ \t]+|$)/;
const TASK = /^\[[ xX]\](?:[ \t]+|$)/;
const FENCE = /^ {0,3}(`{3,}|~{3,})/;

/** Columns of leading whitespace, a tab reaching the next multiple of four. */
function indentOf(line: string): number {
  let col = 0;
  for (const ch of line) {
    if (ch === " ") col += 1;
    else if (ch === "\t") col += 4 - (col % 4);
    else break;
  }
  return col;
}

/** Up to `by` columns of leading whitespace off the line. */
function dedent(line: string, by: number): string {
  let col = 0;
  let i = 0;
  while (i < line.length && col < by) {
    if (line[i] === " ") col += 1;
    else if (line[i] === "\t") col += 4 - (col % 4);
    else break;
    i += 1;
  }
  return line.slice(i);
}

/** Opening or closing a fence: the fence it opens, or null when this line closes `open` or is no fence. */
function fenceStep(text: string, open: string | null): string | null {
  const m = FENCE.exec(text);
  if (!m) return open;
  const mark = m[1]!;
  if (open === null) return mark;
  return mark[0] === open[0] && mark.length >= open.length && text.trim() === mark ? null : open;
}

/**
 * The criteria in a Markdown list. A top-level item (`-`, `*`, `+`, `1.`,
 * `1)`), less an optional `[ ]` / `[x]`, starts a criterion. Lines indented
 * under it — nested lists, more paragraphs, code — belong to it, dedented by
 * the item's content column, as do blank lines between them and anything
 * inside a fence opened in it. So does an unindented line straight after
 * the item's text that opens no block (a lazy continuation, which Preview
 * shows inside the item). Text anywhere else is `stray`.
 */
export function criteriaFromMarkdown(source: string): ParsedCriteria {
  const items: string[] = [];
  let stray = false;
  let current: string[] | null = null;
  // Where the item's content starts: continuation lines are indented to it.
  let column = 0;
  let fence: string | null = null;
  let strayFence: string | null = null;
  let blanks: string[] = [];

  const finish = () => {
    if (current) items.push(current.join("\n"));
    current = null;
    blanks = [];
    fence = null;
  };

  for (const line of source.split("\n")) {
    if (strayFence !== null) {
      strayFence = fenceStep(line, strayFence);
      continue;
    }
    // A fence inside an item runs to its closer or to the item's end: a
    // non-blank line not indented to the item's content.
    if (current && fence !== null && (line.trim() === "" || indentOf(line) >= column)) {
      const text = dedent(line, column);
      current.push(text);
      fence = fenceStep(text, fence);
      continue;
    }
    if (line.trim() === "") {
      if (current) blanks.push(dedent(line, column));
      continue;
    }
    const indent = indentOf(line);
    const marker = MARKER.exec(line);
    // Under the item: any indented text, or a marker indented to the item's
    // content (a nested list). A marker short of that is the next item.
    if (current && (marker ? indent >= Math.min(column, 4) : indent >= 1)) {
      const text = dedent(line, column);
      current.push(...blanks, text);
      blanks = [];
      fence = fenceStep(text, null);
      continue;
    }
    // Lazy continuation, as `parseMarkdown` reads it: a line that opens no
    // block, straight after the item's text, is more of that paragraph — or
    // of the item's open fence, which Preview also keeps it in.
    const last = current ? current[current.length - 1] : undefined;
    if (current && blanks.length === 0 && last !== undefined && last.trim() !== "" && !isMarkdownBlockStart(line)) {
      current.push(line.trim());
      continue;
    }
    if (marker && indent <= 3) {
      finish();
      const rest = line.slice(marker[0].length);
      const task = TASK.exec(rest);
      const first = task ? rest.slice(task[0].length) : rest;
      // Content starts one space after the marker, as CommonMark reads a
      // continuation: `- ` is 2, `10. ` is 4.
      column = indent + marker[2]!.length + 1;
      current = [first];
      fence = fenceStep(first, null);
      continue;
    }
    finish();
    stray = true;
    strayFence = fenceStep(line, null);
  }
  finish();
  return { items: items.map((s) => s.trim()).filter(Boolean), stray };
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

/** The first criterion over the server's limit, as the field's error; null when all fit. */
export function criterionTooLong(items: ReadonlyArray<string>, max = CRITERION_MAX): string | null {
  const at = items.findIndex((c) => c.length > max);
  if (at === -1) return null;
  return `Criterion ${at + 1} is ${items[at]!.length.toLocaleString("en-US")} characters; each can be at most ${max.toLocaleString("en-US")}.`;
}
