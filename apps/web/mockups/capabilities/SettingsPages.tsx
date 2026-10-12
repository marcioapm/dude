/*
 * The settings pages, as proposed: a role's page with its Capabilities
 * under Harness / Model / Machine / Image, the Agents → Capabilities
 * matrix, and the Network page after the brainstorm's switch moved out.
 * Composed from the design system's settings pieces, as the app's
 * RolePage is; the new pieces are in components.tsx.
 */

import { useState, type ReactNode } from "react";
import {
  AgentAvatar,
  Capabilities,
  HostChips,
  HostPresets,
  ImagePicker,
  SettingField,
  SettingFields,
  SettingRow,
  SettingSource,
  SettingsHeader,
  SettingsMeta,
  SettingsNote,
  SettingsSection,
  Switch,
  TextButton,
} from "@dude/design-system/components";
import { Callout, Select, Table, TBody, Td, Th, THead, Tr } from "@dude/design-system/primitives";
import { BeyondMark, CapabilityCell, LevelChoice } from "./components.tsx";
import {
  ACTS,
  ACT_LABEL,
  HARNESS_LABEL,
  HAS,
  FIXED,
  HOLDS,
  INTERNET_DESCRIPTION,
  INTERNET_LABEL,
  INTERNET_LEVELS,
  ORG,
  ORG_CAPS,
  PROJECT_OVERRIDES,
  ROLES,
  ROLE_DESCRIPTION,
  ROLE_LABEL,
  SETUP,
  actDescription,
  faceOf,
  resolve,
  type Act,
  type Harness,
  type Internet,
  type Layered,
  type Overrides,
  type ProjectName,
  type Role,
} from "./data.ts";
import styles from "./capabilities.module.css";

export type Where = { readonly kind: "org" } | { readonly kind: "project"; readonly project: ProjectName };

/** The org's values and the project's overrides, changed in place so the mockup answers clicks. */
export interface Store {
  readonly overrides: Record<ProjectName, Overrides>;
  readonly setOverride: (project: ProjectName, role: Role, change: Overrides[Role] | null) => void;
}

const lower = (s: string) => s.charAt(0).toLowerCase() + s.slice(1);

// --------------------------------------------------------------------------- the danger callouts

/** #110's words, for any role: what a page can do with what the container holds. */
export function AnyHostRisk({ role }: { readonly role: Role }) {
  return (
    <Callout tone="danger" data-testid="risk-any-host">
      <b>Anything a page says can steer the agent.</b> The {lower(ROLE_LABEL[role])} has {HOLDS[role]}, in its container. A malicious page could talk it into sending them to any host.
    </Callout>
  );
}

export function WebSearchRisk({ role, harness }: { readonly role: Role; readonly harness: Harness }) {
  return (
    <Callout tone="danger" data-testid="risk-web-search">
      <b>What a search returns can steer the agent.</b> Results reach the {lower(ROLE_LABEL[role])} as text it reads, and it has {HOLDS[role]}.
      {harness === "claude_code" ? " Its queries — words from your code and the task — go to the model’s provider." : harness === "opencode" ? " Its queries — words from your code and the task — go to Exa." : " Its queries — words from your code and the task — go to OpenAI."}
    </Callout>
  );
}

// --------------------------------------------------------------------------- a role's page

