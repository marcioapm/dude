/**
 * A repository's setup script as its editor checks it: the server's rule
 * (setupScriptInput: UTF-8 bytes against SETUP_SCRIPT_MAX_BYTES, no NUL,
 * blank is none) and the lines that would take git's hooks away from
 * dude's own commit hook, which are warned about and never refused.
 */

import { SETUP_SCRIPT_MAX_BYTES, setupScriptInput } from "@dude/domain";
import type { CodeDiagnostic } from "@dude/design-system/components";

export { SETUP_SCRIPT_MAX_BYTES };

export const setupScriptBytes = (text: string) => new TextEncoder().encode(text).length;

export type SetupScriptCheck =
  | { ok: true; value: string | null }
  | { ok: false; problem: "too-long" | "nul" };

/** What Save would send: the script, or null for a blank one; or why the server would refuse it. */
export function checkSetupScript(text: string): SetupScriptCheck {
  const parsed = setupScriptInput.safeParse(text);
  if (parsed.success) return { ok: true, value: parsed.data };
  return { ok: false, problem: text.includes("\0") ? "nul" : "too-long" };
}

const BYPASSES: ReadonlyArray<{ pattern: RegExp; message: string }> = [
  {
    pattern: /core\.hooksPath/gi,
    message: "Moves git’s hooks away from dude’s own commit hook. Install the hook into $(git rev-parse --git-path hooks) instead.",
  },
  {
    pattern: /--no-verify\b/g,
    message: "Skips git’s hooks, dude’s own commit hook among them.",
  },
  {
    pattern: /\bHUSKY=0\b/g,
    message: "Turns the repository’s Husky hooks off for what follows; dude’s commit hook runs through the same hooks directory.",
  },
];

/**
 * Warnings for each use of core.hooksPath, --no-verify or HUSKY=0 outside a
 * whole-line comment, at the words themselves.
 */
export function lintSetupScript(text: string): CodeDiagnostic[] {
  const out: CodeDiagnostic[] = [];
  text.split("\n").forEach((line, i) => {
    if (line.trimStart().startsWith("#")) return;
    for (const { pattern, message } of BYPASSES) {
      for (const m of line.matchAll(pattern)) {
        out.push({ severity: "warning", line: i + 1, from: m.index, to: m.index + m[0].length, message });
      }
    }
  });
  return out;
}
