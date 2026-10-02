/**
 * An image's layout in a task's Markdown, and the edits Preview makes to
 * the source. The layout is the reference's title: `![alt](attachment:id
 * "small right")`. Words: a size (`small`, `medium`, `full`, or a width in
 * px) and an alignment (`center`, `left`, `right`); unknown words are
 * ignored, `full` is always centred, and defaults are not written back.
 *
 * Every edit takes the field's source and returns the new source: the text
 * stays the one source of truth. Images are named by their place among the
 * text's references (`attachmentReferences`), which is the order Markdown
 * draws them in.
 */

import { attachmentReferences } from "@dude/domain";
import { criteriaLines, type CriteriaItem } from "./criteria.ts";
import { parseMarkdown } from "./markdown.ts";

export type ImageSize = "small" | "medium" | "full" | number;
export type ImageAlign = "center" | "left" | "right";
export interface ImageLayout {
  readonly size: ImageSize;
  readonly align: ImageAlign;
}

export const IMAGE_SIZES = { small: 200, medium: 420 } as const;
/** The narrowest a free resize goes, in px. */
export const IMAGE_MIN_WIDTH = 120;

export function parseLayout(title: string | undefined): ImageLayout {
  let size: ImageSize = "medium";
  let align: ImageAlign = "center";
  for (const w of (title ?? "").split(/[ \t]+/)) {
    if (w === "small" || w === "medium" || w === "full") size = w;
    else if (/^\d{1,5}$/.test(w)) size = Math.max(IMAGE_MIN_WIDTH, Number(w));
    else if (w === "center" || w === "left" || w === "right") align = w;
  }
  return { size, align: size === "full" ? "center" : align };
}

/** The title to write, defaults dropped; undefined for medium and centred. */
export function layoutTitle(l: ImageLayout): string | undefined {
  const words: string[] = [];
  if (l.size !== "medium") words.push(String(typeof l.size === "number" ? Math.max(IMAGE_MIN_WIDTH, Math.round(l.size)) : l.size));
  if (l.align !== "center" && l.size !== "full") words.push(l.align);
  return words.length ? words.join(" ") : undefined;
}

/** CSS width for a size; `max-width: 100%` clamps it to the column. */
export function layoutWidth(size: ImageSize): string {
  return size === "full" ? "100%" : `${typeof size === "number" ? size : IMAGE_SIZES[size]}px`;
}

/**
 * A dragged width as a size: Small or Medium within ±10 px of theirs, Full
 * at the column's edge (`max`), else the px width, never under 120.
 */
export function snapWidth(px: number, max: number): ImageSize {
  if (px >= max - 6) return "full";
  for (const k of ["small", "medium"] as const) if (Math.abs(px - IMAGE_SIZES[k]) <= 10) return k;
  return Math.max(IMAGE_MIN_WIDTH, Math.round(Math.min(px, max)));
}

/** `n`'s reference with `layout` as its title; everything before the URL's end kept as written. */
export function withLayout(text: string, n: number, layout: ImageLayout): string {
  const r = attachmentReferences(text)[n];
  if (!r) return text;
  const span = text.slice(r.from, r.to);
  let end = urlStart(span) + "attachment:".length + r.id.length;
  if (span[end] === ">") end++;
  const title = layoutTitle(layout);
  return text.slice(0, r.from) + span.slice(0, end) + (title ? ` "${title}"` : "") + ")" + text.slice(r.to);
}

/** Where `attachment:` starts in one reference: past the alt's `]`, which an escape never is. */
function urlStart(span: string): number {
  let i = 2;
  while (i < span.length && span[i] !== "]") i += span[i] === "\\" ? 2 : 1;
  return span.indexOf("attachment:", i);
}

const lineOf = (text: string, at: number) => text.slice(0, at).split("\n").length - 1;
const blank = (l: string | undefined) => l === undefined || l.trim() === "";
// A list marker, and a task box, with nothing after them.
const BARE_ITEM = /^[ \t]{0,3}(?:[-*+]|\d{1,9}[.)])[ \t]+(?:\[[ xX]\][ \t]+)?$/;

