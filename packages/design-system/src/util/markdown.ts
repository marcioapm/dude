/**
 * A small Markdown parser that emits an AST, never HTML.
 *
 * Why hand-rolled rather than a dependency: the content is untrusted (model
 * output, repository files — plan §25.1), and the two common approaches
 * both fight that. `marked`-style parsers emit HTML strings that then need a
 * sanitiser; `remark`/`micromark` are correct but heavy and their HTML nodes
 * still need policy. Emitting a typed AST and rendering it with React means
 * every string reaches the DOM as a text node. Raw HTML in the source is
 * shown literally; URLs go through one allow-list.
 *
 * The other reason is streaming. Agent messages arrive token by token, and
 * a parser that treats an unterminated fence or an open `**` as literal
 * text flickers between two shapes on every token. With `streaming: true`
 * an unterminated construct at the end of input is treated as *open*: a
 * fence without its closer is still a code block, an open `**` is still
 * bold to the end. When the closer lands nothing moves. With `streaming:
 * false` (a finished message) unmatched delimiters render literally, as
 * CommonMark would.
 *
 * Supported: ATX headings, paragraphs, fenced code, blockquotes, ordered /
 * unordered / task lists (nested by indent), pipe tables, thematic breaks;
 * inline code, strong, emphasis, strikethrough, links, images, autolinks,
 * hard breaks. Not supported on purpose: raw HTML, setext headings,
 * indented code blocks (agents never write them; paths often start with
 * four spaces), footnotes, reference links.
 */

export type Inline =
  | { readonly t: "text"; readonly v: string }
  | { readonly t: "code"; readonly v: string }
  | { readonly t: "strong"; readonly c: readonly Inline[] }
  | { readonly t: "em"; readonly c: readonly Inline[] }
  | { readonly t: "del"; readonly c: readonly Inline[] }
  | { readonly t: "link"; readonly href: string; readonly c: readonly Inline[] }
  | { readonly t: "image"; readonly src: string; readonly alt: string }
  | { readonly t: "br" };

export type TableAlign = "left" | "center" | "right" | null;

export interface ListItem {
  readonly c: readonly Block[];
  /** null = not a task item; otherwise checked state. */
  readonly task: boolean | null;
}

export type Block =
  | { readonly t: "heading"; readonly level: 1 | 2 | 3 | 4 | 5 | 6; readonly c: readonly Inline[]; readonly id: string }
  | { readonly t: "paragraph"; readonly c: readonly Inline[] }
  | { readonly t: "code"; readonly lang: string; readonly v: string; readonly open: boolean }
  | { readonly t: "quote"; readonly c: readonly Block[] }
  | { readonly t: "list"; readonly ordered: boolean; readonly start: number; readonly items: readonly ListItem[] }
  | { readonly t: "table"; readonly align: readonly TableAlign[]; readonly head: readonly (readonly Inline[])[]; readonly rows: readonly (readonly (readonly Inline[])[])[] }
  | { readonly t: "hr" };

export interface ParseOptions {
  /** Treat unterminated constructs at end of input as open (see above). */
  readonly streaming?: boolean | undefined;
}

// ---------------------------------------------------------------------------
// URLs
// ---------------------------------------------------------------------------

const SAFE_PROTOCOLS = /^(https?|mailto):/i;

/**
 * Allow http(s), mailto, and relative / fragment URLs. Everything else —
 * `javascript:`, `data:`, `vbscript:`, `file:` — becomes null and the link
 * renders as plain text. Applied to links and images alike.
 */
export function safeUrl(raw: string): string | null {
  const url = raw.trim().replace(/[\u0000-\u001f\u007f]/g, "");
  if (url.length === 0) return null;
  if (/^[a-z][a-z0-9+.-]*:/i.test(url)) return SAFE_PROTOCOLS.test(url) ? url : null;
  if (url.startsWith("//")) return null;
  return url;
}

// ---------------------------------------------------------------------------
// Block level
// ---------------------------------------------------------------------------