export function RolePage({ role, where, store, onImages, onCapabilities }: {
  readonly role: Role;
  readonly where: Where;
  readonly store: Store;
  readonly onImages: () => void;
  readonly onCapabilities: () => void;
}) {
  const setup = SETUP[role];
  const project = where.kind === "project" ? where.project : null;
  const always = role !== "simplifier" && role !== "qa_browser";
  return (
    <>
      <SettingsHeader leading={<AgentAvatar role={faceOf(role)} size="lg" />} title={ROLE_LABEL[role]} description={ROLE_DESCRIPTION[role]}
        actions={<Switch checked disabled={always} label={always ? "Always on" : project ? `On in ${project}` : "On"} onCheckedChange={() => undefined} />} />
      <SettingFields>
        <SettingField label="Harness" htmlFor={`harness-${role}`} source={project ? <SettingSource source="organization" from={ORG} /> : null}>
          <Select id={`harness-${role}`} aria-label="Harness" value={setup.harness} onValueChange={() => undefined}
            options={(["opencode", "claude_code", "codex"] as const).map((h) => ({ value: h, label: HARNESS_LABEL[h] }))}
            hint="The coding agent that runs it. The model tier below says which model it asks for." />
        </SettingField>
        <SettingField label="Model" htmlFor={`tier-${role}`} source={project ? <SettingSource source="organization" from={ORG} /> : null}>
          <Select id={`tier-${role}`} aria-label="Model" value="t" onValueChange={() => undefined}
            options={[{ value: "t", label: setup.tier, meta: <span className="ds-mono">{setup.model}</span> }]}
            hint={project ? undefined : <span>Requests <code>{setup.model}</code> · high effort</span>} />
        </SettingField>
        {role === "conductor" ? null : (
          <SettingField label="Time limit without progress" source={project ? <SettingSource source="organization" from={ORG} /> : null}>
            <Select aria-label="Time limit without progress" value="60" onValueChange={() => undefined} options={[{ value: "60", label: "1 hour" }]}
              hint="After this long without progress, the owner is told. Runs are stopped at 4 hours." />
          </SettingField>
        )}
        <SettingField label="Machine" htmlFor={`machine-${role}`} source={project ? <SettingSource source="organization" from={ORG} /> : null}>
          <Select id={`machine-${role}`} aria-label="Machine" value="m" onValueChange={() => undefined}
            options={[{ value: "m", label: setup.machine, meta: setup.machine === "Large" ? "8 CPUs · 16 GiB · 80 GiB" : setup.machine === "Medium" ? "4 CPUs · 8 GiB · 40 GiB" : "2 CPUs · 4 GiB · 20 GiB" }]} />
        </SettingField>
      </SettingFields>
      <SettingsSection title="Image">
        <SettingRow className={styles["row"]} label={`${ROLE_LABEL[role]}’s image`} help="What this role works in, over each project’s runtime image. Without one, the project’s."
          source={project ? <SettingSource source="organization" from={ORG} /> : null}>
          <ImagePicker label={`${ROLE_LABEL[role]}’s image`} value={setup.image} onChange={() => undefined}
            images={[
              { id: "acme-base", name: "acme-base", description: "Node 22, Go 1.24, Python 3.13", version: 7, isDefault: true },
              { id: "acme-browser", name: "acme-browser", description: "acme-base with Chromium and podman", version: 3, canRunContainers: true },
            ]} />
        </SettingRow>
      </SettingsSection>
      <CapabilitySections role={role} where={where} store={store} onImages={onImages} onCapabilities={onCapabilities} />
    </>
  );
}

