/**
 * Chat turns have no measure: a message, a thought, a question, a tool call
 * and a notice span the transcript's column (README, Measure; Agent
 * sessions, "A turn's text spans its column"). A fixed width cap on a turn
 * leaves it stopping short of the turns around it. This reads every
 * stylesheet that draws a turn and fails on a `max-width` / `max-inline-size`
 * in a fixed unit, except on an element inside a turn listed here with why.
 * Percentages, `100%` and caps inside `@container` / `@media` are layout, not
 * a measure, and are allowed.
 */

import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { declarations, ROOT } from "./stylesheets.ts";

const DIR = "packages/design-system/src/components";

/** The stylesheets of the transcript's turns. */
const TURNS = [
  "ChatMessage", "QuestionCard", "ToolCallCard", "ThinkingBlock", "ChatEvent", "ChatProgress", "ChatNotice", "ChatRunLine",
  "ChatThread", "Peer", "StartProgress", "TaskHistory", "DeciderLine", "ActivityIndicator", "AgentPlan", "Brainstorm", "turn",
];

/** `file` → selectors whose fixed cap is on an element inside a turn, and why. */
const ALLOWED: Record<string, Record<string, string>> = {
  StartProgress: { ".steps": "the stepper's columns, not the turn's text" },
  Brainstorm: { ".card": "the open proposal: a raised form inside the agent's turn, with its File footer" },
};

/** Removes `@container` and `@media` blocks: what they cap is the narrow layout. */
function outsideQueries(css: string): string {
  let out = css.replace(/\/\*[\s\S]*?\*\//g, "");
  for (;;) {
    const at = out.search(/@(container|media)\b/);
    if (at < 0) return out;
    const open = out.indexOf("{", at);
    let depth = 0;
    let end = open;
    for (; end < out.length; end++) {
      if (out[end] === "{") depth++;
      else if (out[end] === "}" && --depth === 0) break;
    }
    out = out.slice(0, at) + out.slice(end + 1);
  }
}

const FIXED = /\d(px|ch|rem|em|vw)\b|var\(--ds-measure-/;

describe("a chat turn spans its column", () => {
  for (const name of TURNS) {
    test(name, () => {
      const css = outsideQueries(readFileSync(`${ROOT}/${DIR}/${name}.module.css`, "utf8"));
      const capped = declarations(css, /^(max-width|max-inline-size)$/)
        .filter(([sel, , value]) => FIXED.test(value) && !ALLOWED[name]?.[sel])
        .map(([sel, prop, value]) => `${sel} { ${prop}: ${value} }`);
      expect(capped).toEqual([]);
    });
  }

  test("the guard sees a cap", () => {
    expect(declarations(outsideQueries(".main { max-width: 720px; } @container (max-width: 420px) { .a { max-width: 10px } }"), /^max-width$/))
      .toEqual([[".main", "max-width", "720px"]]);
  });
});
