/**
 * ANSI escape sequences in tool output, parsed into styled segments.
 *
 * Agents run in a container that forces colour (FORCE_COLOR, CLICOLOR_FORCE,
 * `TERM=xterm-256color`, `git color.ui=always`), so what a tool printed
 * arrives with the SGR codes it printed. This turns that text into a list
 * of `{ text, style }` runs for `AnsiString` to render as React spans.
 * There is no HTML string anywhere in the path; nothing here touches the
 * DOM.
 *
 * What is kept: SGR (`ESC [ … m`) — reset, bold, dim, italic, underline,
 * inverse, the 16 named colours, the 256-colour palette (`38;5;n`) and
 * truecolor (`38;2;r;g;b`), in both `;` and `:` parameter forms.
 *
 * What is stripped: every other escape — cursor movement, erase line,
 * mode switches (`ESC [ ? 25 l`), OSC titles and hyperlinks (`ESC ] … BEL`
 * or `… ESC \`), DCS/APC strings, charset selection, two-byte escapes.
 * A string sequence (OSC and friends) is bounded: it may not cross a
 * newline or run past `STRING_MAX` chars, so output that never closes one
 * — binary, or output built to hide itself — loses only the two-byte
 * introducer, never the lines after it.
 *
 * Carriage returns are applied the way a terminal shows them: on each
 * line only the text after the last `\r` survives, so a progress bar's
 * frames collapse to the last one. `\r\n` is a line ending, and a `\r`
 * at the very end of the input (a bar cut mid-frame) is dropped rather
 * than applied. Styles persist across a `\r` as they do on a terminal.
 *
 * Truncation: the backend keeps a 2 KB head and a 2 KB tail of long
 * output, so a sequence can be cut at the end of the head or the start of
 * the tail. A sequence unfinished at the end of the input is dropped (it
 * can never mean anything). A fragment at the *start* is only
 * recognisable by its shape, and `10ms` or `5m ago` are real text, so the
 * caller says when the start was cut (`cutStart`) and only a fragment that
 * plausibly is the end of an SGR — `[…m`, a `;`/`:` parameter list, or a
 * short code whose `m` runs straight into non-word content — or the
 * terminator of a cut OSC, is removed. Head and tail are parsed on their
 * own: styles never carry across the elision.
 */

import { ANSI_COLOR_NAMES, type AnsiColorName } from "../tokens/palette.ts";

export type AnsiColor =
  | { readonly kind: "named"; readonly name: AnsiColorName }
  | { readonly kind: "rgb"; readonly r: number; readonly g: number; readonly b: number };

export interface AnsiStyle {
  readonly bold: boolean;
  readonly dim: boolean;
  readonly italic: boolean;
  readonly underline: boolean;
  readonly inverse: boolean;
  readonly fg: AnsiColor | null;
  readonly bg: AnsiColor | null;
}

export interface AnsiSegment {
  readonly text: string;
  readonly style: AnsiStyle;
}

export interface ParseAnsiOptions {
  /**
   * The input begins mid-stream (the tail of a capped output): a leading
   * run that looks like the end of a cut sequence is dropped.
   */
  readonly cutStart?: boolean | undefined;
}

const PLAIN: AnsiStyle = Object.freeze({
  bold: false,
  dim: false,
  italic: false,
  underline: false,
  inverse: false,
  fg: null,
  bg: null,
});

const ESC = 0x1b;
const BEL = 0x07;
const LF = 0x0a;
const CR = 0x0d;

/** The most a string sequence (OSC, DCS, …) may run before it is treated as unterminated. */
const STRING_MAX = 512;

/** True when the style would render exactly like unstyled text. */
export function isPlainStyle(s: AnsiStyle): boolean {
  return s === PLAIN || (!s.bold && !s.dim && !s.italic && !s.underline && !s.inverse && s.fg === null && s.bg === null);
}

function sameColor(a: AnsiColor | null, b: AnsiColor | null): boolean {
  if (a === null || b === null) return a === b;
  if (a.kind === "named") return b.kind === "named" && a.name === b.name;
  return b.kind === "rgb" && a.r === b.r && a.g === b.g && a.b === b.b;
}