function CapabilitySections({ role, where, store, onImages, onCapabilities }: {
  readonly role: Role;
  readonly where: Where;
  readonly store: Store;
  readonly onImages: () => void;
  readonly onCapabilities: () => void;
}) {
  const project = where.kind === "project" ? where.project : null;
  const own = project ? store.overrides[project] : null;
  const caps = resolve(role, own);
  const setup = SETUP[role];
  const org = ORG_CAPS[role];
  const [orgEdits, setOrgEdits] = useState(0);
  // On the organisation the mockup changes its own copy, so the page answers clicks.
  const change = (c: NonNullable<Overrides[Role]>) => {
    if (project) {
      const mine = own?.[role] ?? {};
      store.setOverride(project, role, { ...mine, ...c, acts: { ...mine.acts, ...c.acts } });
    } else {
      ORG_CAPS[role] = { ...org, ...c, acts: { ...org.acts, ...c.acts } } as typeof org;
      setOrgEdits(orgEdits + 1);
    }
  };
  const reset = (key: "internet" | "webSearch" | "containers" | Act) => {
    if (!project) return;
    const mine = { ...own?.[role] };
    if (key === "internet" || key === "webSearch" || key === "containers") delete mine[key];
    else if (mine.acts) {
      const acts = { ...mine.acts };
      delete acts[key];
      mine.acts = acts;
    }
    store.setOverride(project, role, mine);
  };
  const source = <T,>(l: Layered<T>, words: (v: T) => ReactNode, key: Parameters<typeof reset>[0]) =>
    project ? (
      <span className={styles["source"]}>
        <SettingSource source={l.source} from={ORG} inherited={l.source === "project" ? words(l.organization) : undefined} onReset={() => reset(key)} />
        {l.wider ? <BeyondMark from={ORG} /> : null}
      </span>
    ) : null;
  const onOff = (b: boolean) => (b ? "on" : "off");

  const searchDisabled = caps.internet.value === "dude" && setup.harness === "opencode";
  const containersPossible = setup.imageCanRunContainers;
  const acts = HAS[role];

  return (
    <>
      <SettingsSection title="Capabilities · Reach" data-testid="capabilities"
        actions={<SettingsMeta>A change reaches agents started after it · <TextButton onClick={onCapabilities}>Every role’s</TextButton></SettingsMeta>}>
        <SettingRow className={styles["row"]} label="Internet" help="Hosts its container may reach. Its model and dude’s tools are always reachable." block data-testid="cap-internet">
          <LevelChoice<Internet> label="Internet" value={caps.internet.value} onChange={(v) => change({ internet: v })}
            levels={INTERNET_LEVELS.map((v) => ({
              value: v,
              label: INTERNET_LABEL[v],
              description: INTERNET_DESCRIPTION[v],
              tag: project && v === caps.internet.organization ? `${ORG}’s` : undefined,
              caution: v === "any" ? "Widens what the agent can reach" : undefined,
            }))} />
          {source(caps.internet, (v) => INTERNET_LABEL[v].toLowerCase(), "internet")}
          {caps.internet.value === "any" ? <AnyHostRisk role={role} /> : null}
        </SettingRow>
        <SettingRow className={styles["row"]} label="Web search" help={<WebSearchHint harness={setup.harness} />} data-testid="cap-web-search">
          <Switch checked={caps.webSearch.value && !searchDisabled} disabled={searchDisabled}
            label={searchDisabled ? "Off — OpenCode searches through Exa, which dude only cannot reach" : caps.webSearch.value ? `On — the ${lower(ROLE_LABEL[role])} may search the web` : "Off — no search tool"}
            onCheckedChange={(on) => change({ webSearch: on })} />
          {source(caps.webSearch, onOff, "webSearch")}
          {caps.webSearch.value && caps.internet.value !== "any" && !searchDisabled ? <WebSearchRisk role={role} harness={setup.harness} /> : null}
        </SettingRow>
        <SettingRow className={styles["row"]} label="Containers" help="Start containers inside its Run (podman, Docker): a database for the tests, the app’s own compose file." data-testid="cap-containers">
          <Switch checked={caps.containers.value && containersPossible} disabled={!containersPossible}
            label={!containersPossible ? "Off — its image can’t run containers" : caps.containers.value ? "On — it may start containers" : "Off — it may not start containers"}
            onCheckedChange={(on) => change({ containers: on })} />
          {containersPossible ? source(caps.containers, onOff, "containers") : (
            <SettingsMeta>
              <span><code>{setup.image}</code> can’t run containers. Choose an image that can above, or mark one in <TextButton onClick={onImages}>Images</TextButton>.</span>
            </SettingsMeta>
          )}
        </SettingRow>
      </SettingsSection>
      <SettingsSection title="Capabilities · Acts in dude" data-testid="capabilities-acts">
        {acts.map((a) => {
          const l = caps.acts[a]!;
          return (
            <SettingRow className={styles["row"]} key={a} label={role === "brainstorm" && a === "create" ? "Propose work" : ACT_LABEL[a]} help={actDescription(role, a)} data-testid={`cap-act-${a}`}>
              <Switch checked={l.value} label={l.value ? "On" : "Off"} onCheckedChange={(on) => change({ acts: { [a]: on } })} />
              {source(l, onOff, a)}
            </SettingRow>
          );
        })}
        {ACTS.length > acts.length ? (
          <SettingsMeta>
            Not the {lower(ROLE_LABEL[role])}’s to do: {ACTS.filter((a) => !acts.includes(a)).map((a) => lower(ACT_LABEL[a])).join(", ")}.
          </SettingsMeta>
        ) : null}
      </SettingsSection>
      <SettingsSection title="Capabilities · Fixed by role" data-testid="capabilities-fixed">
        <SettingRow className={styles["row"]} label="Code" help="What the role is for decides it; no setting changes it.">
          <Capabilities can={FIXED[role].filter((f) => f.can).map((f) => f.text)} cannot={FIXED[role].filter((f) => !f.can).map((f) => f.text)} />
        </SettingRow>
      </SettingsSection>
    </>
  );
}