/**
 * The text without `n`'s reference, and the reference as written. A line
 * left empty goes, with a blank line it leaves doubled; inside a line the
 * spaces either side close up to one.
 */
export function cutReference(text: string, n: number): { text: string; ref: string } | null {
  const r = attachmentReferences(text)[n];
  if (!r) return null;
  const ref = text.slice(r.from, r.to);
  const lineStart = text.lastIndexOf("\n", r.from - 1) + 1;
  const nl = text.indexOf("\n", r.to);
  const lineEnd = nl < 0 ? text.length : nl;
  const head = text.slice(lineStart, r.from);
  const tail = text.slice(r.to, lineEnd);
  // The image was a list item's whole text: the item goes too, unless lines
  // under it follow, which then become its text.
  const bareItem = blank(tail) && BARE_ITEM.test(head) && !criteriaLines(text).items.some((i) => i.first === lineOf(text, r.from) && i.last > i.first);
  if (blank(head + tail) || bareItem) {
    const lines = text.split("\n");
    const at = lineOf(text, r.from);
    lines.splice(at, 1);
    while (at < lines.length && blank(lines[at]) && (at === 0 || blank(lines[at - 1]))) lines.splice(at, 1);
    if (at >= lines.length) while (lines.length && blank(lines[lines.length - 1])) lines.pop();
    return { text: lines.join("\n"), ref };
  }
  const joined = tail === "" ? head.replace(/[ \t]+$/, "") : head.endsWith(" ") && tail.startsWith(" ") ? head + tail.slice(1) : head + tail;
  return { text: text.slice(0, lineStart) + joined + text.slice(lineEnd), ref };
}

export function removeReference(text: string, n: number): string {
  return cutReference(text, n)?.text ?? text;
}

// --- Where an image can go ---------------------------------------------------

/** The line each top-level block of the goal starts on. */
function blockStarts(text: string): number[] {
  const lines: number[] = [];
  parseMarkdown(text, { blockLines: lines });
  return lines;
}


/**
 * The criteria's items as `criteriaLines` reads them (the rule the saved
 * criteria follow), one per drawn `li`: `last` is the item's last non-blank
 * line, `column` where a continuation line is indented to.
 */
export function criteriaItems(text: string): CriteriaItem[] {
  return [...criteriaLines(text).items];
}

/**
 * The item's last line an image can follow: its last non-blank line, or,
 * when a fence opened in it runs to its end, the last one before that fence
 * (after it, the image would be code).
 */
function itemEnd(item: CriteriaItem, lines: readonly string[]): number {
  if (item.fenceLine === null) return item.last;
  let at = item.fenceLine - 1;
  while (at > item.first && blank(lines[at])) at--;
  return Math.max(item.first, at);
}

export type FieldKind = "goal" | "criteria";

/** How many slots a field has: before each goal block and after the last; one per criterion. */
export function slotCount(text: string, kind: FieldKind): number {
  return kind === "goal" ? blockStarts(text).length + 1 : Math.max(1, criteriaItems(text).length);
}

/**
 * `ref` put in at `slot`, and its offset. In the goal, a paragraph of its own
 * before block `slot` (or at the end). In the criteria, a continuation line,
 * indented to the item's content, under the last line of criterion `slot`:
 * it stays that criterion's and creates or merges none.
 */
export function insertReference(text: string, ref: string, slot: number, kind: FieldKind): { text: string; at: number } {
  const lines = text === "" ? [] : text.split("\n");
  let at: number;
  if (kind === "goal") {
    const starts = blockStarts(text);
    if (slot >= starts.length) {
      while (lines.length && blank(lines[lines.length - 1])) lines.pop();
      if (lines.length) lines.push("");
      at = lines.length;
      lines.push(ref);
    } else {
      at = starts[Math.max(0, slot)]!;
      const add = [ref, ""];
      let refLine = at;
      if (at > 0 && !blank(lines[at - 1])) {
        add.unshift("");
        refLine++;
      }
      lines.splice(at, 0, ...add);
      at = refLine;
    }
  } else {
    const items = criteriaItems(text);
    const item = items[Math.min(Math.max(0, slot), items.length - 1)];
    if (!item) {
      at = lines.length;
      lines.push(`- [ ] ${ref}`);
      const joined = lines.join("\n");
      return { text: joined, at: joined.length - ref.length };
    }
    at = itemEnd(item, lines) + 1;
    lines.splice(at, 0, " ".repeat(item.column) + ref);
  }
  const joined = lines.join("\n");
  const lineOffset = lines.slice(0, at).reduce((s, l) => s + l.length + 1, 0);
  return { text: joined, at: lineOffset + lines[at]!.indexOf(ref) };
}