function sameStyle(a: AnsiStyle, b: AnsiStyle): boolean {
  return (
    a === b ||
    (a.bold === b.bold &&
      a.dim === b.dim &&
      a.italic === b.italic &&
      a.underline === b.underline &&
      a.inverse === b.inverse &&
      sameColor(a.fg, b.fg) &&
      sameColor(a.bg, b.bg))
  );
}

// ---------------------------------------------------------------------------
// Colours
// ---------------------------------------------------------------------------

const CUBE_LEVELS = [0, 95, 135, 175, 215, 255] as const;

/** The xterm 256-colour palette: 0–15 named, 16–231 a 6×6×6 cube, 232–255 greys. */
function ansi256(n: number): AnsiColor | null {
  if (!Number.isInteger(n) || n < 0 || n > 255) return null;
  if (n < 16) return { kind: "named", name: ANSI_COLOR_NAMES[n] as AnsiColorName };
  if (n < 232) {
    const i = n - 16;
    return {
      kind: "rgb",
      r: CUBE_LEVELS[Math.floor(i / 36)] as number,
      g: CUBE_LEVELS[Math.floor(i / 6) % 6] as number,
      b: CUBE_LEVELS[i % 6] as number,
    };
  }
  const v = 8 + (n - 232) * 10;
  return { kind: "rgb", r: v, g: v, b: v };
}

function rgb(r: number | undefined, g: number | undefined, b: number | undefined): AnsiColor | null {
  if (r === undefined || g === undefined || b === undefined) return null;
  const ok = (v: number) => Number.isInteger(v) && v >= 0 && v <= 255;
  return ok(r) && ok(g) && ok(b) ? { kind: "rgb", r, g, b } : null;
}

// ---------------------------------------------------------------------------
// SGR
// ---------------------------------------------------------------------------

/**
 * Parameters as the sequence carried them: `;` separates parameters, `:`
 * separates sub-parameters, an empty parameter is 0.
 */
function sgrParams(raw: string): number[][] {
  if (raw.length === 0) return [[0]];
  return raw.split(";").map((p) => p.split(":").map((s) => (s.length === 0 ? 0 : Number.parseInt(s, 10))));
}

/**
 * An extended colour (`38`/`48`) starting at `items[i]`. Returns the colour
 * (null when malformed) and how many top-level parameters were consumed.
 */
function extendedColor(items: ReadonlyArray<ReadonlyArray<number>>, i: number): { readonly color: AnsiColor | null; readonly used: number } {
  const it = items[i] as ReadonlyArray<number>;
  if (it.length > 1) {
    // Colon form: 38:5:n, 38:2:r:g:b or 38:2:<colourspace>:r:g:b.
    const mode = it[1];
    if (mode === 5) return { color: ansi256(it[2] ?? -1), used: 1 };
    if (mode === 2) {
      const n = it.length;
      return { color: n >= 5 ? rgb(it[n - 3], it[n - 2], it[n - 1]) : null, used: 1 };
    }
    return { color: null, used: 1 };
  }
  const mode = items[i + 1]?.[0];
  if (mode === 5) return { color: ansi256(items[i + 2]?.[0] ?? -1), used: Math.min(3, items.length - i) };
  if (mode === 2) return { color: rgb(items[i + 2]?.[0], items[i + 3]?.[0], items[i + 4]?.[0]), used: Math.min(5, items.length - i) };
  return { color: null, used: items.length - i };
}

function applySgr(style: AnsiStyle, raw: string): AnsiStyle {
  const items = sgrParams(raw);
  let s: AnsiStyle = style;
  const set = (patch: Partial<AnsiStyle>) => {
    s = { ...s, ...patch };
  };
  for (let i = 0; i < items.length; ) {
    const it = items[i] as ReadonlyArray<number>;
    const code = it[0] ?? 0;
    let used = 1;
    if (code === 0) s = PLAIN;
    else if (code === 1) set({ bold: true });
    else if (code === 2) set({ dim: true });
    else if (code === 3) set({ italic: true });
    else if (code === 4) set({ underline: it[1] !== 0 }); // 4:0 is "underline off"
    else if (code === 7) set({ inverse: true });
    else if (code === 21) set({ underline: true }); // double underline
    else if (code === 22) set({ bold: false, dim: false });
    else if (code === 23) set({ italic: false });
    else if (code === 24) set({ underline: false });
    else if (code === 27) set({ inverse: false });
    else if (code >= 30 && code <= 37) set({ fg: ansi256(code - 30) });
    else if (code === 38 || code === 48) {
      const ext = extendedColor(items, i);
      used = ext.used;
      if (ext.color !== null) set(code === 38 ? { fg: ext.color } : { bg: ext.color });
    } else if (code === 39) set({ fg: null });
    else if (code >= 40 && code <= 47) set({ bg: ansi256(code - 40) });
    else if (code === 49) set({ bg: null });
    else if (code >= 90 && code <= 97) set({ fg: ansi256(code - 90 + 8) });
    else if (code >= 100 && code <= 107) set({ bg: ansi256(code - 100 + 8) });
    // Anything else (blink, conceal, fonts, ideogram attributes) is ignored.
    i += Math.max(1, used);
  }
  return s;
}

