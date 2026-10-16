export type GitHubSection =
  | { kind: "markdown"; source: string }
  | { kind: "details"; summary: string; children: GitHubSection[] };

// Literal tokens are code, escapes and autolinks: passed to Markdown as written, never read for tags or entities.
type Token =
  | { t: "text"; v: string }
  | { t: "literal"; v: string }
  | { t: "comment"; v: string }
  | { t: "tag"; name: string; closing: boolean; href: string };

const ENTITIES: Record<string, string> = { "&amp;": "&", "&lt;": "<", "&gt;": ">", "&quot;": '"', "&#39;": "'", "&nbsp;": " " };
const decode = (text: string) => text.replace(/&(?:amp|lt|gt|quot|#39|nbsp);/g, (entity) => ENTITIES[entity]!);
const BLANKS = /\n[ \t]*\n(?:[ \t]*\n)+/g;
// Any other name, `<b when c>` or `<https://…>` is text, as GitHub shows it.
const TAGS = new Set(("details summary picture source img a strong b em i code br p div span dl dt dd sub sup ul ol li "
  + "table thead tbody tfoot tr th td caption colgroup col hr h1 h2 h3 h4 h5 h6 blockquote pre kbd samp var tt "
  + "s del ins strike u mark small abbr cite q figure figcaption").split(" "));
const BOOLEAN_ATTRIBUTES = new Set(["open", "hidden", "disabled", "checked", "nowrap", "reversed"]);
// A tag GitHub writes is far shorter; a `<` that scans further is text.
const MAX_TAG = 2048;
const MAX_AUTOLINK = 2048;
// As the design system's Markdown bounds quotes and lists: deeper disclosures are flattened into their parent.
const MAX_DEPTH = 32;

const SCHEME = /[a-zA-Z0-9+.-]/;
const EMAIL = /[\w.!#$%&'*+/=?^`{|}~-]/;
const HOST = /[a-zA-Z0-9.-]/;
const ATTRIBUTE_START = /[a-zA-Z_:]/;
const ATTRIBUTE = /[\w:.-]/;
const UNQUOTED_STOP = /["'=<>`]/;
function isSpace(c: number): boolean { return c === 32 || c === 9 || c === 10 || c === 13 || c === 12; }
function isLetter(c: number): boolean { return (c >= 65 && c <= 90) || (c >= 97 && c <= 122); }
function isDigit(c: number): boolean { return c >= 48 && c <= 57; }

/** GitHub's HTML becomes text and disclosures, never HTML to inject. */
export function githubBody(source: string): GitHubSection[] {
  return build(tokenize(source));
}

interface LinePrefix { quotes: number; column: number; listContent: number; at: number }

// The `>`, list markers and indent before a line's content; `column` counts from the last `>`.
function linePrefix(src: string, i: number): LinePrefix {
  let quotes = 0, column = 0, listContent = -1, j = i;
  for (;;) {
    const c = src.charCodeAt(j);
    if (c === 32 || c === 9) { j++; column++; continue; }
    if (c === 62) {
      quotes++; j++; column = 0; listContent = -1;
      if (src.charCodeAt(j) === 32) j++;
      continue;
    }
    if ((c === 45 || c === 42 || c === 43) && (src.charCodeAt(j + 1) === 32 || src.charCodeAt(j + 1) === 9)) {
      j += 2; column += 2; listContent = column;
      continue;
    }
    let d = j;
    while (d - j < 9 && isDigit(src.charCodeAt(d))) d++;
    const after = src.charCodeAt(d);
    if (d > j && (after === 46 || after === 41) && (src.charCodeAt(d + 1) === 32 || src.charCodeAt(d + 1) === 9)) {
      column += d + 2 - j; j = d + 2; listContent = column;
      continue;
    }
    return { quotes, column, listContent, at: j };
  }
}

function fenceRun(src: string, at: number): number {
  const c = src.charCodeAt(at);
  if (c !== 96 && c !== 126) return 0;
  let j = at;
  while (src.charCodeAt(j) === c) j++;
  return j - at >= 3 ? j - at : 0;
}

// The end of a fence opening at line start `i`, or -1. A closer needs the opener's `>` depth and an indent
// within three of the opener's container column (a list item's content column); an unclosed fence runs to the end.
function fenceEnd(src: string, i: number): number {
  const open = linePrefix(src, i);
  const length = fenceRun(src, open.at);
  if (!length) return -1;
  const char = src.charCodeAt(open.at);
  let lineEnd = src.indexOf("\n", open.at);
  if (lineEnd < 0) lineEnd = src.length;
  // A backtick in the info string makes the line a code span, not a fence.
  if (char === 96 && src.lastIndexOf("`", lineEnd) >= open.at + length) return -1;
  const column = open.listContent >= 0 ? open.listContent : Math.max(0, open.column - 3);
  while (lineEnd < src.length) {
    const start = lineEnd + 1;
    lineEnd = src.indexOf("\n", start);
    if (lineEnd < 0) lineEnd = src.length;
    const line = linePrefix(src, start);
    if (line.quotes !== open.quotes || line.column < column || line.column > column + 3) continue;
    const closingLength = fenceRun(src, line.at);
    if (src.charCodeAt(line.at) !== char || closingLength < length) continue;
    let j = line.at + closingLength;
    while (src.charCodeAt(j) === 32 || src.charCodeAt(j) === 9) j++;
    if (j === lineEnd) return lineEnd;
  }
  return src.length;
}

// `<scheme:…>` or `<user@host>`: the end past `>`, or -1.
function autolinkEnd(src: string, i: number): number {
  const limit = Math.min(src.length, i + MAX_AUTOLINK);
  let j = i + 1;
  if (isLetter(src.charCodeAt(j))) {
    while (j < limit && j - i <= 32 && SCHEME.test(src[j] ?? "")) j++;
    if (j < limit && src[j] === ":" && j - i >= 3) {
      j++;
      while (j < limit && src.charCodeAt(j) > 32 && src.charCodeAt(j) !== 60 && src.charCodeAt(j) !== 62) j++;
      if (j < limit && src[j] === ">") return j + 1;
    }
  }
  j = i + 1;
  while (j < limit && src.charCodeAt(j) > 32 && EMAIL.test(src[j]!)) j++;
  if (j >= limit || j === i + 1 || src[j] !== "@") return -1;
  const host = ++j;
  while (j < limit && HOST.test(src[j]!)) j++;
  return j < limit && j > host && src[j] === ">" ? j + 1 : -1;
}

// A recognised tag at `i` with HTML-shaped attributes, read forward once and at most MAX_TAG long.
function readTag(src: string, i: number): { token: Token; end: number } | null {
  let j = i + 1;
  const closing = src[j] === "/";
  if (closing) j++;
  const nameStart = j;
  if (!isLetter(src.charCodeAt(j))) return null;
  while (j - nameStart < 16 && (isLetter(src.charCodeAt(j)) || isDigit(src.charCodeAt(j)) || src[j] === "-")) j++;
  const name = src.slice(nameStart, j).toLowerCase();
  if (!TAGS.has(name)) return null;
  const limit = Math.min(src.length, i + MAX_TAG);
  let href = "";
  for (;;) {
    const gap = j;
    while (j < limit && isSpace(src.charCodeAt(j))) j++;
    if (j >= limit) return null;
    if (src[j] === ">") return { token: { t: "tag", name, closing, href }, end: j + 1 };
    if (!closing && src[j] === "/" && src[j + 1] === ">") return { token: { t: "tag", name, closing, href }, end: j + 2 };
    if (closing || j === gap) return null;
    const attrStart = j;
    if (!ATTRIBUTE_START.test(src[j]!)) return null;
    while (j < limit && ATTRIBUTE.test(src[j]!)) j++;
    const attr = src.slice(attrStart, j).toLowerCase();
    const afterName = j;
    while (j < limit && isSpace(src.charCodeAt(j))) j++;
    if (src[j] !== "=") {
      if (!BOOLEAN_ATTRIBUTES.has(attr)) return null;
      j = afterName;
      continue;
    }
    j++;
    while (j < limit && isSpace(src.charCodeAt(j))) j++;
    const quote = src[j];
    let value: string;
    if (quote === '"' || quote === "'") {
      const valueStart = ++j;
      while (j < limit && src[j] !== quote && src[j] !== "\n") j++;
      if (src[j] !== quote) return null;
      value = src.slice(valueStart, j++);
    } else {
      const valueStart = j;
      while (j < limit && !isSpace(src.charCodeAt(j)) && !UNQUOTED_STOP.test(src[j]!)) j++;
      if (j === valueStart) return null;
      value = src.slice(valueStart, j);
    }
    if (attr === "href") href = value;
  }
}

function tokenize(src: string): Token[] {
  const tokens: Token[] = [];
  const text = (v: string) => { if (v) tokens.push({ t: "text", v: decode(v) }); };
  const literal = (v: string) => tokens.push({ t: "literal", v });
  // Backtick runs by length, each list read forward once, so finding a closer is amortised linear.
  let runs: Map<number, { at: number[]; next: number }> | null = null;
  const closer = (length: number, from: number) => {
    if (!runs) {
      runs = new Map();
      for (let k = src.indexOf("`"); k >= 0; k = src.indexOf("`", k)) {
        const start = k;
        while (src.charCodeAt(k) === 96) k++;
        const list = runs.get(k - start) ?? { at: [], next: 0 };
        list.at.push(start);
        runs.set(k - start, list);
      }
    }
    const list = runs.get(length);
    if (!list) return -1;
    while (list.next < list.at.length && list.at[list.next]! < from) list.next++;
    return list.at[list.next] ?? -1;
  };
  const special = /[<`\\\n]/g;
  for (let i = 0; i < src.length;) {
    if (i === 0 || src.charCodeAt(i - 1) === 10) {
      // fenceEnd only walks lines that end up in the literal it returns (an unclosed fence takes the rest), so no line is walked twice.
      const end = fenceEnd(src, i);
      if (end >= 0) { literal(src.slice(i, end)); i = end; continue; }
    }
    const c = src[i];
    if (c === "\n") { text("\n"); i++; continue; }
    if (c === "\\" && i + 1 < src.length) { literal(src.slice(i, i + 2)); i += 2; continue; }
    if (c === "`") {
      let j = i;
      while (src.charCodeAt(j) === 96) j++;
      const close = closer(j - i, j);
      if (close < 0) text(src.slice(i, j));
      else literal(src.slice(i, (j = close + j - i)));
      i = j;
      continue;
    }
    if (c === "<") {
      if (src.startsWith("<!--", i)) {
        const end = src.indexOf("-->", i + 4);
        tokens.push({ t: "comment", v: src.slice(i + 4, end < 0 ? src.length : end).trim() });
        i = end < 0 ? src.length : end + 3;
        continue;
      }
      const link = autolinkEnd(src, i);
      if (link > 0) { literal(src.slice(i, link)); i = link; continue; }
      const tag = readTag(src, i);
      if (tag) { tokens.push(tag.token); i = tag.end; continue; }
    }
    special.lastIndex = i + 1;
    const next = special.exec(src)?.index ?? src.length;
    text(src.slice(i, next));
    i = next;
  }
  return tokens;
}

interface Frame {
  tag: "root" | "details" | "summary" | "code" | "a";
  parts: { v: string; literal: boolean }[];
  sections: GitHubSection[];
  summary: string | null;
  href: string;
  // Disclosures past MAX_DEPTH still open in this frame, and whether the latest awaits its summary.
  flat: number;
  flatSummary: boolean;
}

const frame = (tag: Frame["tag"], href = ""): Frame => ({ tag, parts: [], sections: [], summary: null, href, flat: 0, flatSummary: false });

function append(target: Frame, v: string, literal = false) {
  const last = target.parts.at(-1);
  if (last && last.literal === literal) last.v += v;
  else target.parts.push({ v, literal });
}

// Removed tags and comments leave blank runs; code keeps its own.
const cleaned = (target: Frame) => target.parts.map((p) => p.literal ? p.v : p.v.replace(BLANKS, "\n\n")).join("").trim();

function flush(target: Frame) {
  const source = cleaned(target);
  if (source) target.sections.push({ kind: "markdown", source });
  target.parts = [];
}

const safeUrl = (url: string) => url.replace(/[\s()<>]/g, (c) => encodeURIComponent(c).replace(/\(/g, "%28").replace(/\)/g, "%29"));

function build(tokens: Token[]): GitHubSection[] {
  // The autofix block is dropped through its end marker, when it has one.
  const blockEnd = new Int32Array(tokens.length);
  for (let k = tokens.length - 1, next = -1; k >= 0; k--) {
    blockEnd[k] = next;
    const token = tokens[k]!;
    if (token.t === "comment" && token.v === "autofix_checkbox_end") next = k;
  }
  const stack: Frame[] = [frame("root")];
  const top = () => stack.at(-1)!;
  // Summary, code and link frames hold text only: no disclosure or other frame opens in them, so at most one is on top.
  const plain = () => top().tag !== "root" && top().tag !== "details";
  const close = () => {
    const done = stack.pop()!;
    const parent = top();
    if (done.tag === "details") {
      flush(done);
      parent.sections.push({ kind: "details", summary: done.summary ?? "Details", children: done.sections });
    } else if (done.tag === "summary") {
      const label = cleaned(done);
      if (parent.flatSummary) {
        parent.flatSummary = false;
        if (label) append(parent, `\n\n**${label}**\n\n`);
      } else if (parent.tag === "details" && parent.summary === null) parent.summary = label || "Details";
      else append(parent, label);
    } else if (done.tag === "code") {
      const content = cleaned(done);
      let longest = 0;
      for (const run of content.matchAll(/`+/g)) longest = Math.max(longest, run[0].length);
      const ticks = "`".repeat(longest + 1);
      append(parent, `${ticks} ${content} ${ticks}`, true);
    } else if (done.tag === "a") {
      const label = cleaned(done);
      const url = decode(done.href);
      append(parent, label && /^https?:\/\//i.test(url) ? `[${label.replace(/[\[\]\\]/g, "\\$&")}](${safeUrl(url)})` : label);
    }
  };
  let picture = false;
  for (let k = 0; k < tokens.length; k++) {
    const token = tokens[k]!;
    if (token.t === "comment") {
      if (token.v === "autofix_checkbox_start" && blockEnd[k]! >= 0) k = blockEnd[k]!;
      continue;
    }
    if (token.t !== "tag") {
      if (!picture) append(top(), token.v, token.t === "literal");
      continue;
    }
    const { name, closing } = token;
    if (closing) {
      if (name === "picture") { picture = false; continue; }
      if (name === "details") {
        if (!stack.some((f) => f.tag === "details")) continue;
        picture = false;
        while (plain()) close();
        if (top().flat > 0) { top().flat--; top().flatSummary = false; append(top(), "\n\n"); }
        else close();
      } else if (plain() && top().tag === name) {
        picture = false;
        close();
      } else if (!picture && !plain() && (name === "strong" || name === "b")) append(top(), "**");
      else if (!picture && !plain() && (name === "em" || name === "i")) append(top(), "*");
      continue;
    }
    // A picture is a pixel or a badge: nothing in it is read.
    if (picture) continue;
    if (name === "picture") picture = true;
    else if (name === "br") append(top(), "\n");
    else if (plain()) continue;
    else if (name === "details") {
      if (stack.length <= MAX_DEPTH) { flush(top()); stack.push(frame("details")); }
      else { top().flat++; top().flatSummary = true; append(top(), "\n\n"); }
    } else if (name === "summary" || name === "code") stack.push(frame(name));
    else if (name === "a") stack.push(frame("a", token.href));
    else if (name === "strong" || name === "b") append(top(), "**");
    else if (name === "em" || name === "i") append(top(), "*");
  }
  while (stack.length > 1) close();
  flush(stack[0]!);
  return stack[0]!.sections;
}
