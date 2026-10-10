/**
 * A brainstorm session's model tier and harness, as the web reads them:
 * the organisation's tiers and its Brainstorm setting, loaded once, for the
 * welcome's picker and the owner's in a session's rail.
 */

import { useEffect, useState } from "react";
import type { PickerTier } from "@dude/design-system/components";
import { harnessSchema, HARNESS_LABEL, type Harness, type ModelTier, type SessionModel } from "@dude/domain";
import type { ApiClient } from "./api/client.ts";

export interface ModelOptions {
  readonly tiers: readonly PickerTier[];
  readonly organization: { readonly tier: PickerTier | null; readonly harness: Harness };
}

export const pickerTier = (t: Pick<ModelTier, "id" | "name" | "model">): PickerTier => ({ id: t.id, name: t.name, model: t.model });

/**
 * The organisation's tiers, and with `withOrganization` its Brainstorm
 * setting, read once per mount; null until both are in, or when either
 * cannot be read (the picker is then left out and the session follows the
 * organisation).
 */
export function useModelOptions(client: ApiClient, enabled: boolean, withOrganization: boolean): ModelOptions | null {
  const [options, setOptions] = useState<ModelOptions | null>(null);
  useEffect(() => {
    if (!enabled) return;
    let current = true;
    void Promise.all([client.modelTiers(), withOrganization ? client.organizationSettings() : Promise.resolve(null)]).then(([tiers, settings]) => {
      if (!current) return;
      const all = tiers.tiers.map(pickerTier);
      const role = settings?.roles.brainstorm;
      const harness = harnessSchema.safeParse(role?.harness.value);
      setOptions({
        tiers: all,
        organization: { tier: all.find((t) => t.id === role?.tier.value) ?? null, harness: harness.success ? harness.data : "opencode" },
      });
    }, () => undefined);
    return () => {
      current = false;
    };
  }, [client, enabled, withOrganization]);
  return options;
}

/** The session's own model as the organisation's setting sees it, for the rail's picker. */
export function organizationOf(model: SessionModel): ModelOptions["organization"] {
  return { tier: model.organization.tier ? pickerTier(model.organization.tier) : null, harness: model.organization.harness };
}

/** "Opus (High) · Claude Code": a tier and harness as the Chat's notice names them; null halves are the organisation's. */
export function modelChangeWords(tier: { name: string } | null | undefined, harness: Harness | null | undefined): string {
  const t = tier?.name ?? "the organisation's tier";
  const h = harness ? HARNESS_LABEL[harness] : "the organisation's harness";
  return tier || harness ? `${t} · ${h}` : "the organisation's default";
}
