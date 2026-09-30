/**
 * The edits `MarkdownEditor` makes to its source, as pure functions over
 * text and a selection. Each returns the range to replace, what to put
 * there, and the selection afterwards; the editor applies it through the
 * browser so it lands on the native undo stack.
 */

export interface TextEdit {
  /** Replace `[from, to)` of the source with `insert`. */
  readonly from: number;
  readonly to: number;
  readonly insert: string;
  /** Selection after the edit, in the new source. */
  readonly selectionStart: number;
  readonly selectionEnd: number;
}

export type MarkdownFormat = "heading" | "bold" | "italic" | "code" | "link" | "quote" | "bullet" | "checklist";

/** Where each format's shortcut is, when it has one (Ctrl on Linux/Windows, ⌘ on macOS). */
export const FORMAT_KEYS: Partial<Record<MarkdownFormat, string>> = { bold: "b", italic: "i", link: "k", code: "e" };

const WRAPS = {
  bold: { mark: "**", placeholder: "bold text" },
  italic: { mark: "_", placeholder: "italic text" },
  code: { mark: "`", placeholder: "code" },
} as const;

const PREFIXES = { heading: "## ", quote: "> ", bullet: "- ", checklist: "- [ ] " } as const;

/** The edit a format makes to `source` with `[start, end)` selected. */
export function formatEdit(source: string, start: number, end: number, format: MarkdownFormat): TextEdit {
  const selected = source.slice(start, end);
  switch (format) {
    case "bold":
    case "italic":
      return wrap(source, start, end, WRAPS[format].mark, WRAPS[format].placeholder);
    case "code":
      if (selected.includes("\n")) {
        const insert = "```\n" + selected + "\n```";
        return { from: start, to: end, insert, selectionStart: start + 4, selectionEnd: start + 4 + selected.length };
      }
      return wrap(source, start, end, WRAPS.code.mark, WRAPS.code.placeholder);
    case "link": {
      // With nothing selected the words are the placeholder, selected;
      // with words selected the address is what is left to type.
      const text = selected || "text";
      const insert = `[${text}](https://)`;
      return selected
        ? { from: start, to: end, insert, selectionStart: start + text.length + 3, selectionEnd: start + insert.length - 1 }
        : { from: start, to: end, insert, selectionStart: start + 1, selectionEnd: start + 1 + text.length };
    }
    default:
      return prefixLines(source, start, end, PREFIXES[format]);
  }
}

/** `mark` either side of the selection, or of a selected placeholder; again removes it. */
function wrap(source: string, start: number, end: number, mark: string, placeholder: string): TextEdit {
  const n = mark.length;
  if (start >= n && source.slice(start - n, start) === mark && source.slice(end, end + n) === mark) {
    const inner = source.slice(start, end);
    return { from: start - n, to: end + n, insert: inner, selectionStart: start - n, selectionEnd: end - n };
  }
  const inner = source.slice(start, end) || placeholder;
  return { from: start, to: end, insert: mark + inner + mark, selectionStart: start + n, selectionEnd: start + n + inner.length };
}

/** `prefix` at the start of every line the selection touches; if all have it, it comes off. */
function prefixLines(source: string, start: number, end: number, prefix: string): TextEdit {
  const from = source.lastIndexOf("\n", start - 1) + 1;
  const lineEnd = source.indexOf("\n", Math.max(end - (end > start ? 1 : 0), from));
  const to = lineEnd === -1 ? source.length : lineEnd;
  const lines = source.slice(from, to).split("\n");
  const off = lines.every((l) => l.startsWith(prefix));
  const insert = lines.map((l) => (off ? l.slice(prefix.length) : prefix + l)).join("\n");
  // A lone caret stays where it was in the words; a selection covers the lines.
  if (start === end && lines.length === 1) {
    const at = off ? Math.max(from, start - prefix.length) : start + prefix.length;
    return { from, to, insert, selectionStart: at, selectionEnd: at };
  }
  return { from, to, insert, selectionStart: from, selectionEnd: from + insert.length };
}

const LIST_ITEM = /^(\s*)([-*+]|(\d{1,9})([.)]))( \[[ xX]\])?[ \t]+(.*)$/;

/**
 * Enter at `caret` on a list line: the next item's marker (`- `, `2.` after
 * `1.`, `- [ ] ` after a task), or, on an item with nothing written, the end
 * of the list. Null when the line is not a list item, so Enter is Enter.
 */
export function continueList(source: string, caret: number): TextEdit | null {
  const lineStart = source.lastIndexOf("\n", caret - 1) + 1;
  const m = LIST_ITEM.exec(source.slice(lineStart, caret));
  if (!m) return null;
  const [, indent = "", bullet = "", num, delim = "", task, content = ""] = m;
  const nextEol = source.indexOf("\n", caret);
  const rest = source.slice(caret, nextEol === -1 ? source.length : nextEol);
  if (!content.trim() && !rest.trim()) {
    return { from: lineStart, to: caret, insert: "", selectionStart: lineStart, selectionEnd: lineStart };
  }
  const marker = num !== undefined ? `${Number(num) + 1}${delim}` : bullet;
  const insert = `\n${indent}${marker}${task ? " [ ]" : ""} `;
  return { from: caret, to: caret, insert, selectionStart: caret + insert.length, selectionEnd: caret + insert.length };
}

/** What a key press asks the editor for, or null for a key it leaves alone. */
export type EditorKey = { readonly kind: "format"; readonly format: MarkdownFormat } | { readonly kind: "toggle" } | { readonly kind: "newline" };

export function editorKey(e: { key: string; ctrlKey: boolean; metaKey: boolean; shiftKey: boolean; altKey: boolean }): EditorKey | null {
  const mod = e.ctrlKey || e.metaKey;
  const key = e.key.toLowerCase();
  if (mod && e.shiftKey && !e.altKey && key === "p") return { kind: "toggle" };
  if (mod && !e.shiftKey && !e.altKey) {
    const format = (Object.keys(FORMAT_KEYS) as MarkdownFormat[]).find((f) => FORMAT_KEYS[f] === key);
    if (format) return { kind: "format", format };
  }
  if (e.key === "Enter" && !mod && !e.shiftKey && !e.altKey) return { kind: "newline" };
  return null;
}

/** Characters as the count shows them against a limit: attention past 90%, danger over. */
export function countState(length: number, max: number | undefined): "ok" | "near" | "over" {
  if (max === undefined) return "ok";
  if (length > max) return "over";
  return length > max * 0.9 ? "near" : "ok";
}
