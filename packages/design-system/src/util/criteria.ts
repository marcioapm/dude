/**
 * The rule for where a task's acceptance criteria start and end in their
 * Markdown list, in one place: the web app's `criteriaFromMarkdown` reads
 * the criteria with it, and Preview's image moves pick their slots with it,
 * so a drop under the n-th drawn item lands in the n-th criterion.
 */

import { isBlockStart } from "./markdown.ts";

// A list marker at the start of a line: `-`, `*`, `+`, `1.` or `1)`, then
// space or the end of the line. Group 1 is the indent, 2 the marker.
const MARKER = /^([ \t]*)([-*+]|\d{1,9}[.)])(?:[ \t]+|$)/;
const TASK = /^\[[ xX]\](?:[ \t]+|$)/;
const FENCE = /^ {0,3}(`{3,}|~{3,})/;

export interface CriteriaItem {
  /** The marker's line. */
  readonly first: number;
  /** The item's last non-blank line. */
  readonly last: number;
  /** Where the item's content starts: continuation lines are indented to it (`- ` is 2, `10. ` is 4). */
  readonly column: number;
  /** The item's lines as content: marker and `[ ]` off the first, the rest dedented by `column`. */
  readonly lines: readonly string[];
  /** A fence opened in the item and still open at its end, which the item's end closes. */
  readonly openFence: string | null;
  /** The line that open fence starts on. */
  readonly fenceLine: number | null;
}

export interface CriteriaLines {
  readonly items: readonly CriteriaItem[];
  /** Lines outside every item, which are not saved. */
  readonly stray: readonly number[];
}

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
 * A top-level item (`-`, `*`, `+`, `1.`, `1)`, at most 3 columns in) starts a
 * criterion. Lines indented under it — nested lists, more paragraphs, code —
 * belong to it, as do blank lines between them and anything inside a fence
 * opened in it, up to the item's end. So does an unindented line straight
 * after the item's text that opens no block (a lazy continuation, which
 * Preview shows inside the item). Any other line ends the item and is stray,
 * and a fence it opens runs on as stray text.
 */
export function criteriaLines(source: string): CriteriaLines {
  const items: CriteriaItem[] = [];
  const stray: number[] = [];
  let cur: { first: number; last: number; column: number; lines: string[] } | null = null;
  let fence: string | null = null;
  let fenceLine: number | null = null;
  let strayFence: string | null = null;
  let blanks: string[] = [];

  const finish = () => {
    if (cur) items.push({ ...cur, openFence: fence, fenceLine: fence === null ? null : fenceLine });
    cur = null;
    blanks = [];
    fence = null;
  };
  const step = (text: string, open: string | null, i: number) => {
    const next = fenceStep(text, open);
    if (open === null && next !== null) fenceLine = i;
    fence = next;
  };

  source.split("\n").forEach((line, i) => {
    if (strayFence !== null) {
      strayFence = fenceStep(line, strayFence);
      stray.push(i);
      return;
    }
    const empty = line.trim() === "";
    // A fence inside an item runs to its closer or to the item's end: a
    // non-blank line not indented to the item's content.
    if (cur && fence !== null && (empty || indentOf(line) >= cur.column)) {
      const text = dedent(line, cur.column);
      cur.lines.push(text);
      if (!empty) cur.last = i;
      step(text, fence, i);
      return;
    }
    if (empty) {
      if (cur) blanks.push(dedent(line, cur.column));
      return;
    }
    const indent = indentOf(line);
    const marker = MARKER.exec(line);
    // Under the item: any indented text, or a marker indented to the item's
    // content (a nested list). A marker short of that is the next item.
    if (cur && (marker ? indent >= Math.min(cur.column, 4) : indent >= 1)) {
      const text = dedent(line, cur.column);
      cur.lines.push(...blanks, text);
      cur.last = i;
      blanks = [];
      step(text, null, i);
      return;
    }
    // Lazy continuation, as `parseMarkdown` reads it: a line that opens no
    // block, straight after the item's text, is more of that paragraph — or
    // of the item's open fence, which Preview also keeps it in.
    const last = cur ? cur.lines[cur.lines.length - 1] : undefined;
    if (cur && blanks.length === 0 && last !== undefined && last.trim() !== "" && !isBlockStart(line)) {
      cur.lines.push(line.trim());
      cur.last = i;
      return;
    }
    if (marker && indent <= 3) {
      finish();
      const rest = line.slice(marker[0].length);
      const task = TASK.exec(rest);
      const first = task ? rest.slice(task[0].length) : rest;
      cur = { first: i, last: i, column: indent + marker[2]!.length + 1, lines: [first] };
      step(first, null, i);
      return;
    }
    finish();
    stray.push(i);
    strayFence = fenceStep(line, null);
  });
  finish();
  return { items, stray };
}
