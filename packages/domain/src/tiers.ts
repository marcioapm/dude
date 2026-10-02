import { z } from "zod";
import { TEST_HARNESS_MODELS, type AgentModels } from "./hierarchy.ts";

/**
 * Model tiers: what an agent runs on. A tier is the organization's,
 * changed by its admins, and names the one model dude requests from the
 * LLM proxy for it, exactly as the proxy names it. Every agent role names a
 * tier in its settings (org default, project override, field by field like
 * its effort); no role names a model. What the proxy then serves — that
 * model, or a fallback from its own config — dude does not see.
 *
 * The database holds the same bounds (migration 066).
 */

export const TIER_NAME_MAX = 24;
export const TIER_DESCRIPTION_MAX = 80;
export const TIER_MODEL_MAX = 200;

export const TIER_MODEL_MESSAGE = "The model as the proxy names it: no spaces or slashes, at most 200 characters";

/** A model name a tier can request: the proxy's name, or one of the scripted agent's test models. */
export function isTierModel(value: string): boolean {
  if ((TEST_HARNESS_MODELS as readonly string[]).includes(value)) return true;
  return value.length > 0 && value.length <= TIER_MODEL_MAX && !/[\s/]/u.test(value);
}

export const tierModelSchema = z.string().refine(isTierModel, TIER_MODEL_MESSAGE);

/** A tier as an admin writes it (`POST /v1/models/tiers`, `PUT /v1/models/tiers/:id`). */
export const modelTierInputSchema = z
  .object({
    name: z.string().trim().min(1, "A tier needs a name").max(TIER_NAME_MAX, `At most ${TIER_NAME_MAX} characters`),
    description: z.string().trim().max(TIER_DESCRIPTION_MAX, `At most ${TIER_DESCRIPTION_MAX} characters`).default(""),
    /** null: not set yet; a Run on the tier fails until it is. */
    model: tierModelSchema.nullable().default(null),
  })
  .strict();
export type ModelTierInput = z.infer<typeof modelTierInputSchema>;

/** A tier as the API shows it. */
export interface ModelTier {
  id: string;
  name: string;
  description: string;
  model: string | null;
  position: number;
  updatedAt: string;
  updatedBy: { id: string; name: string } | null;
}

/** Who names a tier: an organization's role or a project's override. */
export interface ModelTierUse {
  kind: "organization" | "project";
  role: string;
  project: { id: string; name: string; imageUrl: string | null } | null;
  /** The fixer with no tier of its own, taking the implementer's. */
  inherited?: boolean;
  /** The role's reasoning effort, resolved; null leaves it to the model. */
  effort: string | null;
}

export interface ModelTierWithUse extends ModelTier {
  usedBy: ModelTierUse[];
}

/** One line of what the upgrade to tiers changed, for the organization's admins. */
export interface ModelTierUpgradeNote {
  id: number;
  role: string;
  project: { id: string; name: string; imageUrl: string | null } | null;
  /** What the role named before: llm-anthropic/claude-opus-5-5. */
  oldModel: string;
  tierId: string | null;
  tierName: string;
  /** The upgrade made the tier for this model. */
  newTier: boolean;
  /** What it requests now is not what it named before. */
  modelChanged: boolean;
}

export interface ModelTiersResponse {
  tiers: ModelTierWithUse[];
  canEdit: boolean;
  /** Undismissed upgrade notes; only an admin is given them. */
  upgrade: ModelTierUpgradeNote[];
}

/** `DELETE /v1/models/tiers/:id`: the tier what named it moves to; required when it is in use. */
export const removeModelTierSchema = z.object({ replacement: z.string().min(1).nullable().default(null) }).strict();

/** `PUT /v1/models/tiers/order`: every tier's id, in the order shown. */
export const reorderModelTiersSchema = z.object({ ids: z.array(z.string().min(1)).min(1) }).strict();

/**
 * `POST /v1/models/test`: a model to try, for the tier being edited (null:
 * a new one). It is tried at each effort the tier's agents use, or once
 * without an effort when none do.
 */
export const testModelSchema = z
  .object({
    model: tierModelSchema,
    tierId: z.string().min(1).nullable().default(null),
  })
  .strict();

/** One try of a model at one effort, as the orchestrator reports it. */
export interface ModelTestResult {
  /** null: sent without an effort. */
  effort: string | null;
  ok: boolean;
  latencyMs: number;
  /** The proxy's HTTP status; null when no answer came (timeout, unreachable). */
  status: number | null;
  /** The proxy's error message, verbatim; null on success. */
  error: string | null;
}

/** `GET /v1/models/proxy`: the model ids the proxy lists, as suggestions. */
export interface ProxyModels {
  models: string[];
  /** The proxy's address (no key), for "From …/v1/models". */
  source: string | null;
  /** Why there are none: the proxy could not be read. */
  problem: string | null;
}

/** The roles that start on Thinker; the implementer starts on Coder, the fixer following it. */
export const THINKER_ROLES = ["investigator", "reviewer", "simplifier", "qa_browser", "orchestrator"] as const;

// ---------------------------------------------------------------------------
// Resolving and moving
// ---------------------------------------------------------------------------

/**
 * The tier a role runs on: the project's, then the organization's — for
 * the fixer, then the implementer's over the same layers. A stored id that
 * names no tier is skipped. null: no layer names one of the tiers. The
 * orchestrator's delivery.ResolveRole is the same rule, for the Run.
 */
export function resolveTier(
  role: string,
  layers: { project?: AgentModels | null | undefined; organization: AgentModels | null | undefined },
  tiers: ReadonlyArray<Pick<ModelTier, "id">>,
): { tierId: string | null; from: "project" | "organization" | "implementer" | null } {
  const chain = role === "fixer" ? ["fixer", "implementer"] : [role];
  const ordered = [["project", layers.project], ["organization", layers.organization]] as const;
  for (const r of chain) {
    for (const [name, layer] of ordered) {
      const id = (layer as Record<string, { tier?: string }> | null | undefined)?.[r]?.tier;
      if (id && tiers.some((t) => t.id === id)) return { tierId: id, from: r === role ? name : "implementer" };
    }
  }
  return { tierId: null, from: null };
}

/**
 * A role's reasoning effort over the same layers, field by field (the
 * fixer then the implementer's), as the orchestrator's ResolveRole has it.
 */
export function resolveEffort(
  role: string,
  layers: { project?: AgentModels | null | undefined; organization: AgentModels | null | undefined },
): string | null {
  const chain = role === "fixer" ? ["fixer", "implementer"] : [role];
  for (const r of chain) {
    for (const layer of [layers.project, layers.organization]) {
      const effort = (layer as Record<string, { effort?: string }> | null | undefined)?.[r]?.effort;
      if (effort) return effort;
    }
  }
  return null;
}

/**
 * A layer's agent models with every role's `tier` naming `from` moved to
 * `to`. Returns the same object when nothing named it.
 */
export function replaceTier(models: AgentModels, from: string, to: string): AgentModels {
  let changed = false;
  const out: Record<string, Record<string, unknown>> = {};
  for (const [role, config] of Object.entries(models as Record<string, Record<string, unknown>>)) {
    if (config?.["tier"] === from) {
      changed = true;
      out[role] = { ...config, tier: to };
    } else {
      out[role] = config;
    }
  }
  return changed ? (out as AgentModels) : models;
}
