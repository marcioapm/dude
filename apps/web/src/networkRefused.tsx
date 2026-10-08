/**
 * In a Run: the note under a tool call whose output names a host lux
 * refused this Run (its `agent.network.refused` events), with Allow for the
 * project. A call names a host when the host's whole name is in its output: simple,
 * and honest about what it knows — a Run refused a name, and this call's
 * output mentions it.
 */

import { useEffect, useMemo, useState, type ReactNode } from "react";
import { NetworkRefusedNote, type ToolOutput } from "@dude/design-system/components";
import { egressAllows, EventTypes, type PersistedEvent, type SettingsResponse } from "@dude/domain";
import type { ApiClient } from "./api/client.ts";
import type { ToolResult, ToolTurn } from "./api/conversation.ts";
import { formatPlace } from "./place.ts";

/** The hosts lux refused the Run, each once, in the order it refused them. */
export function refusedHosts(events: readonly PersistedEvent[]): string[] {
  const names = events.filter((e) => e.eventType === EventTypes.NetworkRefused).map((e) => String(e.payload["name"] ?? "").toLowerCase());
  return [...new Set(names.filter(Boolean))];
}

const text = (o: ToolOutput | undefined) => (o ? `${o.head}\n${o.tail ?? ""}` : "");

/**
 * Matchers for the refused hosts, longest first so a call naming
 * api.github.com is labelled with it rather than github.com. A host counts
 * only as a whole name: not inside api.github.com, notpypi.org or
 * pypi.org.internal, though a sentence's full stop may follow it.
 */
export function hostMatchers(hosts: readonly string[]): Array<[string, RegExp]> {
  return [...hosts].sort((a, b) => b.length - a.length)
    .map((h) => [h, new RegExp(`(?<![a-z0-9.-])${h.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}(?!\\.?[a-z0-9-])`)]);
}

/** The most specific refused host the call's output names, if any. */
export function refusedHostIn(result: ToolResult | null, matchers: ReadonlyArray<[string, RegExp]>): string | null {
  if (!result || matchers.length === 0) return null;
  const out = `${text(result.output)}\n${text(result.stdout)}\n${text(result.stderr)}`.toLowerCase();
  return matchers.find(([, re]) => re.test(out))?.[0] ?? null;
}

/**
 * The note for each tool call of a Run, or undefined. The project's
 * settings are read once the Run has a refusal: whose lists it was not on
 * (the organisation's only while the project adds to it), whether this
 * person may Allow, and whether the project's list reaches the host now.
 */
export function useNetworkNotes(client: ApiClient, projectId: string | undefined, events: readonly PersistedEvent[]): ((turn: ToolTurn) => ReactNode) | undefined {
  const hosts = useMemo(() => refusedHosts(events), [events]);
  const matchers = useMemo(() => hostMatchers(hosts), [hosts]);
  // A call's result never changes once it has one: each is scanned once
  // per set of refused hosts, not on every render of a live Run.
  const matched = useMemo(() => new WeakMap<ToolResult, string | null>(), [matchers]);
  const [settings, setSettings] = useState<SettingsResponse | null>(null);
  const [busy, setBusy] = useState(false);
  const needed = hosts.length > 0 && projectId !== undefined;
  useEffect(() => {
    if (!needed || !projectId) return;
    let cancelled = false;
    client.projectSettings(projectId).then((s) => !cancelled && setSettings(s), () => undefined);
    return () => {
      cancelled = true;
    };
  }, [client, projectId, needed]);
  if (!needed || !settings?.project) return undefined;
  const project = settings.project;
  const organization = settings.network.mode?.value === "only" ? null : settings.organization.name;
  const allow = (host: string) => {
    setBusy(true);
    client.allowNames(project.id, [host]).then(setSettings, () => undefined).finally(() => setBusy(false));
  };
  const openSettings = () => {
    window.location.hash = formatPlace({ view: "projectSettings", projectId: project.id, page: "network" });
  };
  return (turn) => {
    const result = turn.result;
    if (!result) return undefined;
    let host = matched.get(result);
    if (host === undefined) matched.set(result, (host = refusedHostIn(result, matchers)));
    if (!host) return undefined;
    return (
      <NetworkRefusedNote host={host} project={project.name} organization={organization}
        allowed={egressAllows(settings.network.effective, host)} busy={busy}
        onAllow={settings.canEdit ? () => allow(host) : undefined} onSettings={openSettings} />
    );
  };
}
