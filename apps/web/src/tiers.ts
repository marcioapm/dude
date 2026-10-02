/**
 * What the model screens compute from tiers: each tier's mark, the words
 * for who uses one and at which effort, whether a name is one the proxy
 * lists, a test message's result as a line, and the upgrade's notes. Pure,
 * so the rules are tested apart from the screens.
 */

import {
  modelTierInputSchema,
  type ModelTestResult,
  type ModelTier,
  type ModelTierInput,
  type ModelTierUpgradeNote,
  type ModelTierUse,
} from "@dude/domain";
import type { IconName } from "@dude/design-system";
import type { TierTone } from "@dude/design-system/components";
import { roleLabel } from "./machines.ts";
import { effortLabel } from "./settings.ts";

/** A tier's mark: the seeded tiers have their own; any other the sparkle, by its place. */
export function tierMark(tier: Pick<ModelTier, "name" | "position">): { icon: IconName; tone: TierTone } {
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

/** One use, as the remove dialog and the edit callout name it: "Implementer", "abs · Reviewer". */
export function tierUseName(use: ModelTierUse): string {
  return use.kind === "project" ? `${use.project!.name} · ${roleLabel(use.role)}` : roleLabel(use.role);
}

/** Where a use is set, as the remove dialog says it. */
export function tierUseWhere(use: ModelTierUse, orgName: string): string {
  if (use.inherited) return "follows the implementer";
  return use.kind === "project" ? "project override" : `${orgName}’s setting`;
}

/** The efforts a tier's agents use, distinct, in words: "high", "high and low", or null for none set. */
export function effortsWords(uses: readonly ModelTierUse[]): string | null {
  const efforts = [...new Set(uses.map((u) => u.effort ?? "the model’s default"))];
  if (!uses.length) return null;
  return efforts.map((e) => (e === "the model’s default" ? e : effortLabel(e).toLowerCase())).join(" and ");
}

/** Whether the proxy lists a name; unknown (null) while its list could not be read. */
export function proxyKnows(name: string, listed: readonly string[] | null): boolean | null {
  if (listed === null) return null;
  return listed.includes(name.trim());
}

/** A tier dialog's fields as typed. */
export interface TierDraft {
  name: string;
  description: string;
  model: string;
}

export function tierDraftOf(tier: ModelTier | null): TierDraft {
  return tier ? { name: tier.name, description: tier.description, model: tier.model ?? "" } : { name: "", description: "", model: "" };
}

/** The draft as the API takes it: an empty model is "not set". */
export function tierInput(d: TierDraft): ModelTierInput {
  const model = d.model.trim();
  return { name: d.name.trim(), description: d.description.trim(), model: model === "" ? null : model };
}

/** Each field's problem, in the schema's words; none when the draft is a tier. */
export function tierDraftProblems(d: TierDraft): Partial<Record<keyof TierDraft, string>> {
  const parsed = modelTierInputSchema.safeParse(tierInput(d));
  const out: Partial<Record<keyof TierDraft, string>> = {};
  if (parsed.success) return out;
  for (const issue of parsed.error.issues) out[issue.path[0] as keyof TierDraft] ??= issue.message;
  return out;
}

/** A test message's result, as a line: "answered at effort high in 1.2 s", or the proxy's own words. */
export function testResultWords(r: ModelTestResult): string {
  const at = r.effort ? `at effort ${r.effort}` : "with no effort";
  if (r.ok) return `answered ${at} in ${(r.latencyMs / 1000).toFixed(1)} s`;
  if (r.status === null) return `${at}: ${r.error ?? "no answer"}`;
  return `${at}: the proxy answered ${r.status}${r.error ? ` — ${r.error}` : ""}`;
}

/** An upgrade note's role, as its line names it: "Implementer", or "abs · Reviewer" for a project's. */
export function noteWho(note: ModelTierUpgradeNote): string {
  return note.project ? `${note.project.name} · ${roleLabel(note.role)}` : roleLabel(note.role);
}

/**
 * The upgrade's notes as the banner lists them: the organisation's roles
 * grouped by the model they named and the tier they ask for, then each
 * project's override on its own line.
 */
export function upgradeLines(notes: readonly ModelTierUpgradeNote[]): Array<{
  key: string;
  roles: string[];
  oldModel: string;
  project: ModelTierUpgradeNote["project"];
  who: string | null;
  tierName: string;
  newTier: boolean;
  modelChanged: boolean;
}> {
  const out: ReturnType<typeof upgradeLines> = [];
  for (const n of notes) {
    if (!n.project) {
      const same = out.find((l) => !l.project && l.oldModel === n.oldModel && l.tierName === n.tierName);
      if (same) {
        same.roles.push(n.role);
        continue;
      }
    }
    out.push({ key: `${n.id}`, roles: [n.role], oldModel: n.oldModel, project: n.project, who: n.project ? noteWho(n) : null,
      tierName: n.tierName, newTier: n.newTier, modelChanged: n.modelChanged });
  }
  return out;
}
