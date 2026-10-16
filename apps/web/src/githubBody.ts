export type GitHubSection =
  | { kind: "markdown"; source: string }
  | { kind: "details"; summary: string; children: GitHubSection[] };

const ENTITIES: Record<string, string> = { "&amp;": "&", "&lt;": "<", "&gt;": ">", "&quot;": '"', "&#39;": "'", "&nbsp;": " " };
const decode = (text: string) => text.replace(/&(?:amp|lt|gt|quot|#39|nbsp);/g, (entity) => ENTITIES[entity]!);
const TAG = /^<\/?([a-z][\w:-]*)\b(?:[^>"']|"[^"]*"|'[^']*')*>/i;

/** GitHub's HTML becomes text and disclosures, never HTML to inject. */
export function githubBody(source: string): GitHubSection[] {
  // Protect code before reading tags or comments, including blank lines in fences.
  let marker = "\uE000";
  while (source.includes(marker)) marker += "\uE000";
  const code: string[] = [];
  const keep = (text: string) => `${marker}${code.push(text) - 1}${marker}`;
  let protectedSource = "";
  for (let i = 0; i < source.length;) {
    const rest = source.slice(i);
    // Fences may sit in a list or a quote, so their indent and `>` prefix count as the line start.
    const fence = (i === 0 || source[i - 1] === "\n") ? /^([ \t>]*)(`{3,}|~{3,})[^\n]*(?:\n|$)/.exec(rest) : null;
    if (rest.startsWith("<!--")) {
      const end = rest.indexOf("-->");
      const length = end < 0 ? rest.length : end + 3;
      protectedSource += rest.slice(0, length);
      i += length;
    } else if (fence) {
      const delimiter = fence[2]!;
      const close = new RegExp(`^[ \\t>]*${delimiter[0]}{${delimiter.length},}[ \\t]*$`, "m");
      const tail = rest.slice(fence[0].length);
      const end = close.exec(tail);
      const length = end ? fence[0].length + end.index + end[0].length : rest.length;
      protectedSource += keep(rest.slice(0, length));
      i += length;
    } else if (source[i] === "\\" && i + 1 < source.length) {
      protectedSource += keep(source.slice(i, i + 2));
      i += 2;
    } else if (source[i] === "`") {
      const run = /^`+/.exec(rest)![0];
      const closer = new RegExp(`(?<!\x60)${run}(?!\x60)`).exec(rest.slice(run.length));
      const length = closer ? run.length + closer.index + run.length : run.length;
      protectedSource += keep(rest.slice(0, length));
      i += length;
    } else {
      protectedSource += source[i];
      i++;
    }
  }
  const restore = (text: string) => text.replace(new RegExp(`${marker}(\\d+)${marker}`, "g"), (_, n: string) => code[Number(n)]!);
  let at = 0;
  const input = protectedSource;
  function read(stop = "", plain = false, onSummary?: (label: string) => void): GitHubSection[] {
    const sections: GitHubSection[] = [];
    let text = "";
    const flush = () => {
      const cleaned = restore(text.replace(/\n[ \t]*\n(?:[ \t]*\n)+/g, "\n\n").trim());
      if (cleaned) sections.push({ kind: "markdown", source: cleaned });
      text = "";
    };
    while (at < input.length) {
      if (input.startsWith("<!--", at)) {
        const end = input.indexOf("-->", at + 4);
        const comment = input.slice(at + 4, end < 0 ? input.length : end).trim();
        at = end < 0 ? input.length : end + 3;
        if (comment === "autofix_checkbox_start") {
          const finish = /<!--[ \t]*autofix_checkbox_end[ \t]*-->/.exec(input.slice(at));
          if (finish) at += finish.index + finish[0].length;
        }
        continue;
      }
      const tag = TAG.exec(input.slice(at));
      if (tag) {
        at += tag[0].length;
        const name = tag[1]!.toLowerCase();
        const closing = tag[0].startsWith("</");
        if (closing && name === stop) break;
        if (name === "picture" && !closing) { read("picture", true); continue; }
        if (name === "img" || name === "source" || name === "picture") continue;
        if (name === "details" && !closing && !plain) {
          flush();
          let summary = "Details";
          const children = read("details", false, (label) => { summary = label || "Details"; });
          sections.push({ kind: "details", summary, children });
        } else if (name === "summary" && !closing) {
          const label = read("summary", true).map((s) => s.kind === "markdown" ? s.source : "").join("");
          if (onSummary) onSummary(label);
          else text += label;
        } else if (name === "br") text += "\n";
        else if (!plain && (name === "strong" || name === "b")) text += "**";
        else if (!plain && (name === "em" || name === "i")) text += "*";
        else if (!plain && name === "code" && !closing) {
          const content = read("code", true).map((s) => s.kind === "markdown" ? s.source : "").join("");
          const ticks = "`".repeat(Math.max(0, ...Array.from(content.matchAll(/`+/g), (m) => m[0].length)) + 1);
          text += `${ticks} ${content} ${ticks}`;
        } else if (!plain && name === "a" && !closing) {
          const href = /\bhref\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))/i.exec(tag[0]);
          const url = decode(href?.[1] ?? href?.[2] ?? href?.[3] ?? "");
          const label = read("a", true).map((s) => s.kind === "markdown" ? s.source : "").join("");
          text += label && /^https?:\/\//i.test(url) ? `[${label.replace(/[\[\]\\]/g, "\\$&")}](${url.replace(/[\s()<>]/g, (c) => encodeURIComponent(c).replace(/\(/g, "%28").replace(/\)/g, "%29"))})` : label;
        }
        continue;
      }
      const next = input.indexOf("<", at + 1);
      text += decode(input.slice(at, next < 0 ? input.length : next));
      at = next < 0 ? input.length : next;
    }
    flush();
    return sections;
  }
  return read();
}