const FENCE_RE = /^(\s{0,3})(`{3,}|~{3,})\s*([^\s`]*)\s*(.*)$/;
const HEADING_RE = /^(#{1,6})(?:[ \t]+(.*?))?[ \t]*#*[ \t]*$/;
const HR_RE = /^[ \t]{0,3}([-*_])(?:[ \t]*\1){2,}[ \t]*$/;
const UL_RE = /^(\s*)([-*+])(?:[ \t]+(.*)|$)/;
const OL_RE = /^(\s*)(\d{1,9})[.)](?:[ \t]+(.*)|$)/;
const QUOTE_RE = /^\s{0,3}>[ \t]?(.*)$/;
const TABLE_SEP_RE = /^\s*\|?\s*:?-+:?\s*(\|\s*:?-+:?\s*)*\|?\s*$/;
const TASK_RE = /^\[( |x|X)\][ \t]+/;

export function parseMarkdown(src: string, opts: ParseOptions = {}): Block[] {
  const streaming = opts.streaming === true;
  const lines = src.replace(/\r\n?/g, "\n").split("\n");
  const usedIds = new Map<string, number>();
  return parseBlocks(lines, streaming, usedIds);
}

function parseBlocks(lines: readonly string[], streaming: boolean, usedIds: Map<string, number>): Block[] {
  const out: Block[] = [];
  let i = 0;
  while (i < lines.length) {
    const line = lines[i] ?? "";
    if (line.trim() === "") {
      i++;
      continue;
    }

    // Fenced code
    const fence = FENCE_RE.exec(line);
    if (fence) {
      const marker = fence[2] ?? "```";
      const lang = (fence[3] ?? "").toLowerCase();
      const body: string[] = [];
      let j = i + 1;
      let closed = false;
      for (; j < lines.length; j++) {
        const l = lines[j] ?? "";
        const m = /^\s{0,3}(`{3,}|~{3,})\s*$/.exec(l);
        if (m && (m[1] ?? "").charAt(0) === marker.charAt(0) && (m[1] ?? "").length >= marker.length) {
          closed = true;
          break;
        }
        body.push(l);
      }
      out.push({ t: "code", lang, v: body.join("\n"), open: !closed });
      i = closed ? j + 1 : j;
      continue;
    }

    // Heading
    const h = HEADING_RE.exec(line);
    if (h) {
      const level = Math.min(6, (h[1] ?? "#").length) as 1 | 2 | 3 | 4 | 5 | 6;
      const text = h[2] ?? "";
      out.push({ t: "heading", level, c: parseInline(text, streaming), id: slug(text, usedIds) });
      i++;
      continue;
    }

    // Thematic break (checked before lists: `---` and `* * *` overlap)
    if (HR_RE.test(line)) {
      out.push({ t: "hr" });
      i++;
      continue;
    }

    // Blockquote
    if (QUOTE_RE.test(line)) {
      const inner: string[] = [];
      let j = i;
      for (; j < lines.length; j++) {
        const l = lines[j] ?? "";
        const q = QUOTE_RE.exec(l);
        if (q) inner.push(q[1] ?? "");
        else if (l.trim() !== "" && inner.length > 0 && !isBlockStart(l)) inner.push(l); // lazy continuation
        else break;
      }
      out.push({ t: "quote", c: parseBlocks(inner, streaming, usedIds) });
      i = j;
      continue;
    }

    // Lists
    const li = matchListMarker(line);
    if (li) {
      const ordered = li.ordered;
      const items: ListItem[] = [];
      let j = i;
      let start = li.start;
      while (j < lines.length) {
        const l = lines[j] ?? "";
        const m = matchListMarker(l);
        if (!m || m.ordered !== ordered || m.indent !== li.indent) break;
        if (items.length === 0) start = m.start;
        // Gather this item's lines: the first (marker stripped) plus any
        // following lines indented to the content column, and blank lines
        // that are followed by such a line.
        const contentIndent = m.indent + m.markerWidth;
        const buf: string[] = [];
        let first = m.content;
        let task: boolean | null = null;
        const tm = TASK_RE.exec(first);
        if (tm) {
          task = (tm[1] ?? " ") !== " ";
          first = first.slice(tm[0].length);
        } else if (/^\[( |x|X)\]$/.test(first)) {
          task = first !== "[ ]";
          first = "";
        }
        buf.push(first);
        let k = j + 1;
        for (; k < lines.length; k++) {
          const l2 = lines[k] ?? "";
          if (l2.trim() === "") {
            // Blank: keep only if the next non-blank line continues the item.
            const next = nextNonBlank(lines, k);
            if (next !== -1 && leadingSpaces(lines[next] ?? "") >= contentIndent) {
              buf.push("");
              continue;
            }
            break;
          }
          if (leadingSpaces(l2) >= contentIndent) {
            buf.push(l2.slice(contentIndent));
            continue;
          }
          // Lazy continuation of a paragraph inside the item.
          if (!matchListMarker(l2) && !isBlockStart(l2) && (buf[buf.length - 1] ?? "").trim() !== "") {
            buf.push(l2.trim());
            continue;
          }
          break;
        }
        items.push({ c: parseBlocks(buf, streaming, usedIds), task });
        j = k;
      }
      out.push({ t: "list", ordered, start, items });
      i = j;
      continue;
    }

    // Table: a header row followed by a separator row.
    if (line.includes("|") && TABLE_SEP_RE.test(lines[i + 1] ?? "")) {
      const head = splitRow(line).map((c) => parseInline(c, streaming));
      const align = splitRow(lines[i + 1] ?? "").map<TableAlign>((c) => {
        const s = c.trim();
        const l = s.startsWith(":");
        const r = s.endsWith(":");
        return l && r ? "center" : r ? "right" : l ? "left" : null;
      });
      const rows: (readonly Inline[])[][] = [];
      let j = i + 2;
      for (; j < lines.length; j++) {
        const l = lines[j] ?? "";
        if (l.trim() === "" || !l.includes("|")) break;
        rows.push(splitRow(l).map((c) => parseInline(c, streaming)));
      }
      out.push({ t: "table", align, head, rows });
      i = j;
      continue;
    }

    // Paragraph: consecutive non-blank lines until something else starts.
    const buf: string[] = [line];
    let j = i + 1;
    for (; j < lines.length; j++) {
      const l = lines[j] ?? "";
      if (l.trim() === "" || isBlockStart(l)) break;
      if (l.includes("|") && TABLE_SEP_RE.test(lines[j + 1] ?? "")) break;
      buf.push(l);
    }
    out.push({ t: "paragraph", c: parseInline(buf.join("\n"), streaming) });
    i = j;
  }
  return out;
}

function isBlockStart(l: string): boolean {
  return FENCE_RE.test(l) || HEADING_RE.test(l) || HR_RE.test(l) || QUOTE_RE.test(l) || matchListMarker(l) !== null;
}

function matchListMarker(l: string): { indent: number; markerWidth: number; ordered: boolean; start: number; content: string } | null {
  const u = UL_RE.exec(l);
  if (u) {
    const indent = (u[1] ?? "").length;
    const rest = u[3] ?? "";
    // "- " followed by content; width = marker + one space (extra spaces are content indent)
    return { indent, markerWidth: 2, ordered: false, start: 1, content: rest };
  }
  const o = OL_RE.exec(l);
  if (o) {
    const indent = (o[1] ?? "").length;
    const num = o[2] ?? "1";
    return { indent, markerWidth: num.length + 2, ordered: true, start: Number.parseInt(num, 10), content: o[3] ?? "" };
  }
  return null;
}

function leadingSpaces(l: string): number {
  let n = 0;
  while (n < l.length && (l.charAt(n) === " " || l.charAt(n) === "\t")) n++;
  return n;
}

function nextNonBlank(lines: readonly string[], from: number): number {
  for (let k = from; k < lines.length; k++) if ((lines[k] ?? "").trim() !== "") return k;
  return -1;
}

function splitRow(l: string): string[] {
  let s = l.trim();
  if (s.startsWith("|")) s = s.slice(1);
  if (s.endsWith("|") && !s.endsWith("\\|")) s = s.slice(0, -1);
  const cells: string[] = [];
  let cur = "";
  let inCode = false;
  for (let i = 0; i < s.length; i++) {
    const ch = s.charAt(i);
    if (ch === "`") inCode = !inCode;
    if (ch === "\\" && s.charAt(i + 1) === "|") {
      cur += "|";
      i++;
      continue;
    }
    if (ch === "|" && !inCode) {
      cells.push(cur.trim());
      cur = "";
      continue;
    }
    cur += ch;
  }
  cells.push(cur.trim());
  return cells;
}

function slug(text: string, used: Map<string, number>): string {
  const base =
    text
      .toLowerCase()
      .replace(/[`*_~[\]()!]/g, "")
      .replace(/[^a-z0-9\s-]/g, "")
      .trim()
      .replace(/\s+/g, "-") || "section";
  const n = used.get(base) ?? 0;
  used.set(base, n + 1);
  return n === 0 ? base : `${base}-${n}`;
}

// ---------------------------------------------------------------------------
// Inline level
// ---------------------------------------------------------------------------

const URL_RE = /^https?:\/\/[^\s<>()]+[^\s<>().,;:!?'"]/;

export function parseInline(src: string, streaming: boolean): Inline[] {
  return parseInlineRange(src, 0, src.length, streaming, null).nodes;
}

interface InlineResult {
  readonly nodes: Inline[];
  readonly end: number;
}

/**
 * Scan `src[from, to)` producing inline nodes. `closer` is the delimiter
 * that ends the current run (for nested emphasis); when found, `end` is
 * the index just past it. When not found: if streaming, the run extends
 * to `to` and stays open; if not, the caller renders the opener literally.
 */
function parseInlineRange(src: string, from: number, to: number, streaming: boolean, closer: string | null): InlineResult {
  const nodes: Inline[] = [];
  let text = "";
  const flush = () => {
    if (text.length > 0) {
      nodes.push({ t: "text", v: text });
      text = "";
    }
  };
  let i = from;
  while (i < to) {
    const ch = src.charAt(i);

    if (closer !== null && src.startsWith(closer, i) && (closer.length > 1 || !isFlankingSpace(src, i, closer))) {
      flush();
      return { nodes, end: i + closer.length };
    }

    // Escapes
    if (ch === "\\" && i + 1 < to) {
      const nx = src.charAt(i + 1);
      if (nx === "\n") {
        flush();
        nodes.push({ t: "br" });
        i += 2;
        continue;
      }
      if (/[\\`*_{}[\]()#+\-.!~|>]/.test(nx)) {
        text += nx;
        i += 2;
        continue;
      }
    }

    // Hard break: two+ spaces before newline. Soft break otherwise.
    if (ch === "\n") {
      if (text.endsWith("  ")) {
        text = text.replace(/ +$/, "");
        flush();
        nodes.push({ t: "br" });
      } else {
        text += " ";
      }
      i++;
      continue;
    }

    // Code span
    if (ch === "`") {
      let n = 0;
      while (src.charAt(i + n) === "`" && i + n < to) n++;
      const ticks = "`".repeat(n);
      const close = src.indexOf(ticks, i + n);
      if (close !== -1 && close < to) {
        flush();
        nodes.push({ t: "code", v: src.slice(i + n, close).replace(/\n/g, " ").trim() });
        i = close + n;
        continue;
      }
      if (streaming) {
        flush();
        nodes.push({ t: "code", v: src.slice(i + n, to).replace(/\n/g, " ") });
        i = to;
        continue;
      }
      text += ticks;
      i += n;
      continue;
    }

    // Image / link
    if (ch === "!" && src.charAt(i + 1) === "[") {
      const link = parseLink(src, i + 1, to, streaming);
      if (link) {
        flush();
        const s = safeUrl(link.href);
        const alt = plain(link.nodes);
        if (s) nodes.push({ t: "image", src: s, alt });
        else nodes.push({ t: "text", v: alt });
        i = link.end;
        continue;
      }
    }
    if (ch === "[") {
      const link = parseLink(src, i, to, streaming);
      if (link) {
        flush();
        const s = safeUrl(link.href);
        if (s) nodes.push({ t: "link", href: s, c: link.nodes });
        else nodes.push(...link.nodes);
        i = link.end;
        continue;
      }
    }

    // Autolink <https://…> and bare URLs
    if (ch === "<") {
      const m = /^<(https?:\/\/[^\s<>]+)>/.exec(src.slice(i, to));
      if (m) {
        flush();
        const href = safeUrl(m[1] ?? "");
        if (href) nodes.push({ t: "link", href, c: [{ t: "text", v: m[1] ?? "" }] });
        else text += m[0];
        i += m[0].length;
        continue;
      }
    }
    if (ch === "h" && (src.startsWith("http://", i) || src.startsWith("https://", i)) && (i === 0 || /[\s(]/.test(src.charAt(i - 1)))) {
      const m = URL_RE.exec(src.slice(i, to));
      if (m) {
        flush();
        const href = safeUrl(m[0]);
        if (href) nodes.push({ t: "link", href, c: [{ t: "text", v: m[0] }] });
        else text += m[0];
        i += m[0].length;
        continue;
      }
    }

    // Emphasis / strong / strikethrough
    const delim = matchDelimiter(src, i, to);
    if (delim) {
      const inner = parseInlineRange(src, i + delim.length, to, streaming, delim);
      const closed = inner.end <= to && src.startsWith(delim, inner.end - delim.length);
      if (closed && inner.nodes.length > 0) {
        flush();
        nodes.push(wrap(delim, inner.nodes));
        i = inner.end;
        continue;
      }
      if (streaming && inner.nodes.length > 0 && inner.end >= to) {
        flush();
        nodes.push(wrap(delim, inner.nodes));
        i = to;
        continue;
      }
      text += delim;
      i += delim.length;
      continue;
    }

    text += ch;
    i++;
  }
  flush();
  // Reached `to` without a closer.
  return { nodes, end: to + (closer?.length ?? 0) };
}

function wrap(delim: string, c: Inline[]): Inline {
  if (delim === "**" || delim === "__") return { t: "strong", c };
  if (delim === "~~") return { t: "del", c };
  return { t: "em", c };
}

/** Left-flanking opener: not followed by whitespace, and for `_` not intraword. */
function matchDelimiter(src: string, i: number, to: number): string | null {
  const two = src.slice(i, i + 2);
  const one = src.charAt(i);
  const after2 = src.charAt(i + 2);
  const after1 = src.charAt(i + 1);
  const before = i > 0 ? src.charAt(i - 1) : " ";
  if ((two === "**" || two === "~~" || two === "__") && i + 2 < to && after2 !== "" && !/\s/.test(after2)) {
    if (two === "__" && /\w/.test(before)) return null;
    return two;
  }
  if ((one === "*" || one === "_") && i + 1 < to && after1 !== "" && !/\s/.test(after1) && after1 !== one) {
    if (one === "_" && /\w/.test(before)) return null;
    return one;
  }
  return null;
}

/** A closer must not be preceded by whitespace (so "a * b * c" is not emphasis). */
function isFlankingSpace(src: string, i: number, closer: string): boolean {
  const before = src.charAt(i - 1);
  const after = src.charAt(i + closer.length);
  if (/\s/.test(before)) return true;
  if (closer === "_" && /\w/.test(after)) return true;
  return false;
}

function parseLink(src: string, from: number, to: number, streaming: boolean): { nodes: Inline[]; href: string; end: number } | null {
  // from points at "["
  let depth = 0;
  let j = from;
  for (; j < to; j++) {
    const c = src.charAt(j);
    if (c === "\\") {
      j++;
      continue;
    }
    if (c === "[") depth++;
    else if (c === "]") {
      depth--;
      if (depth === 0) break;
    }
  }
  if (j >= to || src.charAt(j + 1) !== "(") return null;
  let k = j + 2;
  let pd = 0;
  for (; k < to; k++) {
    const c = src.charAt(k);
    if (c === "\\") {
      k++;
      continue;
    }
    if (c === "(") pd++;
    else if (c === ")") {
      if (pd === 0) break;
      pd--;
    }
  }
  if (k >= to) return null;
  const label = parseInlineRange(src, from + 1, j, streaming, null).nodes;
  let href = src.slice(j + 2, k).trim();
  // Strip an optional title: (url "title")
  const tm = /^(\S+)\s+["'(].*["')]$/.exec(href);
  if (tm) href = tm[1] ?? href;
  if (href.startsWith("<") && href.endsWith(">")) href = href.slice(1, -1);
  return { nodes: label, href, end: k + 1 };
}

/** Plain-text projection of inline nodes (for alt text and outlines). */
export function plain(nodes: readonly Inline[]): string {
  let s = "";
  for (const n of nodes) {
    switch (n.t) {
      case "text":
      case "code":
        s += n.v;
        break;
      case "strong":
      case "em":
      case "del":
      case "link":
        s += plain(n.c);
        break;
      case "image":
        s += n.alt;
        break;
      case "br":
        s += " ";
        break;
    }
  }
  return s;
}

/** Headings, for an outline. */
export function outline(blocks: readonly Block[]): Array<{ level: number; text: string; id: string }> {
  const out: Array<{ level: number; text: string; id: string }> = [];
  for (const b of blocks) if (b.t === "heading") out.push({ level: b.level, text: plain(b.c), id: b.id });
  return out;
}
