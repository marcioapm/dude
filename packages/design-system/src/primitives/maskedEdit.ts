/**
 * Edits made in a masked SecretField, applied to the whole value.
 *
 * A password input drops CR and LF, so masked, the field shows the value
 * without its line breaks ("flattened"), and its offsets are offsets into
 * that. An edit there is described by the selection before it and the
 * browser's inputType, and is applied to the whole value at the mapped
 * offsets: never inferred from the old and new flattened strings alone,
 * which cannot tell repeated characters apart.
 *
 * At a hidden line break:
 * - a caret there is after the break, so text typed there starts the next line;
 * - deleting the character just before or just after a break deletes that
 *   character and keeps the break;
 * - a break is removed only when the characters on both sides of it are
 *   within the deleted range;
 * - deleting every character empties the value, its breaks included.
 */

export interface MaskedEdit {
  /** The browser's InputEvent.inputType; "" when it is not known. */
  readonly inputType: string;
  /** The selection before the edit, in flattened offsets. */
  readonly start: number;
  readonly end: number;
}

const BREAK = /[\r\n]/g;
const isBreak = (c: string) => c === "\r" || c === "\n";

/** The value as a password input shows it: without CR and LF. */
export function flatten(value: string): string {
  return value.replace(BREAK, "");
}

/** Where flattened offset i falls in the whole value: at the i-th character, after any break before it. */
export function wholeOffset(value: string, i: number): number {
  for (let at = 0, seen = 0; at < value.length; at++) {
    if (isBreak(value[at]!)) continue;
    if (seen === i) return at;
    seen++;
  }
  return value.length;
}

/** The flattened offset of the whole value's offset at. */
export function maskedOffset(value: string, at: number): number {
  return flatten(value.slice(0, at)).length;
}

/** value with the flattened characters [from, to) replaced by text, by the rules above. */
export function spliceFlat(value: string, from: number, to: number, text: string): string {
  const count = flatten(value).length;
  if (from === 0 && to >= count && to > from) return text;
  const at = wholeOffset(value, from);
  const until = to > from ? wholeOffset(value, to - 1) + 1 : at;
  return value.slice(0, at) + text + value.slice(until);
}

interface Placed {
  readonly from: number;
  readonly to: number;
  readonly text: string;
}

/** Flattened [from, to) and its replacement text that turn shown into next by edit, or null. */
function place(shown: string, next: string, edit: MaskedEdit): Placed | null {
  const s = Math.max(0, Math.min(edit.start, edit.end, shown.length));
  const e = Math.min(Math.max(edit.start, edit.end), shown.length);
  let from = s;
  let to = e;
  let text = "";
  const deletes = edit.inputType.startsWith("delete") && !edit.inputType.includes("Composition");
  // Undo, redo and formatting say nothing about where the field changed.
  if (!deletes && !edit.inputType.startsWith("insert")) return null;
  if (deletes) {
    if (e === s) {
      // Collapsed: the browser removed what lies on one side of the caret,
      // as many characters as the field lost (one, a surrogate pair, a word, a line).
      const lost = shown.length - next.length;
      if (lost <= 0) return null;
      if (edit.inputType.includes("Backward")) from = s - lost;
      else if (edit.inputType.includes("Forward")) to = s + lost;
      else return null;
      if (from < 0 || to > shown.length) return null;
    }
  } else {
    const inserted = next.length - (shown.length - (e - s));
    if (inserted < 0) return null;
    text = next.slice(s, s + inserted);
  }
  if (shown.slice(0, from) + text + shown.slice(to) !== next) return null;
  return { from, to, text };
}

/**
 * The whole value after the masked field went from showing flatten(value)
 * to showing next, by edit. Null when edit does not account for that
 * change, so the caller never applies an edit it cannot place.
 */
export function applyMaskedEdit(value: string, next: string, edit: MaskedEdit): string | null {
  // Nothing changed (a cancelled composition): the breaks in its range stay.
  if (next === flatten(value)) return value;
  const p = place(flatten(value), next, edit);
  return p && spliceFlat(value, p.from, p.to, p.text);
}

/**
 * The whole value when no inputType was seen for a change. The selection
 * last known before it says where it was made, and the caret after it
 * (after what was inserted, or where the deletion was) which of a
 * collapsed caret's edits it was. Null when there is no known selection,
 * when no edit from that selection explains the change, or when the edits
 * that do disagree on the whole value: the caller then shows the value
 * rather than guess.
 */
export function inferMaskedEdit(
  value: string,
  next: string,
  before: { readonly start: number; readonly end: number } | null,
  caretAfter: number | null,
): string | null {
  if (!before) return null;
  const shown = flatten(value);
  const { start, end } = before;
  const types = start === end ? ["insertText", "deleteContentBackward", "deleteContentForward"] : ["insertText", "deleteContent"];
  const results = new Set<string>();
  for (const inputType of types) {
    const p = place(shown, next, { inputType, start, end });
    if (p && (caretAfter === null || caretAfter === p.from + p.text.length)) results.add(spliceFlat(value, p.from, p.to, p.text));
  }
  return results.size === 1 ? [...results][0]! : null;
}
