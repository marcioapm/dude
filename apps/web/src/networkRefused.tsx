/**
 * In a Run: the note under a tool call whose output names a host lux
 * refused this Run (its `agent.network.refused` events), with Allow for the
 * project. A call names a host when the host's name is in its output: simple,
 * and honest about what it knows — a Run refused a name, and this call's
 * output mentions it.
 */

import { useEffect, useMemo, useState, type ReactNode } from "react";
import { NetworkRefusedNote, type ToolOutput } from "@dude/design-system/components";
import { EventTypes, type PersistedEvent, type SettingsResponse } from "@dude/domain";
import type { ApiClient } from "./api/client.ts";
import type { ToolTurn } from "./api/conversation.ts";
import { formatPlace } from "./place.ts";

/** The hosts lux refused the Run, each once, in the order it refused them. */
export function refusedHosts(events: readonly PersistedEvent[]): string[] {
  const names = events.filter((e) => e.eventType === EventTypes.NetworkRefused).map((e) => String(e.payload["name"] ?? "").toLowerCase());
  return [...new Set(names.filter(Boolean))];
}

const text = (o: ToolOutput | undefined) => (o ? `${o.head}\n${o.tail ?? ""}` : "");

/** The first refused host the call's output names, if any. */
export function refusedHostIn(turn: ToolTurn, hosts: readonly string[]): string | null {
  const r = turn.result;
  if (!r || hosts.length === 0) return null;
  const out = `${text(r.output)}\n${text(r.stdout)}\n${text(r.stderr)}`.toLowerCase();
  return hosts.find((h) => out.includes(h)) ?? null;
}

/**
 * The note for each tool call of a Run, or undefined. The project's
 * settings are read once the Run has a refusal: whose lists it was not on
 * (the organisation's only while the project adds to it), and whether this
 * person may Allow.
 */
export function useNetworkNotes(client: ApiClient, projectId: string | undefined, events: readonly PersistedEvent[]): ((turn: ToolTurn) => ReactNode) | undefined {
  const hosts = useMemo(() => refusedHosts(events), [events]);
  const [settings, setSettings] = useState<SettingsResponse | null>(null);
  const [allowed, setAllowed] = useState<ReadonlySet<string>>(new Set());
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
    client.allowNames(project.id, [host]).then(
      (s) => {
        setSettings(s);
        setAllowed((a) => new Set([...a, host]));
      },
      () => undefined,
    ).finally(() => setBusy(false));
  };
  const openSettings = () => {
    window.location.hash = formatPlace({ view: "projectSettings", projectId: project.id, page: "network" });
  };
  return (turn) => {
    const host = refusedHostIn(turn, hosts);
    if (!host) return undefined;
    return (
      <NetworkRefusedNote host={host} project={project.name} organization={organization} allowed={allowed.has(host)} busy={busy}
        onAllow={settings.canEdit ? () => allow(host) : undefined} onSettings={openSettings} />
    );
  };
}