/** The role's harness's search in a sentence, then the other two in a few words each. */
function WebSearchHint({ harness }: { readonly harness: Harness }) {
  const own: Record<Harness, ReactNode> = {
    claude_code: <>Claude Code’s WebSearch runs at the model’s provider, through dude’s proxy. Opening a result (WebFetch) needs Internet beyond dude only.</>,
    opencode: <>OpenCode’s websearch goes through Exa: dude adds <code>mcp.exa.ai</code> to the Run’s hosts, so it needs Internet beyond dude only.</>,
    codex: <>Codex’s web_search runs at OpenAI, through dude’s proxy: it needs no host.</>,
  };
  const short: Record<Harness, string> = { claude_code: "Claude Code: at the provider", opencode: "OpenCode: through Exa", codex: "Codex: at OpenAI" };
  return (
    <span className={styles["hintList"]}>
      <span>The harness’s own search tool. {own[harness]}</span>
      <span>{(["claude_code", "opencode", "codex"] as const).filter((h) => h !== harness).map((h) => short[h]).join(" · ")}.</span>
    </span>
  );
}

// --------------------------------------------------------------------------- the matrix

export function CapabilitiesMatrix({ where, store, onRole }: { readonly where: Where; readonly store: Store; readonly onRole: (role: Role) => void }) {
  const project = where.kind === "project" ? where.project : null;
  const own = project ? store.overrides[project] : null;
  const rows = ROLES.map((r) => ({ role: r, caps: resolve(r, own) }));
  const overridden = rows.flatMap(({ caps }) => [caps.internet, caps.webSearch, caps.containers, ...Object.values(caps.acts)]).filter((l) => l?.source === "project");
  const wider = overridden.filter((l) => l?.wider);
  const src = (role: Role, l: Layered<unknown>, words: ReactNode, reset: () => void) =>
    project ? { source: l.source, from: ORG, inherited: words, wider: l.wider, onReset: reset } : undefined;
  const resetRole = (role: Role, key: "internet" | "webSearch" | "containers" | "acts") => {
    if (!project) return;
    const mine = { ...own?.[role] };
    delete mine[key];
    store.setOverride(project, role, mine);
  };
  return (
    <>
      <SettingsHeader title="Capabilities" description={project
        ? `What each agent may do on ${project}: ${ORG}’s, unless ${project} sets its own. A value here can be narrower or wider than ${ORG}’s.`
        : `What each agent may reach and do across ${ORG}. Each project follows these unless it sets its own.`} />
      {project ? (
        <SettingsNote icon="layers">
          {overridden.length ? `${overridden.length} ${overridden.length === 1 ? "value is" : "values are"} ${project}’s own` : `Every value is ${ORG}’s`}
          {wider.length ? <>; {wider.length} {wider.length === 1 ? "goes" : "go"} beyond {ORG}’s, marked <b>Beyond {ORG}</b>.</> : "."} Reset puts {ORG}’s back.
        </SettingsNote>
      ) : null}
      <SettingsSection data-testid="capabilities-matrix">
        <Table density={project ? "comfortable" : "default"} aria-label={`Capabilities by role${project ? ` on ${project}` : ""}`}>
          <THead>
            <Tr>
              <Th>Agent</Th>
              <Th>Internet</Th>
              <Th>Web search</Th>
              <Th>Containers</Th>
              <Th>Acts in dude</Th>
              <Th hideWhenNarrow>Code</Th>
            </Tr>
          </THead>
          <TBody>
            {rows.map(({ role, caps }) => {
              const name = ROLE_LABEL[role];
              const actList = HAS[role].map((a) => caps.acts[a]!);
              const actsOn = actList.filter((l) => l.value).length;
              const actsOwn = actList.filter((l) => l.source === "project");
              const actsLayer: Layered<unknown> = {
                value: actsOn, organization: HAS[role].filter((a) => ORG_CAPS[role].acts[a]).length,
                source: actsOwn.length ? "project" : "organization", wider: actsOwn.some((l) => l.wider),
              };
              const pushes = FIXED[role].some((f) => f.can && /Pushes|Publishes/.test(f.text));
              const canContainers = SETUP[role].imageCanRunContainers;
              return (
                <Tr key={role} data-role={role}>
                  <Td fit>
                    <span className={styles["roleCell"]}>
                      <AgentAvatar role={faceOf(role)} size="sm" />
                      <TextButton className={styles["roleName"]} onClick={() => onRole(role)}>{name}</TextButton>
                    </span>
                  </Td>
                  <Td wrap>
                    <CapabilityCell icon={caps.internet.value === "any" ? "warning" : "globe"} off={caps.internet.value === "dude"} value={INTERNET_LABEL[caps.internet.value]}
                      label={`${name}, Internet: ${INTERNET_LABEL[caps.internet.value]}${caps.internet.source === "organization" && project ? `, from ${ORG}` : ""}`} onOpen={() => onRole(role)}
                      source={src(role, caps.internet, INTERNET_LABEL[caps.internet.organization].toLowerCase(), () => resetRole(role, "internet"))} />
                  </Td>
                  <Td wrap>
                    <CapabilityCell icon={caps.webSearch.value ? "search" : "cross"} off={!caps.webSearch.value} value={caps.webSearch.value ? "On" : "Off"}
                      label={`${name}, Web search: ${caps.webSearch.value ? "on" : "off"}`} onOpen={() => onRole(role)}
                      source={src(role, caps.webSearch, caps.webSearch.organization ? "on" : "off", () => resetRole(role, "webSearch"))} />
                  </Td>
                  <Td wrap>
                    {canContainers ? (
                      <CapabilityCell icon={caps.containers.value ? "cube" : "cross"} off={!caps.containers.value} value={caps.containers.value ? "On" : "Off"}
                        label={`${name}, Containers: ${caps.containers.value ? "on" : "off"}`} onOpen={() => onRole(role)}
                        source={src(role, caps.containers, caps.containers.organization ? "on" : "off", () => resetRole(role, "containers"))} />
                    ) : (
                      <CapabilityCell icon="cross" off value="Image can’t" label={`${name}, Containers: off, its image can’t run containers`} onOpen={() => onRole(role)} />
                    )}
                  </Td>
                  <Td wrap>
                    <CapabilityCell icon="zap" off={actsOn === 0} value={`${actsOn} of ${actList.length}`}
                      label={`${name}, Acts in dude: ${actsOn} of ${actList.length} on`} onOpen={() => onRole(role)}
                      source={src(role, actsLayer, `${actsLayer.organization} of ${actList.length}`, () => resetRole(role, "acts"))} />
                  </Td>
                  <Td hideWhenNarrow muted wrap>
                    {role === "conductor" ? "Publishes, steers" : pushes ? "Pushes" : "Reads"}
                  </Td>
                </Tr>
              );
            })}
          </TBody>
        </Table>
      </SettingsSection>
      <SettingsNote icon="info">
        A value opens its role’s page. Code is fixed by role: the implementer, fixer and simplifier push their branch, the conductor publishes small changes and steers the others, the rest read.
      </SettingsNote>
    </>
  );
}

