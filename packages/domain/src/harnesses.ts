/**
 * Harnesses: the coding agent a role's Runs run on, separate from the
 * model tier it asks for. OpenCode speaks both the proxy's APIs; Claude
 * Code only Anthropic's, Codex only OpenAI's. Whether a role's harness can
 * run its tier's model is checked when a Run is built (the orchestrator's
 * delivery.HarnessFits, the same rule), and warned of when it is set — but
 * a setting that does not fit is still saved.
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

export const HARNESS_DESCRIPTION: Record<Harness, string> = {
  opencode: "Runs Anthropic and OpenAI models",
  "claude-code": "Runs Anthropic models (claude-…)",
  codex: "Runs OpenAI models",
};

/** The API a model is requested through, by its name: the orchestrator's llm.Provider. */
export function modelProvider(model: string): "anthropic" | "openai" {
  return model.startsWith("claude-") ? "anthropic" : "openai";
}

/**
 * Why `harness` cannot run `model`, in a sentence; null when it can, or
 * when there is no model to check. The scripted agent stands in for any.
 */
export function harnessMisfit(harness: Harness, model: string | null | undefined): string | null {
  if (!model || model.startsWith("fake/")) return null;
  const provider = modelProvider(model);
  if (harness === "claude-code" && provider !== "anthropic") {
    return `Claude Code runs only Anthropic models (claude-…); ${model} is not one. Its Runs will fail until the harness or tier changes.`;
  }
  if (harness === "codex" && provider !== "openai") {
    return `Codex runs only OpenAI models; ${model} is Anthropic's. Its Runs will fail until the harness or tier changes.`;
  }
  return null;
}

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
