/**
 * Harnesses: the coding agent a role's Runs run on, separate from the
 * model tier it asks for. Any harness takes any tier's model: each speaks
 * its own API to the LLM proxy, which translates to the model's.
 */

import { z } from "zod";
import type { AgentModels } from "./hierarchy.ts";

export const HARNESSES = ["opencode", "claude-code", "codex"] as const;
export const harnessSchema = z.enum(HARNESSES);
export type Harness = z.infer<typeof harnessSchema>;

/** What a role runs on when no layer names a harness. */
export const DEFAULT_HARNESS: Harness = "opencode";

export const HARNESS_LABEL: Record<Harness, string> = {
  opencode: "OpenCode",
  "claude-code": "Claude Code",
  codex: "Codex",
};

/**
 * The harness a role runs on: the project's, then the organization's —
 * for the fixer, then the implementer's over the same layers — else
 * OpenCode. A stored value that names no harness is skipped. The
 * orchestrator's delivery.ResolveRole is the same rule, for the Run.
 */
export function resolveHarness(
  role: string,
  layers: { project?: AgentModels | null | undefined; organization: AgentModels | null | undefined },
): { harness: Harness; from: "project" | "organization" | "implementer" | "default" } {
  const chain = role === "fixer" ? ["fixer", "implementer"] : [role];
  const ordered = [["project", layers.project], ["organization", layers.organization]] as const;
  for (const r of chain) {
    for (const [name, layer] of ordered) {
      const h = (layer as Record<string, { harness?: string } | undefined> | null | undefined)?.[r]?.harness;
      if (h && (HARNESSES as readonly string[]).includes(h)) return { harness: h as Harness, from: r === role ? name : "implementer" };
    }
  }
  return { harness: DEFAULT_HARNESS, from: "default" };
}
