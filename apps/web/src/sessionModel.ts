/**
 * A brainstorm session's model tier and harness, as the web reads them:
 * the organisation's tiers and its Brainstorm setting, for the welcome's
 * picker and the owner's in a session's rail.
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

// Read once per client and kept until the App hears a settings change
// (a tier added, edited or removed is one too): the welcome remounts on
// every New session, and the rail opens the same list.
const tiersRead = new WeakMap<ApiClient, Promise<readonly PickerTier[]>>();
const organizationRead = new WeakMap<ApiClient, Promise<{ tierId: string | null; harness: Harness }>>();

function kept<T>(cache: WeakMap<ApiClient, Promise<T>>, client: ApiClient, read: () => Promise<T>): Promise<T> {
  let found = cache.get(client);
  if (!found) {
    found = read();
    // A failed read is tried again by the next reader, not kept.
    found.catch(() => cache.delete(client));
    cache.set(client, found);
  }
  return found;
}

const readTiers = (client: ApiClient) => kept(tiersRead, client, async () => (await client.modelTiers()).tiers.map(pickerTier));
const readOrganization = (client: ApiClient) => kept(organizationRead, client, async () => {
  const role = (await client.organizationSettings()).roles.brainstorm;
  const harness = harnessSchema.safeParse(role?.harness.value);
  return { tierId: typeof role?.tier.value === "string" ? role.tier.value : null, harness: harness.success ? harness.data : "opencode" };
});

/** Drop what was read: the organisation's tiers or settings changed. */
export function forgetModelOptions(client: ApiClient): void {
  tiersRead.delete(client);
  organizationRead.delete(client);
}

/**
 * The organisation's tiers and its Brainstorm setting, for the welcome;
 * null until both are in, or when either cannot be read (the picker is then
 * left out and the session follows the organisation).
 */
export function useModelOptions(client: ApiClient): ModelOptions | null {
  const [options, setOptions] = useState<ModelOptions | null>(null);
  useEffect(() => {
    let current = true;
    void Promise.all([readTiers(client), readOrganization(client)]).then(([tiers, org]) => {
      if (current) setOptions({ tiers, organization: { tier: tiers.find((t) => t.id === org.tierId) ?? null, harness: org.harness } });
    }, () => undefined);
    return () => {
      current = false;
    };
  }, [client]);
  return options;
}

/** The organisation's tiers once `wanted` (the rail's menu first opened): null until read; "failed" when they cannot be. */
export function useTiers(client: ApiClient, wanted: boolean): readonly PickerTier[] | "failed" | null {
  const [tiers, setTiers] = useState<readonly PickerTier[] | "failed" | null>(null);
  useEffect(() => {
    if (!wanted) return;
    let current = true;
    void readTiers(client).then((t) => current && setTiers(t), () => current && setTiers("failed"));
    return () => {
      current = false;
    };
  }, [client, wanted]);
  return tiers;
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
