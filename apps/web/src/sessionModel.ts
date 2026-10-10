/**
 * A brainstorm session's model tier and harness, as the web reads them:
 * the organisation's tiers and its Brainstorm setting, for the welcome's
 * picker and the owner's in a session's rail.
 */

import { useEffect, useState, useSyncExternalStore } from "react";
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

// Bumped by each forgetModelOptions, so a mounted welcome or rail re-reads rather than keeping a removed tier.
let forgotten = 0;
const forgetting = new Set<() => void>();
function subscribeForgotten(changed: () => void): () => void {
  forgetting.add(changed);
  return () => forgetting.delete(changed);
}
const useForgotten = () => useSyncExternalStore(subscribeForgotten, () => forgotten);

/** Drop what was read: the organisation's tiers or settings changed. A mounted picker reads them again. */
export function forgetModelOptions(client: ApiClient): void {
  tiersRead.delete(client);
  organizationRead.delete(client);
  forgotten += 1;
  for (const changed of forgetting) changed();
}

/**
 * The organisation's tiers and its Brainstorm setting, for the welcome;
 * null until both are in, or when either cannot be read (the picker is then
 * left out and the session follows the organisation).
 */
export function useModelOptions(client: ApiClient): ModelOptions | null {
  const [options, setOptions] = useState<ModelOptions | null>(null);
  const version = useForgotten();
  useEffect(() => {
    let current = true;
    void Promise.all([readTiers(client), readOrganization(client)]).then(([tiers, org]) => {
      if (current) setOptions({ tiers, organization: { tier: tiers.find((t) => t.id === org.tierId) ?? null, harness: org.harness } });
    }, () => undefined);
    return () => {
      current = false;
    };
  }, [client, version]);
  return options;
}

/**
 * The organisation's tiers once the rail's menu has opened (`openings` > 0):
 * null until read; "failed" when they cannot be, until the next opening
 * tries again.
 */
export function useTiers(client: ApiClient, openings: number): readonly PickerTier[] | "failed" | null {
  const [read, setRead] = useState<{ tiers: readonly PickerTier[] | "failed"; openings: number } | null>(null);
  const version = useForgotten();
  useEffect(() => {
    if (openings === 0) return;
    let current = true;
    // A list already read is kept by readTiers: an opening after the first costs no request.
    void readTiers(client).then((tiers) => current && setRead({ tiers, openings }), () => current && setRead({ tiers: "failed", openings }));
    return () => {
      current = false;
    };
  }, [client, openings, version]);
  if (read?.tiers === "failed" && read.openings !== openings) return null;
  return read?.tiers ?? null;
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
