import { z } from "zod";
import { TEST_HARNESS_MODELS, type AgentModels } from "./hierarchy.ts";

/**
 * Model tiers: what an agent runs on. A tier is the organization's,
 * changed by its admins, and names the one model dude requests from the
 * LLM proxy for it, exactly as the proxy names it, how hard it thinks, and
 * any extra settings its agent is requested with. Every agent role names a
 * tier in its settings (org default, project override, field by field like
 * its machine); no role names a model or an effort. What the proxy then
 * serves — that model, or a fallback from its own config — dude does not see.
 *
 * The database holds the same bounds (migrations 069 and 099).
 */

export const TIER_NAME_MAX = 24;
export const TIER_DESCRIPTION_MAX = 80;
export const TIER_MODEL_MAX = 200;
/** The most a tier's options, or its headers, may take as JSON, in bytes. */
export const TIER_JSON_MAX = 4096;

export const TIER_MODEL_MESSAGE = "The model as the proxy names it: no spaces or slashes, at most 200 characters";

/** How hard a tier's model thinks; null is the model's default. none turns Claude's thinking off; GPT reasons at its default. */
export const TIER_EFFORTS = ["none", "low", "medium", "high", "max"] as const;
export const tierEffortSchema = z.enum(TIER_EFFORTS);
export type TierEffort = z.infer<typeof tierEffortSchema>;

/** A model name a tier can request: the proxy's name, or one of the scripted agent's test models. */
export function isTierModel(value: string): boolean {
  if ((TEST_HARNESS_MODELS as readonly string[]).includes(value)) return true;
  return value.length > 0 && value.length <= TIER_MODEL_MAX && !/[\s/]/u.test(value);
}

export const tierModelSchema = z.string().refine(isTierModel, TIER_MODEL_MESSAGE);

/**
 * A JSON value's size as Postgres renders jsonb as text (", " and ": "
 * between items), which is what the table's CHECK measures.
 */