// ---------------------------------------------------------------------------
// Escape scanning
// ---------------------------------------------------------------------------

interface Escape {
  /** Index just past the sequence. */
  readonly end: number;
  /** SGR parameter string when the sequence is a well-formed SGR. */
  readonly sgr: string | null;
}

const isParam = (c: number) => c >= 0x30 && c <= 0x3f;
const isIntermediate = (c: number) => c >= 0x20 && c <= 0x2f;
const isFinal = (c: number) => c >= 0x40 && c <= 0x7e;

/**
 * The end of a string sequence whose body starts at `from`: just past its
 * BEL or `ESC \`. A body may not cross a newline or exceed `STRING_MAX`;
 * when it would, or when the input ends first, the result is null.
 */
function stringEnd(text: string, from: number): number | null {
  const limit = Math.min(text.length, from + STRING_MAX);
  for (let k = from; k < limit; k++) {
    const c = text.charCodeAt(k);
    if (c === BEL) return k + 1;
    if (c === ESC) return text.charCodeAt(k + 1) === 0x5c /* \ */ ? k + 2 : k;
    if (c === LF) return null;
  }
  return null;
}

/**
 * Scan one escape sequence starting at the ESC at `i`. Returns null when
 * the sequence runs off the end of the input (truncated).
 */
function scanEscape(text: string, i: number): Escape | null {
  const n = text.length;
  const j = i + 1;
  if (j >= n) return null;
  const c = text.charCodeAt(j);
  if (c === 0x5b /* [ */) {
    let k = j + 1;
    let privateParams = false;
    while (k < n && isParam(text.charCodeAt(k))) {
      const p = text.charCodeAt(k);
      if (p >= 0x3c) privateParams = true; // < = > ?
      k++;
    }
    const paramsEnd = k;
    while (k < n && isIntermediate(text.charCodeAt(k))) k++;
    if (k >= n) return null;
    const final = text.charCodeAt(k);
    // A byte that cannot end a CSI (a newline, say): the sequence was
    // malformed, drop what was consumed and let the text carry on.
    if (!isFinal(final)) return { end: k, sgr: null };
    const isSgr = final === 0x6d /* m */ && k === paramsEnd && !privateParams;
    return { end: k + 1, sgr: isSgr ? text.slice(j + 1, paramsEnd) : null };
  }
  if (c === 0x5d /* ] */ || c === 0x50 /* P */ || c === 0x58 /* X */ || c === 0x5e /* ^ */ || c === 0x5f /* _ */) {
    const end = stringEnd(text, j + 1);
    if (end !== null) return { end, sgr: null };
    // Unterminated. If the input simply ran out on this line within the
    // bound, it was cut by the cap: drop it whole. Otherwise (a newline
    // or the bound came first) the tool never closed it: drop only the
    // two-byte introducer and keep the lines after it.
    const rest = n - (j + 1);
    const cut = rest < STRING_MAX && text.indexOf("\n", j + 1) === -1;
    return cut ? null : { end: j + 1, sgr: null };
  }
  if (isIntermediate(c)) {
    let k = j;
    while (k < n && isIntermediate(text.charCodeAt(k))) k++;
    if (k >= n) return null;
    return { end: k + 1, sgr: null };
  }
  if (c >= 0x30 && c <= 0x7e) return { end: j + 1, sgr: null };
  // ESC before a control character or newline: drop the ESC alone.
  return { end: j, sgr: null };
}

