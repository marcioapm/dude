/**
 * Organisation and project settings → Network: what an agent's Run may
 * reach from inside its container. The organisation lists hosts every Run
 * of it may reach; a project adds its own, or runs on its own alone. What
 * is always reachable (the model, dude's tools) is shown, never listed. A
 * project's page also lists what its agents were refused lately, with
 * Allow. A Run gets the lists as they are when it starts.
 */

import { useCallback, useEffect, useState } from "react";
import { HostChips, HostPresets, RefusedHosts, SettingRow, SettingSource, SettingsHeader, SettingsNote, SettingsSection, Switch, TextButton, type HostPreset } from "@dude/design-system/components";
import { Callout } from "@dude/design-system/primitives";
import { egressAllows, egressProblem, type RefusedName, type SettingsResponse } from "@dude/domain";
import type { SettingsScope } from "./settingsPages.tsx";

/** What a toolchain needs, by its name. */
export const NETWORK_PRESETS: ReadonlyArray<HostPreset> = [
  { name: "GitHub", hosts: ["github.com", "*.github.com", "objects.githubusercontent.com"] },
  { name: "npm", hosts: ["registry.npmjs.org"] },
  { name: "PyPI", hosts: ["pypi.org", "files.pythonhosted.org"] },
  { name: "Go modules", hosts: ["proxy.golang.org", "sum.golang.org"] },
  { name: "crates.io", hosts: ["crates.io", "static.crates.io", "index.crates.io"] },
  { name: "Docker Hub", hosts: ["registry-1.docker.io", "auth.docker.io", "production.cloudflare.docker.com"] },
];

const ANYWHERE = "*";
const plus = (list: readonly string[], more: readonly string[]) => [...new Set([...list, ...more])];

export function NetworkPage({ scope, onOrganization }: {
  scope: SettingsScope;
  /** A project's page: the organisation's own Network page, where its list is changed. */
  onOrganization?: (() => void) | undefined;
}) {
  const { settings } = scope;
  return settings.project ? <ProjectNetwork scope={scope} project={settings.project} onOrganization={onOrganization} /> : <OrganizationNetwork scope={scope} />;
}

function OrganizationNetwork({ scope }: { scope: SettingsScope }) {
  const { settings } = scope;
  const org = settings.organization.name;
  const list = settings.network.egress.value;
  const anywhere = list.includes(ANYWHERE);
  const hosts = list.filter((h) => h !== ANYWHERE);
  const disabled = !settings.canEdit;
  const save = (egress: string[], done: string) => void scope.patch({ network: { egress: anywhere ? [ANYWHERE, ...egress] : egress } }, done).catch(() => {});
  return (
    <>
      <SettingsHeader title="Network" description={`What an agent may reach from inside its container while it works for ${org}: package registries, GitHub, an internal mirror. Everything else is refused. Each project can add to this list.`} />
      <SettingsSection data-testid="network-page">
        <SettingRow label="Agents may reach" help="A hostname, an address or a range. Presets add what a toolchain needs, by name.">
          {anywhere ? (
            <Callout tone="danger" data-testid="network-anywhere">
              <b>Anywhere.</b> Agents may reach any host. Code in a Run — and whatever a prompt talked it into — can leave the container. The list below is kept but not applied.
            </Callout>
          ) : null}
          <HostChips hosts={hosts} validate={egressProblem} disabled={disabled} placeholder="registry.example.com" label="Add a host agents may reach"
            onChange={(next) => save(next, "Network saved")} data-testid="network-egress" />
          {!anywhere && hosts.length === 0 ? (
            <Callout tone="info" data-testid="network-empty">
              <b>Agents can reach only their model.</b> <code>npm install</code>, <code>uv sync</code> and anything that asks GitHub will fail inside a Run. Allow what your projects’ toolchains need.
            </Callout>
          ) : null}
          <HostPresets presets={NETWORK_PRESETS} has={(h) => egressAllows(list, h)} disabled={disabled}
            onAdd={(p) => save(plus(hosts, p.hosts), `${p.name} added`)} data-testid="network-presets" />
          <AlwaysReachable settings={settings} />
        </SettingRow>
        <SettingRow label="Anywhere" help="Turn the list off. For a dude on a private network, or while a toolchain is still being worked out.">
          <Switch checked={anywhere} disabled={disabled} testId="network-anywhere-switch"
            label={anywhere ? "On — agents may reach any host" : "Off — only the list above"}
            onCheckedChange={(on) => void scope.patch({ network: { egress: on ? [ANYWHERE, ...hosts] : hosts } }, on ? "Agents may reach anywhere" : "Only the list applies").catch(() => {})} />
        </SettingRow>
        <ChangeNote settings={settings} />
      </SettingsSection>
    </>
  );
}