// --------------------------------------------------------------------------- Network

const PRESETS = [
  { name: "GitHub", hosts: ["github.com", "*.github.com", "objects.githubusercontent.com"] },
  { name: "npm", hosts: ["registry.npmjs.org"] },
  { name: "PyPI", hosts: ["pypi.org", "files.pythonhosted.org"] },
  { name: "Go modules", hosts: ["proxy.golang.org", "sum.golang.org"] },
];
const ORG_HOSTS = ["github.com", "*.github.com", "objects.githubusercontent.com", "registry.npmjs.org", "proxy.golang.org", "sum.golang.org"];

export function NetworkPage({ where, store, onRole }: { readonly where: Where; readonly store: Store; readonly onRole: (role: Role) => void }) {
  const project = where.kind === "project" ? where.project : null;
  const own = project ? store.overrides[project] : null;
  const beyond = ROLES.filter((r) => resolve(r, own).internet.value === "any");
  const narrower = ROLES.filter((r) => resolve(r, own).internet.value === "dude");
  const [hosts, setHosts] = useState(ORG_HOSTS);
  const names = (rs: readonly Role[]) => rs.map((r, i) => (
    <span key={r}>{i > 0 ? (i === rs.length - 1 ? " and " : ", ") : null}<TextButton onClick={() => onRole(r)}>{ROLE_LABEL[r]}</TextButton></span>
  ));
  return (
    <>
      <SettingsHeader title="Network" description={`What an agent may reach from inside its container while it works for ${project ?? ORG}: package registries, GitHub, an internal mirror. Everything else is refused. Each project can add to this list.`} />
      <SettingsNote icon="globe">
        {beyond.length ? <>Beyond these lists{project ? ` on ${project}` : ""}: {names(beyond)}, set to <b>Any host</b> in {beyond.length === 1 ? "its" : "their"} Capabilities.</> : <>No agent goes beyond these lists.</>}
        {narrower.length ? <> {names(narrower)} {narrower.length === 1 ? "reaches" : "reach"} only dude.</> : null}
        {beyond.length || narrower.length ? " Every other agent keeps to them." : null}
        {project ? null : (() => {
          // Projects whose own value goes past Acme's: an admin reading Acme's lists should see them too.
          const wide = (Object.keys(store.overrides) as ProjectName[]).flatMap((p) =>
            ROLES.filter((r) => resolve(r, store.overrides[p]).internet.wider && resolve(r, store.overrides[p]).internet.value === "any").map((r) => `${p}’s ${ROLE_LABEL[r]}`));
          return wide.length ? <> On projects, beyond {ORG}’s: {wide.join(", ")}.</> : null;
        })()}
      </SettingsNote>
      <SettingsSection data-testid="network-page">
        <SettingRow className={styles["row"]} label="Agents may reach" help="A hostname, an address or a range. Presets add what a toolchain needs, by name.">
          <HostChips hosts={hosts} placeholder="registry.example.com" label="Add a host agents may reach" onChange={setHosts} />
          <HostPresets presets={PRESETS} has={(h) => hosts.includes(h)} onAdd={(p) => setHosts([...new Set([...hosts, ...p.hosts])])} />
          <span className="networkLabel">Always reachable</span>
          <HostChips readOnly muted hosts={["llm.acme.dev", "dude.acme.dev"]} />
        </SettingRow>
        <SettingRow className={styles["row"]} label="Anywhere" help="Turn the list off for every agent. For a dude on a private network, or while a toolchain is still being worked out.">
          <Switch checked={false} label="Off — only the list above" onCheckedChange={() => undefined} />
        </SettingRow>
        <SettingsNote icon="info">
          A change reaches Runs started after it; a running agent keeps the network it started with. The operator’s own list (<code>DUDE_AGENT_EGRESS</code>) always applies as well.
        </SettingsNote>
      </SettingsSection>
    </>
  );
}