/** Which slot `n`'s reference sits in, and whether it is all of it (a goal paragraph of its own; the end of a criterion). */
function placeOf(text: string, n: number, kind: FieldKind): { slot: number; own: boolean } | null {
  const r = attachmentReferences(text)[n];
  if (!r) return null;
  const line = lineOf(text, r.from);
  if (kind === "goal") {
    const starts = blockStarts(text);
    let b = 0;
    while (b + 1 < starts.length && starts[b + 1]! <= line) b++;
    const end = b + 1 < starts.length ? starts[b + 1]! : Infinity;
    const lines = text.split("\n");
    const blockText = lines.slice(starts[b], Math.min(end, lines.length)).join("\n").trim();
    return { slot: b, own: blockText === text.slice(r.from, r.to) };
  }
  const items = criteriaItems(text);
  let i = 0;
  while (i + 1 < items.length && items[i + 1]!.first <= line) i++;
  const lines = text.split("\n");
  const item = items[i];
  const end = lines.slice(0, (item ? itemEnd(item, lines) : 0) + 1).join("\n").length;
  return { slot: i, own: text.slice(r.to, end).trim() === "" };
}

/** The reference's index in `text` at offset `at`. */
const indexAt = (text: string, at: number) => attachmentReferences(text).findIndex((r) => r.from === at);

/**
 * `n`'s image moved within its field to `slot` (in the field as it is now).
 * Returns the new text and the image's new index; null when nothing moves.
 */
export function moveReferenceTo(text: string, n: number, slot: number, kind: FieldKind): { text: string; index: number } | null {
  const place = placeOf(text, n, kind);
  const cut = cutReference(text, n);
  if (!place || !cut) return null;
  let target = slot;
  // A goal paragraph of its own is gone after the cut: the slots after it move down one.
  if (kind === "goal" && place.own && slot > place.slot) target--;
  if (kind === "goal" && place.own && target === place.slot) return null;
  if (kind === "criteria" && place.own && target === place.slot) return null;
  // A criterion that was only the image is gone after the cut: the same.
  if (kind === "criteria" && target > place.slot && criteriaItems(cut.text).length < criteriaItems(text).length) target--;
  const put = insertReference(cut.text, cut.ref, target, kind);
  return { text: put.text, index: indexAt(put.text, put.at) };
}

/** `n`'s image one block up (-1) or down (+1): past the goal paragraph or criterion beside it. */
export function moveReference(text: string, n: number, dir: -1 | 1, kind: FieldKind): { text: string; index: number } | null {
  const place = placeOf(text, n, kind);
  if (!place) return null;
  if (kind === "goal") {
    const blocks = blockStarts(text).length;
    // Own paragraph: before the one above, or after the one below. In a paragraph: before it, or after it.
    const slot = place.own ? (dir < 0 ? place.slot - 1 : place.slot + 2) : dir < 0 ? place.slot : place.slot + 1;
    if (slot < 0 || slot > blocks) return null;
    return moveReferenceTo(text, n, slot, kind);
  }
  const items = criteriaItems(text).length;
  const slot = place.own ? place.slot + dir : dir < 0 ? place.slot - 1 : place.slot;
  if (slot < 0 || slot >= items) return null;
  return moveReferenceTo(text, n, slot, kind);
}

/** Whether `n`'s image can move one block in `dir`. */
export function canMove(text: string, n: number, dir: -1 | 1, kind: FieldKind): boolean {
  return moveReference(text, n, dir, kind) !== null;
}
