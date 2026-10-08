/**
 * What the model screens compute from tiers: each tier's mark, the words
 * for who uses one, what it requests, whether a name is one the proxy
 * lists, a tier dialog's draft, a test message's result as a line, and the
 * upgrade's notes. Pure, so the rules are tested apart from the screens.
 */

import {
  modelTierInputSchema,
  type ModelTestResult,
  type ModelTier,
  type ModelTierInput,
  type ModelTierUpgradeNote,
  type ModelTierUse,
  type TierEffort,
} from "@dude/domain";
import type { IconName } from "@dude/design-system";
import type { TierTone } from "@dude/design-system/components";
import { roleLabel } from "./machines.ts";

/** A tier's mark: the seeded tiers have their own; any other the sparkle. */
export function tierMark(tier: Pick<ModelTier, "name">): { icon: IconName; tone: TierTone } {
  switch (tier.name.toLowerCase()) {
    case "thinker":
      return { icon: "brain", tone: "info" };
    case "coder":
      return { icon: "agent", tone: "success" };
    case "fast":
      return { icon: "zap", tone: "attention" };
  }
  return { icon: "sparkle", tone: "neutral" };
}

/** Who uses a tier, in words: "4 agents", "2 agents · 1 project", "Nobody". */
export function tierUsedByWords(uses: readonly ModelTierUse[]): string {
  const agents = new Set(uses.filter((u) => u.kind === "organization").map((u) => u.role)).size;
  const projects = new Set(uses.filter((u) => u.kind === "project").map((u) => u.project?.id)).size;
  const parts = [
    agents ? `${agents} ${agents === 1 ? "agent" : "agents"}` : null,
    projects ? `${projects} ${projects === 1 ? "project" : "projects"}` : null,
  ].filter(Boolean);
  return parts.length ? parts.join(" · ") : "Nobody";
}

/** A role, on a project when it is a project's: "Implementer", "abs · Reviewer". */
function roleOn(role: string, project: { name: string } | null): string {
  return project ? `${project.name} · ${roleLabel(role)}` : roleLabel(role);
}

/** One use, as the remove dialog and the edit callout name it: "Implementer", "abs · Reviewer". */
export function tierUseName(use: ModelTierUse): string {
  return roleOn(use.role, use.kind === "project" ? use.project! : null);
}

/** Where a use is set, as the remove dialog says it. */
export function tierUseWhere(use: ModelTierUse, orgName: string): string {
  if (use.inherited) return "follows the implementer";
  return use.kind === "project" ? "project override" : `${orgName}’s setting`;
}

/** Whether the proxy lists a name; unknown (null) while its list could not be read. */
export function proxyKnows(name: string, listed: readonly string[] | null): boolean | null {
  if (listed === null) return null;
  return listed.includes(name.trim());
}

/** A tier's effort as people read it: "High", "Model’s default". */
export function tierEffortLabel(effort: TierEffort | null): string {
  return effort ? effort[0]!.toUpperCase() + effort.slice(1) : "Model’s default";
}

/** A tier dialog's fields as typed: options and headers as the JSON text in their fields. */
export interface TierDraft {
  name: string;
  description: string;
  model: string;
  effort: TierEffort | null;
  options: string;
  headers: string;
}

const jsonText = (v: unknown) => (v === null || v === undefined ? "" : JSON.stringify(v, null, 2));

export function tierDraftOf(tier: ModelTier | null): TierDraft {
  return tier
    ? { name: tier.name, description: tier.description, model: tier.model ?? "", effort: tier.effort ?? null,
        options: jsonText(tier.options), headers: jsonText(tier.headers) }
    : { name: "", description: "", model: "", effort: null, options: "", headers: "" };
}

/** A JSON field's text as a value: empty is null; anything that is not a JSON object is `invalid`. */
function parseObject(text: string): { value: Record<string, unknown> | null } | { invalid: true } {
  if (!text.trim()) return { value: null };
  try {
    const v: unknown = JSON.parse(text);
    return typeof v === "object" && v !== null && !Array.isArray(v) ? { value: v as Record<string, unknown> } : { invalid: true };
  } catch {
    return { invalid: true };
  }
}

const NOT_AN_OBJECT = "A JSON object, like {\"key\": \"value\"}";

/** The draft as the API takes it: an empty model is "not set", an empty JSON field none. Unparsable JSON is sent as it would be refused. */
export function tierInput(d: TierDraft): ModelTierInput {
  const model = d.model.trim();
  const options = parseObject(d.options);
  const headers = parseObject(d.headers);
  return {
    name: d.name.trim(), description: d.description.trim(), model: model === "" ? null : model, effort: d.effort,
    options: "invalid" in options ? null : options.value,
    headers: "invalid" in headers ? null : (headers.value as Record<string, string> | null),
  };
}

/** Each field's problem, in the schema's words; none when the draft is a tier. */
export function tierDraftProblems(d: TierDraft): Partial<Record<keyof TierDraft, string>> {
  const out: Partial<Record<keyof TierDraft, string>> = {};
  if ("invalid" in parseObject(d.options)) out.options = NOT_AN_OBJECT;
  if ("invalid" in parseObject(d.headers)) out.headers = NOT_AN_OBJECT;
  const parsed = modelTierInputSchema.safeParse(tierInput(d));
  if (parsed.success) return out;
  for (const issue of parsed.error.issues) out[issue.path[0] as keyof TierDraft] ??= issue.message;
  return out;
}

/**
 * A test message's result, as a line: "answered in 1.2 s", or the proxy's
 * own words, with the reasoning settings that went on the wire after it.
 */
export function testResultWords(r: ModelTestResult): string {
  const sent = Object.keys(r.sent).length ? ` (sent ${JSON.stringify(r.sent)})` : "";
  if (r.ok) return `answered in ${(r.latencyMs / 1000).toFixed(1)} s${sent}`;
  if (r.status === null) return `${r.error ?? "no answer"}${sent}`;
  return `the proxy answered ${r.status}${r.error ? ` — ${r.error}` : ""}${sent}`;
}

/** One line of the upgrade banner: the roles it covers, and a project's override named as `who`. */
export interface UpgradeLine {
  key: string;
  roles: string[];
  oldModel: string;
  project: ModelTierUpgradeNote["project"];
  who: string | null;
  tierName: string;
  newTier: boolean;
  modelChanged: boolean;
}

/**
 * The upgrade's notes as the banner lists them: the organisation's roles
 * grouped by the model they named and the tier they ask for, then each
 * project's override on its own line.
 */
export function upgradeLines(notes: readonly ModelTierUpgradeNote[]): UpgradeLine[] {
  const out: UpgradeLine[] = [];
  for (const n of notes) {
    if (!n.project) {
      const same = out.find((l) => !l.project && l.oldModel === n.oldModel && l.tierName === n.tierName);
      if (same) {
        same.roles.push(n.role);
        continue;
      }
    }
    out.push({ key: `${n.id}`, roles: [n.role], oldModel: n.oldModel, project: n.project, who: n.project ? roleOn(n.role, n.project) : null,
      tierName: n.tierName, newTier: n.newTier, modelChanged: n.modelChanged });
  }
  return out;
}