function ProjectNetwork({ scope, project, onOrganization }: { scope: SettingsScope; project: { id: string; name: string }; onOrganization?: (() => void) | undefined }) {
  const { settings, client } = scope;
  const org = settings.organization.name;
  const network = settings.network;
  const own = network.egress.value;
  const only = network.mode?.value === "only";
  const orgHosts = network.organizationEgress ?? [];
  const disabled = !settings.canEdit;
  const [refused, setRefused] = useState<RefusedName[]>([]);
  const load = useCallback(() => void client.refusedNames(project.id).then((r) => setRefused(r.refused), () => setRefused([])), [client, project.id]);
  // Read again whenever the list changes: a name allowed since is not refused.
  useEffect(load, [load, network.effective.join(" ")]);
  const save = (egress: string[], done: string) => void scope.patch({ network: { egress } }, done).catch(() => {});
  const allow = (names: string[]) =>
    void client.allowNames(project.id, names).then(scope.replace, () => undefined);
  return (
    <>
      <SettingsHeader title="Network" description={`What an agent may reach from inside its container while it works on ${project.name}. ${org}’s list applies here; add what this project needs on top of it.`} />
      <SettingsSection data-testid="network-page">
        <SettingRow label="Agents may reach" help={`${org}’s hosts, then this project’s. A hostname, an address or a range.`}>
          {only ? (
            <>
              <SettingSource source="project" from={org} inherited={`${orgHosts.length} ${orgHosts.length === 1 ? "host" : "hosts"}`} disabled={disabled}
                onReset={disabled ? undefined : () => void scope.patch({ network: { mode: "add", egress: [] } }, `Back to ${org}’s list`).catch(() => {})} />
              <span className="networkLabel">Only these, for {project.name}</span>
            </>
          ) : (
            <>
              <span className="networkFrom" data-testid="network-from">
                <SettingSource source="organization" from={org} />
                {onOrganization ? <> · <TextButton onClick={onOrganization}>change in {org}’s settings</TextButton></> : null}
              </span>
              <HostChips readOnly hosts={orgHosts} data-testid="network-org-egress" />
              <span className="networkLabel">{project.name} also</span>
            </>
          )}
          {disabled ? <HostChips readOnly hosts={own} data-testid="network-egress" /> : (
            <HostChips hosts={own} validate={egressProblem} placeholder={only ? "another host" : "a host this project needs"} label={`Add a host ${project.name} may reach`}
              onChange={(next) => save(next, "Network saved")} data-testid="network-egress" />
          )}
          <HostPresets presets={NETWORK_PRESETS} has={(h) => egressAllows(network.effective, h)} disabled={disabled}
            onAdd={(p) => save(plus(own, p.hosts), `${p.name} added`)} data-testid="network-presets" />
          <AlwaysReachable settings={settings} />
        </SettingRow>
        <SettingRow label="Only this project’s list" help={`Ignore ${org}’s hosts here. For code that must stay tighter than the rest of the organisation.`}>
          <Switch checked={only} disabled={disabled} testId="network-only-switch"
            label={only ? `On — ${org}’s list is ignored for ${project.name}` : `Off — ${org}’s list applies, plus the hosts above`}
            onCheckedChange={(on) => void scope.patch({ network: { mode: on ? "only" : "add" } }, on ? "Only this project’s list" : `${org}’s list applies`).catch(() => {})} />
        </SettingRow>
        {refused.length ? (
          <SettingRow label="Refused recently" help={`Hosts agents on ${project.name} tried in the last 7 days and were not allowed to reach.`} block data-testid="network-refused-row">
            <RefusedHosts refused={refused} target={project.name} onAllow={disabled ? undefined : allow} />
          </SettingRow>
        ) : null}
        <ChangeNote settings={settings} />
      </SettingsSection>
    </>
  );
}

/** The model's host and dude's tools: every Run reaches them, so the list reads whole. */
function AlwaysReachable({ settings }: { settings: SettingsResponse }) {
  return (
    <>
      <span className="networkLabel">Always reachable</span>
      <HostChips readOnly muted hosts={settings.network.always} data-testid="network-always" />
    </>
  );
}

/** When a change applies, and the operator's own list under every organisation's. */
function ChangeNote({ settings }: { settings: SettingsResponse }) {
  const operator = settings.network.operator;
  return (
    <SettingsNote icon="info">
      A change reaches Runs started after it; a running agent keeps the network it started with.
      {settings.project ? null : <> The operator’s own list (<code>DUDE_AGENT_EGRESS</code>) always applies as well{operator.length && !operator.includes(ANYWHERE) ? <>: <code>{operator.join(", ")}</code></> : null}.</>}
      {operator.includes(ANYWHERE) ? <> The operator’s list allows anywhere: every Run is unrestricted, whatever these lists say.</> : null}
    </SettingsNote>
  );
}
