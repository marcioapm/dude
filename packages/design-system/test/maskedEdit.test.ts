/**
 * Masked edits applied to the whole value: an edit made where the field
 * shows the value without its line breaks lands where it was made, by the
 * rules in maskedEdit.ts, whatever characters repeat around it.
 */

import { describe, expect, test } from "bun:test";
import { applyMaskedEdit, flatten, inferMaskedEdit, spliceFlat, type MaskedEdit } from "../src/primitives/maskedEdit.ts";

/** What the browser's password field shows after the edit: the flattened value spliced. */
function browser(value: string, edit: MaskedEdit, text: string): string {
  const shown = flatten(value);
  const s = Math.min(edit.start, edit.end);
  const e = Math.max(edit.start, edit.end);
  if (s !== e) return shown.slice(0, s) + text + shown.slice(e);
  if (edit.inputType.endsWith("Backward")) return shown.slice(0, s - 1) + shown.slice(s);
  if (edit.inputType.endsWith("Forward")) return shown.slice(0, s) + shown.slice(s + 1);
  return shown.slice(0, s) + text + shown.slice(s);
}

const cases: { name: string; value: string; edit: MaskedEdit; text?: string; next?: string; want: string }[] = [
  { name: "typing at offset 0 of a\\na", value: "a\na", edit: { inputType: "insertText", start: 0, end: 0 }, text: "a", want: "aa\na" },
  { name: "typing at offset 0 of a\\nb", value: "a\nb", edit: { inputType: "insertText", start: 0, end: 0 }, text: "a", want: "aa\nb" },
  { name: "typing between unique characters", value: "ab\ncd", edit: { inputType: "insertText", start: 1, end: 1 }, text: "x", want: "axb\ncd" },
  { name: "typing where a break is hidden starts the next line", value: "a\na", edit: { inputType: "insertText", start: 1, end: 1 }, text: "a", want: "a\naa" },
  { name: "typing at the end", value: "one\ntwo", edit: { inputType: "insertText", start: 6, end: 6 }, text: "!", want: "one\ntwo!" },
  { name: "Backspace over a selection of repeated characters across a break", value: "a\naa", edit: { inputType: "deleteContentBackward", start: 0, end: 2 }, want: "a" },
  { name: "Backspace over [1,3) of aa\\naa", value: "aa\naa", edit: { inputType: "deleteContentBackward", start: 1, end: 3 }, want: "aa" },
  { name: "Backspace over the first of a\\na", value: "a\na", edit: { inputType: "deleteContentBackward", start: 0, end: 1 }, want: "\na" },
  { name: "a selected delete across a break, unique", value: "one\ntwo", edit: { inputType: "deleteContentBackward", start: 2, end: 4 }, want: "onwo" },
  { name: "a selected delete across a break, control", value: "one\ntwo!", edit: { inputType: "deleteContentBackward", start: 1, end: 4 }, want: "owo!" },
  { name: "a selected delete across a break", value: "ab\ncd", edit: { inputType: "deleteContentBackward", start: 1, end: 3 }, want: "ad" },
  { name: "Backspace just after a break keeps it", value: "a\na", edit: { inputType: "deleteContentBackward", start: 1, end: 1 }, want: "\na" },
  { name: "Delete just before a break keeps it", value: "a\na", edit: { inputType: "deleteContentForward", start: 1, end: 1 }, want: "a\n" },
  { name: "Backspace of a repeated character keeps the break", value: "ab\nb", edit: { inputType: "deleteContentBackward", start: 2, end: 2 }, want: "a\nb" },
  { name: "Backspace in the middle of a line", value: "abc\nd", edit: { inputType: "deleteContentBackward", start: 2, end: 2 }, want: "ac\nd" },
  { name: "a word deleted backward, anchored at the caret", value: "xx yy\nyy", edit: { inputType: "deleteWordBackward", start: 7, end: 7 }, next: "xx yy", want: "xx yy\n" },
  { name: "a word deleted forward across a break takes the break between its characters", value: "aa\naa bb", edit: { inputType: "deleteWordForward", start: 0, end: 0 }, next: " bb", want: " bb" },
  { name: "a word deleted forward up to a break keeps it", value: "aa\nbb", edit: { inputType: "deleteWordForward", start: 0, end: 0 }, next: "bb", want: "\nbb" },
  { name: "a line deleted backward", value: "ab\ncd", edit: { inputType: "deleteSoftLineBackward", start: 4, end: 4 }, next: "", want: "" },
  { name: "selecting all and typing replaces the whole value, breaks too", value: "a\nb\n", edit: { inputType: "insertText", start: 0, end: 2 }, text: "x", want: "x" },
  { name: "a paste without a break replaces the selection", value: "a\nb\nc", edit: { inputType: "insertFromPaste", start: 1, end: 2 }, text: "Q", want: "a\nQ\nc" },
  { name: "a composition at offset 0 of a\\na", value: "a\na", edit: { inputType: "insertCompositionText", start: 0, end: 0 }, text: "ab", want: "aba\na" },
  { name: "a replacement over a selection that spans a break", value: "aa\naa", edit: { inputType: "insertReplacementText", start: 1, end: 3 }, text: "zz", want: "azza" },
  { name: "CRLF breaks count as one hidden break", value: "a\r\na", edit: { inputType: "insertText", start: 0, end: 0 }, text: "a", want: "aa\r\na" },
];