function jsonbText(v: unknown): string {
  if (Array.isArray(v)) return `[${v.map(jsonbText).join(", ")}]`;
  if (typeof v === "object" && v !== null) {
    return `{${Object.entries(v).map(([k, x]) => `${JSON.stringify(k)}: ${jsonbText(x)}`).join(", ")}}`;
  }
  return JSON.stringify(v) ?? "null";
}
const jsonBytes = (v: unknown) => new TextEncoder().encode(jsonbText(v)).length;
/** jsonb cannot store U+0000 in any string, key or value (Postgres 22P05). */
function noNul(v: unknown): boolean {
  if (typeof v === "string") return !v.includes("\u0000");
  if (Array.isArray(v)) return v.every(noNul);
  if (typeof v === "object" && v !== null) return Object.entries(v).every(([k, x]) => noNul(k) && noNul(x));
  return true;
}
/** An HTTP header name (RFC 9110 token). */
export const HEADER_NAME = /^[!#$%&'*+.^_`|~0-9A-Za-z-]+$/;

/**
 * The byte count is the client's estimate: Postgres prints jsonb numbers
 * in full (1e21 is 22 digits), so the API also maps the CHECK's refusal.
 */
export const TIER_JSON_TOO_BIG = `At most ${TIER_JSON_MAX} bytes as JSON`;
export const TIER_NUL_MESSAGE = "No NUL characters (\\u0000)";
const HEADER_NAME_MESSAGE = "Header names are letters, digits and !#$%&'*+.^_`|~-";
const HEADER_VALUE_MESSAGE = "A header's value is one line";
// DEL is not allowed in an HTTP field value (RFC 9110), nor raw in Codex's TOML config.
const HEADER_DEL_MESSAGE = "A header's value holds no DEL character";
/** What the options and headers fields are refused with, said back as they are. */
export const TIER_JSON_MESSAGES = [TIER_JSON_TOO_BIG, TIER_NUL_MESSAGE, HEADER_NAME_MESSAGE, HEADER_VALUE_MESSAGE, HEADER_DEL_MESSAGE] as const;

/** Extra OpenCode model options: a JSON object, at most TIER_JSON_MAX bytes. */
export const tierOptionsSchema = z
  .record(z.unknown())
  .refine(noNul, TIER_NUL_MESSAGE)
  .refine((v) => jsonBytes(v) <= TIER_JSON_MAX, TIER_JSON_TOO_BIG);

/** Extra request headers: header names to one-line string values, at most TIER_JSON_MAX bytes. */
export const tierHeadersSchema = z
  .record(z.string())
  .refine(noNul, TIER_NUL_MESSAGE)
  .refine((h) => Object.keys(h).every((k) => HEADER_NAME.test(k)), HEADER_NAME_MESSAGE)
  .refine((h) => Object.values(h).every((v) => !/[\r\n]/u.test(v)), HEADER_VALUE_MESSAGE)
  .refine((h) => Object.values(h).every((v) => !v.includes("\u007f")), HEADER_DEL_MESSAGE)
  .refine((h) => jsonBytes(h) <= TIER_JSON_MAX, TIER_JSON_TOO_BIG);

/** A tier as an admin writes it (`POST /v1/models/tiers`, `PUT /v1/models/tiers/:id`). */
export const modelTierInputSchema = z
  .object({
    name: z.string().trim().min(1, "A tier needs a name").max(TIER_NAME_MAX, `At most ${TIER_NAME_MAX} characters`),
    description: z.string().trim().max(TIER_DESCRIPTION_MAX, `At most ${TIER_DESCRIPTION_MAX} characters`).default(""),
    /** null: not set yet; a Run on the tier fails until it is. */
    model: tierModelSchema.nullable().default(null),
    /** null: the model's default. */
    effort: tierEffortSchema.nullable().default(null),
    /** Merged over what the effort makes; the tier's keys win. */
    options: tierOptionsSchema.nullable().default(null),
    headers: tierHeadersSchema.nullable().default(null),
  })
  .strict();
export type ModelTierInput = z.infer<typeof modelTierInputSchema>;

/** A tier as the API shows it. */
export interface ModelTier {
  id: string;
  name: string;
  description: string;
  model: string | null;
  effort: TierEffort | null;
  options: Record<string, unknown> | null;
  headers: Record<string, string> | null;
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
 * `POST /v1/models/test`: a tier's model and settings as the dialog has
 * them (saved or not), sent once as its agent would send them.
 */
export const testModelSchema = z
  .object({
    model: tierModelSchema,
    effort: tierEffortSchema.nullable().default(null),
    options: tierOptionsSchema.nullable().default(null),
    headers: tierHeadersSchema.nullable().default(null),
  })
  .strict();
export type ModelTestInput = z.input<typeof testModelSchema>;

/** The test message's request to the proxy, as the orchestrator reports it. */
export interface ModelTestResult {
  /**
   * The tier's reasoning settings as they went on the wire: Anthropic's
   * `thinking` and `output_config`, or the Responses API's `reasoning`.
   */
  sent: Record<string, unknown>;
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

// ---------------------------------------------------------------------------
// Resolving and moving
// ---------------------------------------------------------------------------

/** A role's settings layers, the project's (if any) over the organization's. */
type RoleLayers = { project?: AgentModels | null | undefined; organization: AgentModels | null | undefined };

/** The roles whose settings a role takes, in order: the fixer falls back to the implementer's. */
function roleChain(role: string): string[] {
  return role === "fixer" ? ["fixer", "implementer"] : [role];
}

function roleTier(layer: AgentModels | null | undefined, role: string): string | undefined {
  return (layer as Record<string, { tier?: string } | undefined> | null | undefined)?.[role]?.tier;
}

/**
 * The tier a role runs on: the first layer that names one — the
 * project's, then the organization's; for the fixer, then the
 * implementer's over the same layers. null: none does, or the one named is
 * not among the tiers (never, while removals move what named them). The
 * orchestrator's delivery.ResolveRole is the same rule, for the Run.
 */
export function resolveTier(
  role: string,
  layers: RoleLayers,
  tiers: ReadonlyArray<Pick<ModelTier, "id">>,
): { tierId: string | null; from: "project" | "organization" | "implementer" | null } {
  const ordered = [["project", layers.project], ["organization", layers.organization]] as const;
  for (const r of roleChain(role)) {
    for (const [name, layer] of ordered) {
      const id = roleTier(layer, r);
      if (!id) continue;
      return { tierId: tiers.some((t) => t.id === id) ? id : null, from: r === role ? name : "implementer" };
    }
  }
  return { tierId: null, from: null };
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
