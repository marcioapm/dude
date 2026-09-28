/**
 * Markdown source, lightly highlighted for editing: headings, list markers,
 * code, bold, quotes and `{{variables}}` — enough to read the structure of
 * a prompt while writing it. Tokens, never HTML: the editor renders them as
 * spans under a transparent textarea, so what is typed is what is shown.
 */

export type SourceTokenKind = "text" | "headingMark" | "heading" | "listMark" | "code" | "fence" | "bold" | "quote" | "variable";

export interface SourceToken {
  readonly kind: SourceTokenKind;
  readonly text: string;
}

const INLINE = /(\{\{[^}]+\}\}|`[^`]+`|\*\*[^*]+\*\*)/g;

function inline(text: string): SourceToken[] {
  const out: SourceToken[] = [];
  let last = 0;
  for (const m of text.matchAll(INLINE)) {
    if (m.index > last) out.push({ kind: "text", text: text.slice(last, m.index) });
    const t = m[0];
    out.push({ kind: t.startsWith("{{") ? "variable" : t.startsWith("`") ? "code" : "bold", text: t });
    last = m.index + t.length;
  }
  if (last < text.length) out.push({ kind: "text", text: text.slice(last) });
  return out;
}

/** The source as lines of tokens. Joined, each line's tokens are exactly the line. */
export function markdownSourceTokens(source: string): SourceToken[][] {
  let inFence = false;
  return source.split("\n").map((line): SourceToken[] => {
    if (line.startsWith("```")) {
      inFence = !inFence;
      return [{ kind: "fence", text: line }];
    }
    if (inFence) return line ? [{ kind: "code", text: line }] : [];
    let m = /^(#{1,6} )(.*)$/.exec(line);
    if (m) return ([{ kind: "headingMark", text: m[1]! }, { kind: "heading", text: m[2]! }] as SourceToken[]).filter((t) => t.text);
    if (line.startsWith("> ")) return [{ kind: "quote", text: line }];
    m = /^(\s*(?:[-*]|\d+\.) )(.*)$/.exec(line);
    if (m) return [{ kind: "listMark", text: m[1]! }, ...inline(m[2]!)];
    return inline(line);
  });
}

/** Lines and words, as the editor counts them. */
export function sourceCounts(source: string): { lines: number; words: number } {
  const trimmed = source.trim();
  return { lines: source ? source.split("\n").length : 0, words: trimmed ? trimmed.split(/\s+/).length : 0 };
}