describe("applyMaskedEdit", () => {
  for (const c of cases) {
    test(c.name, () => {
      const next = c.next ?? browser(c.value, c.edit, c.text ?? "");
      expect(applyMaskedEdit(c.value, next, c.edit)).toBe(c.want);
    });
  }

  test("an edit that does not explain the field's change is refused, not guessed", () => {
    expect(applyMaskedEdit("a\na", "ba", { inputType: "insertText", start: 1, end: 1 })).toBeNull();
    expect(applyMaskedEdit("a\na", "aaa", { inputType: "historyUndo", start: 0, end: 0 })).toBeNull();
    expect(applyMaskedEdit("a\na", "aaa", { inputType: "deleteContentBackward", start: 1, end: 1 })).toBeNull();
  });

  test("a change that changed nothing keeps the breaks", () => {
    expect(applyMaskedEdit("a\nb", "ab", { inputType: "insertCompositionText", start: 0, end: 2 })).toBe("a\nb");
  });
});

describe("inferMaskedEdit, when the browser said nothing before the change", () => {
  test("the last known selection and the caret after place an edit among repeated characters", () => {
    expect(inferMaskedEdit("a\na", "aaa", { start: 0, end: 0 }, 1)).toBe("aa\na");
    expect(inferMaskedEdit("a\na", "a", { start: 1, end: 1 }, 0)).toBe("\na");
    expect(inferMaskedEdit("a\na", "a", { start: 1, end: 1 }, 1)).toBe("a\n");
    expect(inferMaskedEdit("a\naa", "a", { start: 0, end: 2 }, 0)).toBe("a");
  });

  test("with no known selection, or one that does not explain the change, it refuses where a break makes the place matter", () => {
    expect(inferMaskedEdit("a\na", "aaa", null, 1)).toBeNull();
    expect(inferMaskedEdit("a\na", "aaa", { start: 2, end: 2 }, 1)).toBeNull();
  });

  test("with no known selection, a value without line breaks takes the change as it is", () => {
    expect(inferMaskedEdit("abc", "abXc", null, null)).toBe("abXc");
    expect(inferMaskedEdit("", "sk-test", null, null)).toBe("sk-test");
  });

  test("with no known selection, a value with a line break refuses: the edit may have reached it", () => {
    expect(inferMaskedEdit("ab\ncd", "abcXd", null, null)).toBeNull();
    // "b" selected whole and replaced by "baa" would empty the line breaks too.
    expect(inferMaskedEdit("\r\nb\n", "baa", null, null)).toBeNull();
  });

  test("a collapsed selection whose backward and forward deletes differ, and no caret after, refuses", () => {
    expect(inferMaskedEdit("a\na", "a", { start: 1, end: 1 }, null)).toBeNull();
  });
});