/**
 * The tail of a cut SGR at the start of the input. Accepted shapes:
 *   `[…m`            the bracket is there, so it can only be a CSI
 *   `01;34m`         a parameter list: real text does not look like this
 *   `2m✓`, `1m\x1b`  a short code whose `m` runs straight into something
 *                    that is not a word character or a space
 * Not accepted: `10ms`, `5m ago`, `m` alone — those are text.
 */
const CUT_SGR = /^(?:\[[0-9;:]*m|[0-9]*[;:][0-9;:]*m|[0-9]{1,3}m(?=[^\p{L}\p{N}_\s]))/u;

/**
 * How much of the start of a cut input is the end of a sequence whose
 * beginning was lost: a cut SGR (above), or a run up to a BEL or `ESC \`
 * on the first line — the terminator of an OSC whose opener was cut.
 */
function cutPrefix(text: string): number {
  const m = CUT_SGR.exec(text);
  if (m !== null) return m[0].length;
  const limit = Math.min(text.length, STRING_MAX);
  for (let k = 0; k < limit; k++) {
    const c = text.charCodeAt(k);
    if (c === LF) return 0;
    if (c === BEL) return k + 1;
    if (c === ESC) return text.charCodeAt(k + 1) === 0x5c ? k + 2 : 0;
  }
  return 0;
}

// ---------------------------------------------------------------------------
// Assembly
// ---------------------------------------------------------------------------

/** Runs of one style, merged when adjacent, never empty. */
class Runs {
  readonly out: AnsiSegment[] = [];
  push(text: string, style: AnsiStyle): void {
    if (text.length === 0) return;
    const last = this.out[this.out.length - 1];
    if (last !== undefined && sameStyle(last.style, style)) this.out[this.out.length - 1] = { text: last.text + text, style: last.style };
    else this.out.push({ text, style });
  }
}

/**
 * Apply carriage returns: on each line, keep only what follows the last
 * `\r`. Styles are those the text was parsed with, so a bar drawn in
 * colour keeps its colour. `\r\n` and a trailing `\r` are just removed.
 */
function applyCarriageReturns(segments: ReadonlyArray<AnsiSegment>): AnsiSegment[] {
  const runs = new Runs();
  let line: AnsiSegment[] = [];
  const flush = () => {
    for (const s of line) runs.push(s.text, s.style);
    line = [];
  };
  for (let si = 0; si < segments.length; si++) {
    const seg = segments[si] as AnsiSegment;
    const text = seg.text;
    let start = 0;
    for (let k = 0; k < text.length; k++) {
      const c = text.charCodeAt(k);
      if (c === LF) {
        line.push({ text: text.slice(start, k + 1), style: seg.style });
        flush();
        start = k + 1;
      } else if (c === CR) {
        if (k > start) line.push({ text: text.slice(start, k), style: seg.style });
        start = k + 1;
        const next = k + 1 < text.length ? text.charCodeAt(k + 1) : si + 1 < segments.length ? (segments[si + 1] as AnsiSegment).text.charCodeAt(0) : -1;
        // A line ending or the cut end of a frame: the \r goes, the text stays.
        if (next === LF || next === -1) continue;
        line = [];
      }
    }
    if (start < text.length) line.push({ text: text.slice(start), style: seg.style });
  }
  flush();
  return runs.out;
}

/**
 * Split text into runs of one style. Adjacent runs with the same style
 * are merged; empty runs are dropped; plain text comes back as one run.
 */
export function parseAnsi(text: string, options?: ParseAnsiOptions): AnsiSegment[] {
  const runs = new Runs();
  let i = options?.cutStart ? cutPrefix(text) : 0;
  let start = i;
  let style: AnsiStyle = PLAIN;
  const n = text.length;
  while (i < n) {
    if (text.charCodeAt(i) !== ESC) {
      i++;
      continue;
    }
    runs.push(text.slice(start, i), style);
    const esc = scanEscape(text, i);
    if (esc === null) {
      // Cut mid-sequence at the end of the input: nothing after it is text.
      start = n;
      break;
    }
    if (esc.sgr !== null) style = applySgr(style, esc.sgr);
    i = esc.end;
    start = i;
  }
  runs.push(text.slice(start), style);
  return text.indexOf("\r") === -1 ? runs.out : applyCarriageReturns(runs.out);
}
