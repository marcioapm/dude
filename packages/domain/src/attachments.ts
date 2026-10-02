/**
 * Images a person sends an agent with a steer, an answer or a task's
 * prompt: the limits the browser, the backend and the orchestrator share.
 * lux's own (feat/input-attachments): at most 10 per input, 5 MiB each
 * decoded, the whole JSON body under 8 MiB — so a message's images stay
 * under ~5.5 MiB in total.
 */

export const ATTACHMENT_TYPES = ["image/png", "image/jpeg", "image/webp", "image/gif"] as const;
export type AttachmentType = (typeof ATTACHMENT_TYPES)[number];

export const ATTACHMENT_LIMITS = {
  /** Images in one message. */
  perMessage: 6,
  /** An image as picked, before the browser scales it. */
  originalBytes: 10 * 1000 * 1000,
  /** What the agent is sent: one image. */
  deliveredBytes: Math.floor(4.5 * 1024 * 1024),
  /** What the agent is sent: one message's images together. */
  messageBytes: 5 * 1024 * 1024,
  /** The long side of what the agent is sent, in pixels. */
  maxSide: 2000,
} as const;

/** The URL scheme a task's Markdown names one of its images by: `![name](attachment:att_…)`. */
export const ATTACHMENT_SCHEME = "attachment:";

/** One `![alt](attachment:id)` in a text: where it is, and what it names. */
export interface AttachmentReference {
  id: string;
  alt: string;
  /** The title's text, without its quotes: where the task dialog keeps an image's layout. */
  title?: string;
  /** Offsets of the whole `![…](…)` in the text. */
  from: number;
  to: number;
}

// `![alt](attachment:id)`, `(<attachment:id>)`, and either with a title. The
// orchestrator's delivery.attachmentRef is the same expression. Blanks are
// ASCII space and tab only, and the alt holds no `[`: one meaning in both
// languages, and a scan that stays linear on a run of `![`.
const REFERENCE = /!\[((?:\\[^\n]|[^[\]\\\n])*)\]\([ \t]*<?attachment:(att_[A-Za-z0-9]+)>?(?:[ \t]+(?:"([^"\n]*)"|'([^'\n]*)'|\(([^)\n]*)\)))?[ \t]*\)/dg;
const FENCE = /^ {0,3}(`{3,}|~{3,})/;
const trimBlanks = (s: string) => s.replace(/^[ \t]+|[ \t]+$/g, "");

/**
 * The attachment references in Markdown, in order. Inside a fenced code
 * block or a code span it is text, as Markdown renders it, not a reference.
 */
export function attachmentReferences(text: string): AttachmentReference[] {
  const out: AttachmentReference[] = [];
  let fence: string | null = null;
  let offset = 0;
  for (const line of text.split("\n")) {
    const opener = FENCE.exec(line)?.[1];
    if (fence !== null) {
      if (opener && opener[0] === fence[0] && opener.length >= fence.length && trimBlanks(line) === opener) fence = null;
    } else if (opener) {
      fence = opener;
    } else {
      const masked = maskCodeSpans(line);
      for (const m of masked.matchAll(REFERENCE)) {
        const ref: AttachmentReference = { id: m[2]!, alt: line.slice(m.index + 2, m.index + 2 + m[1]!.length).replace(/\\([^\n])/g, "$1"), from: offset + m.index, to: offset + m.index + m[0].length };
        // From the line, not the masked copy: a title may hold a code span.
        const at = m.indices?.[3] ?? m.indices?.[4] ?? m.indices?.[5];
        if (at) ref.title = line.slice(at[0], at[1]);
        out.push(ref);
      }
    }
    offset += line.length + 1;
  }
  return out;
}

/** The line with each closed code span's content blanked, so nothing in it matches. */
function maskCodeSpans(line: string): string {
  let out = "";
  let i = 0;
  while (i < line.length) {
    if (line[i] !== "`") {
      out += line[i++];
      continue;
    }
    let n = 0;
    while (line[i + n] === "`") n++;
    const ticks = "`".repeat(n);
    let close = line.indexOf(ticks, i + n);
    // A longer run of backticks does not close it.
    while (close !== -1 && line[close + n] === "`") {
      let end = close;
      while (line[end] === "`") end++;
      close = line.indexOf(ticks, end);
    }
    if (close === -1) {
      out += ticks;
      i += n;
      continue;
    }
    out += " ".repeat(close + n - i);
    i = close + n;
  }
  return out;
}

/** The distinct ids a task's goal and criteria reference: the goal's, then the criteria's, each in order of first appearance. */
export function taskAttachmentIds(goal: string, criteria: ReadonlyArray<string>): string[] {
  const ids: string[] = [];
  for (const text of [goal, ...criteria]) {
    for (const r of attachmentReferences(text)) if (!ids.includes(r.id)) ids.push(r.id);
  }
  return ids;
}

/** A reference to put in Markdown: the name with its brackets made parentheses, so it cannot close the alt early. */
export function attachmentMarkdown(name: string, id: string): string {
  return `![${name.replace(/\[/g, "(").replace(/\]/g, ")").replace(/\n/g, " ")}](${ATTACHMENT_SCHEME}${id})`;
}

/** An attachment as the API returns it and events carry it. */
export interface AttachmentInfo {
  id: string;
  name: string;
  /** What the agent got. */
  contentType: AttachmentType;
  width: number;
  height: number;
  bytes: number;
  /** The image as picked. */
  original: { contentType: AttachmentType; width: number; height: number; bytes: number };
}