// A reference for the rules, on the whole value seen as characters and the
// breaks before each one: gaps[i] is the run of CR/LF before chars[i], and
// gaps[n] the run after the last.
function split(value: string): { chars: string[]; gaps: string[] } {
  const chars: string[] = [];
  const gaps: string[] = [""];
  for (const c of value) {
    if (c === "\n" || c === "\r") gaps[gaps.length - 1] += c;
    else {
      chars.push(c);
      gaps.push("");
    }
  }
  return { chars, gaps };
}

/**
 * Characters [from, to) replaced by text. A gap between two deleted
 * characters goes; the gaps at the range's two ends stay, the inserted text
 * between them. Every character deleted empties the value.
 */
function reference(value: string, from: number, to: number, text: string): string {
  const { chars, gaps } = split(value);
  if (from === 0 && to === chars.length && to > from) return text;
  let out = "";
  for (let i = 0; i < from; i++) out += gaps[i]! + chars[i]!;
  out += gaps[from]! + text;
  if (to > from) out += gaps[to]!;
  for (let i = to; i < chars.length; i++) out += chars[i]! + gaps[i + 1]!;
  return out;
}

/** A seeded generator, so a failure names the seed that reproduces it. */
function rng(seed: number) {
  let s = seed >>> 0;
  return () => {
    s = (s * 1664525 + 1013904223) >>> 0;
    return s / 2 ** 32;
  };
}

describe("applyMaskedEdit against the reference, on random values with repeated characters", () => {
  test("every insertion, replacement and deletion lands where it was made", () => {
    for (let seed = 1; seed <= 3000; seed++) {
      const r = rng(seed);
      const pick = <T,>(xs: readonly T[]) => xs[Math.floor(r() * xs.length)]!;
      const len = 1 + Math.floor(r() * 9);
      let value = "";
      for (let i = 0; i < len; i++) value += pick(["a", "a", "b", "\n", "\n", "\r\n"]);
      const n = flatten(value).length;
      const a = Math.floor(r() * (n + 1));
      const b = r() < 0.5 ? a : Math.floor(r() * (n + 1));
      const start = Math.min(a, b);
      const end = Math.max(a, b);
      const op = pick(["insertText", "insertFromPaste", "insertCompositionText", "deleteContentBackward", "deleteContentForward", "deleteWordBackward"]);
      let from = start;
      let to = end;
      let text = "";
      let next: string;
      const shown = flatten(value);
      if (op.startsWith("insert")) {
        text = Array.from({ length: 1 + Math.floor(r() * 3) }, () => pick(["a", "b"])).join("");
        next = shown.slice(0, start) + text + shown.slice(end);
      } else if (start !== end) {
        next = shown.slice(0, start) + shown.slice(end);
      } else if (op === "deleteContentForward") {
        if (start === n) continue;
        to = start + 1;
        next = shown.slice(0, start) + shown.slice(to);
      } else {
        const lost = op === "deleteWordBackward" ? Math.min(start, 1 + Math.floor(r() * 3)) : 1;
        if (start - lost < 0 || lost === 0) continue;
        from = start - lost;
        next = shown.slice(0, from) + shown.slice(start);
      }
      const want = next === shown ? value : reference(value, from, to, text);
      const got = applyMaskedEdit(value, next, { inputType: op, start, end });
      if (got !== want) throw new Error(`seed ${seed}: ${JSON.stringify({ value, op, start, end, text, got, want })}`);
      expect(flatten(got!)).toBe(next);
      // Not told the edit, nor where: the value made, or none, never another.
      const guessed = inferMaskedEdit(value, next, null, null);
      if (guessed !== null && guessed !== want) {
        throw new Error(`seed ${seed}, no selection: ${JSON.stringify({ value, op, start, end, text, guessed, want })}`);
      }
    }
  });

  test("spliceFlat and the reference agree on every range of a few values", () => {
    for (const value of ["a\na", "\naa\n", "a\r\n\nb\na", "ab"]) {
      const n = flatten(value).length;
      for (let from = 0; from <= n; from++)
        for (let to = from; to <= n; to++)
          for (const text of ["", "x"]) expect(spliceFlat(value, from, to, text)).toBe(reference(value, from, to, text));
    }
  });
});
