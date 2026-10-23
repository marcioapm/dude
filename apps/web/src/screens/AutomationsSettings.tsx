/**
 * Automations: an organisation's and each project's work nobody started by
 * hand. The list (filters, on/off from the row, owner, "Also running
 * here" on a project), New automation, and one automation's page — its
 * Settings and Runs tabs, a run, Run now / Test with an event, Transfer.
 *
 * Who may do what is the API's (canEdit, canTransfer); this shows it.
 */

import { useCallback, useEffect, useMemo, useState } from "react";
import {
  ChipsInput,
  KindMark,
  ListSwitch,
  OwnerMark,
  RepoFilterField,
  SchedulePicker,
  ScopeChip,
  Segmented,
  SessionMark,
  SettingRow,
  SettingsHeader,
  SettingsNote,
  SettingsSection,
  SettingSource,
  Switch,
  TextButton,
  TriggerSummary,
  type RepoFilterValue,
} from "@dude/design-system/components";
import { formatTimestamp } from "@dude/design-system";
import {
  Badge,
  Button,
  Callout,
  Dialog,
  EmptyState,
  FormStack,
  Input,
  NumberInput,
  Select,
  Spinner,
  Table,
  TBody,
  Td,
  Th,
  THead,
  Tr,
  Textarea,
} from "@dude/design-system/primitives";
import {
  AUTOMATION_PROMPT_VARIABLES,
  CONCURRENCY,
  CONCURRENCY_LABEL,
  FILTER_LABEL,
  HARNESSES,
  HARNESS_LABEL,
  PRESET_ROLE_LABEL,
  PRESET_ROLES,
  REVIEWER_NEEDS_PULL_REQUEST,
  automationProblem,
  type AgentOverrides,
  type Automation,
  type AutomationInput,
  type AutomationKind,
  type AutomationRun,
  type AutomationRunDetail,
  type AutomationRunsResponse,
  type EventSubscriptions,
  type EventVerdict,
  type Harness,
  type ImageChoice,
  type MachineSizeWithUse,
  type ModelTier,
  type PastDelivery,
  type PresetRole,
  type SettingsResponse,
  type Trigger,
} from "@dude/domain";
import type { ApiClient, Member } from "../api/client.ts";
import { errorText, useSave } from "../hooks/useSave.tsx";

/** Where the automations pages are: a list, one automation (its tab), one run. */
export interface AutomationsPlace {
  readonly id?: string | undefined;
  readonly tab?: "settings" | "runs" | undefined;
  readonly runId?: string | undefined;
}

/** `sub` of a settings place ("aut_1/runs/arn_2") as the pages read it. */
export function automationsPlace(sub: string | undefined): AutomationsPlace {
  const [id, tab, runId] = (sub ?? "").split("/");
  return { id: id || undefined, tab: tab === "runs" ? "runs" : id ? "settings" : undefined, runId: runId || undefined };
}

export function automationsSub(p: AutomationsPlace): string | undefined {
  if (!p.id) return undefined;
  return p.tab === "runs" ? (p.runId ? `${p.id}/runs/${p.runId}` : `${p.id}/runs`) : p.id;
}

const ago = (iso: string | null | undefined) => (iso ? formatTimestamp(iso, "relative") : "—");

/** The kind column's role, when its agent is one of dude's. */
function roleOf(a: Pick<Automation, "agent">): string | undefined {
  return a.agent && "preset" in a.agent ? PRESET_ROLE_LABEL[a.agent.preset.role] : undefined;
}

const RUN_TONE: Record<AutomationRun["status"], "neutral" | "info" | "success" | "danger" | "attention"> = {
  queued: "neutral", running: "info", succeeded: "success", failed: "danger", skipped: "neutral", cancelled: "neutral",
};
const RUN_WORD: Record<AutomationRun["status"], string> = {
  queued: "Queued", running: "Running", succeeded: "Succeeded", failed: "Failed", skipped: "Skipped", cancelled: "Cancelled",
};

function RunStatus({ status }: { status: AutomationRun["status"] }) {
  return <Badge tone={RUN_TONE[status]} size="sm" dot>{RUN_WORD[status]}</Badge>;
}

// ---------------------------------------------------------------------------
// The list
// ---------------------------------------------------------------------------

interface ListProps {
  client: ApiClient;
  orgName: string;
  /** A project's list; null for the organisation's. */
  projectId: string | null;
  /** Whether the viewer may create one here. */
  canCreate: boolean;
  onOpen: (id: string) => void;
  /** An organisation automation from a project's "Also running here". */
  onOpenOrg?: ((id: string) => void) | undefined;
}

type Filters = { q: string; scope: string; trigger: string; kind: string; enabled: string; owner: string };
const ANY = "any";

export function AutomationsList({ client, orgName, projectId, canCreate, onOpen, onOpenOrg }: ListProps) {
  const [list, setList] = useState<Automation[] | null>(null);
  const [here, setHere] = useState<Automation[]>([]);
  const [problem, setProblem] = useState<string | null>(null);
  const [filters, setFilters] = useState<Filters>({ q: "", scope: ANY, trigger: ANY, kind: ANY, enabled: ANY, owner: ANY });
  const [creating, setCreating] = useState(false);
  const load = useCallback(() => {
    if (projectId) {
      client.projectAutomations(projectId).then((r) => { setList(r.automations); setHere(r.alsoRunningHere); setProblem(null); },
        (e: unknown) => setProblem(errorText(e)));
      return;
    }
    const q: Record<string, string> = {};
    for (const [k, v] of Object.entries(filters)) if (v && v !== ANY) q[k] = v;
    client.automations(q).then((r) => { setList(r.automations); setProblem(null); }, (e: unknown) => setProblem(errorText(e)));
  }, [client, projectId, filters]);
  useEffect(load, [load]);
  const toggle = async (a: Automation, on: boolean) => {
    setList((l) => l?.map((x) => (x.id === a.id ? { ...x, enabled: on } : x)) ?? l);
    try {
      await client.setAutomationEnabled(a.id, on);
    } catch (e) {
      setProblem(errorText(e));
      load();
    }
  };
  const set = (k: keyof Filters) => (v: string) => setFilters((f) => ({ ...f, [k]: v }));
  const kinds = useMemo(() => [
    { value: ANY, label: "Any kind" },
    { value: "agent", label: "Agent" },
    { value: "agent:own", label: "Agent (its own)" },
    ...PRESET_ROLES.map((r) => ({ value: `agent:${r}`, label: `Agent (${PRESET_ROLE_LABEL[r]})` })),
    { value: "command", label: "Command" },
    { value: "server", label: "Server" },
    { value: "webhook", label: "Webhook only" },
  ], []);
  return (
    <div className="settingsPage" data-testid="automations">
      <SettingsHeader title="Automations"
        description={projectId
          ? "What runs on its own for this project: on a schedule, or when GitHub says something happened."
          : `Everything that runs on its own in ${orgName}: the organisation's and every project's.`}
        actions={canCreate ? <Button variant="primary" leadingIcon="plus" onClick={() => setCreating(true)} data-testid="new-automation">New automation</Button> : null} />
      {projectId ? null : (
        <div className="automationFilters" data-testid="filter-bar">
          <Input aria-label="Search automations" placeholder="Search" value={filters.q} onChange={(e) => set("q")(e.target.value)} />
          <Select<string> label="Scope" size="sm" value={filters.scope} onValueChange={set("scope")}
            options={[{ value: ANY, label: "Anywhere" }, { value: "organization", label: orgName }, { value: "project", label: "Projects" }]} />
          <Select<string> label="Trigger" size="sm" value={filters.trigger} onValueChange={set("trigger")}
            options={[{ value: ANY, label: "Any trigger" }, { value: "schedule", label: "Schedule" }, { value: "github", label: "GitHub event" }]} />
          <Select<string> label="Kind" size="sm" value={filters.kind} onValueChange={set("kind")} options={kinds} />
          <Select<string> label="On or off" size="sm" value={filters.enabled} onValueChange={set("enabled")}
            options={[{ value: ANY, label: "On and off" }, { value: "true", label: "On" }, { value: "false", label: "Off" }]} />
          <Select<string> label="Owner" size="sm" value={filters.owner} onValueChange={set("owner")} data-testid="owner-filter"
            options={[{ value: ANY, label: "Anyone's" }, { value: "mine", label: "Mine" }]} />
        </div>
      )}
      {problem ? <Callout tone="danger">{problem}</Callout> : null}
      {!list ? <Spinner label="Loading…" /> : list.length === 0 ? (
        <EmptyState data-testid="automations-empty" title="No automations yet"
          description="An automation runs an agent, a command, a server or a webhook on a schedule or when GitHub tells dude something happened." />
      ) : (
        <AutomationTable rows={list} orgName={orgName} onOpen={onOpen} onToggle={toggle} showScope={!projectId} />
      )}
      {projectId && here.length > 0 ? (
        <SettingsSection title={`Also running here, from ${orgName}`} data-testid="also-running">
          <SettingsNote icon="layers">Organisation automations whose repositories include this project's. A project cannot turn them off.</SettingsNote>
          <AutomationTable rows={here} orgName={orgName} onOpen={onOpenOrg ?? onOpen} showScope={false} />
        </SettingsSection>
      ) : null}
      {creating ? <NewAutomationDialog client={client} projectId={projectId} onClose={() => setCreating(false)}
        onCreated={(a) => { setCreating(false); onOpen(a.id); }} /> : null}
    </div>
  );
}

function AutomationTable({ rows, orgName, onOpen, onToggle, showScope }: {
  rows: Automation[];
  orgName: string;
  onOpen: (id: string) => void;
  onToggle?: ((a: Automation, on: boolean) => void) | undefined;
  showScope: boolean;
}) {
  return (
    <Table density="compact">
      <THead>
        <Tr>
          <Th width={56}>On</Th>
          <Th>Name</Th>
          <Th>Kind · session</Th>
          <Th>Trigger</Th>
          <Th hideWhenNarrow>Owner</Th>
          <Th hideWhenNarrow>Last run</Th>
          <Th hideWhenNarrow>Next run</Th>
        </Tr>
      </THead>
      <TBody>
        {rows.map((a) => (
          <Tr key={a.id} interactive onClick={() => onOpen(a.id)} data-testid="automation-row" data-id={a.id}>
            <Td>
              <ListSwitch checked={a.enabled} label={a.name} disabled={!onToggle || !a.canEdit} testId="automation-enabled"
                onCheckedChange={(on) => onToggle?.(a, on)} />
            </Td>
            <Td>
              <span className="automationName">
                <TextButton onClick={(e) => { e.stopPropagation(); onOpen(a.id); }}>{a.name}</TextButton>
                {showScope ? <ScopeChip org={orgName} project={a.projectName} /> : null}
              </span>
            </Td>
            <Td><span className="automationKind"><KindMark kind={a.kind} role={roleOf(a)} /><SessionMark mode={a.sessionMode} expiryDays={a.expiryDays} /></span></Td>
            <Td><TriggerSummary trigger={a.trigger} /></Td>
            <Td hideWhenNarrow><OwnerMark owner={a.owner} left={Boolean(a.ownerLeftAt)} org={orgName} avatarOnly /></Td>
            <Td hideWhenNarrow muted>{a.lastRun ? <span>{RUN_WORD[a.lastRun.status]} · {ago(a.lastRun.at)}</span> : "Never"}</Td>
            <Td hideWhenNarrow muted>{a.trigger.kind === "schedule" && a.enabled && a.nextRunAt ? formatTimestamp(a.nextRunAt, "datetime") : "—"}</Td>
          </Tr>
        ))}
      </TBody>
    </Table>
  );
}

// ---------------------------------------------------------------------------
// New automation
// ---------------------------------------------------------------------------

type What = "agent" | `agent:${PresetRole}` | "command" | "server" | "webhook";

export function NewAutomationDialog({ client, projectId, onClose, onCreated }: {
  client: ApiClient;
  projectId: string | null;
  onClose: () => void;
  onCreated: (a: Automation) => void;
}) {
  const [name, setName] = useState("");
  const [what, setWhat] = useState<What>("agent");
  const [when, setWhen] = useState<"schedule" | "github">("schedule");
  const { busy, problem, save } = useSave();
  const [kind, preset] = what.split(":") as [AutomationKind, PresetRole | undefined];
  const eventsOnly = kind === "server" || preset === "reviewer";
  const tz = Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title="New automation"
      footerProblem={problem}
      footer={<>
        <Button variant="secondary" onClick={onClose}>Cancel</Button>
        <Button variant="primary" disabled={busy || !name.trim()} data-testid="create-automation"
          onClick={() => void save(async () => onCreated(await client.createAutomation({ name: name.trim(), kind, preset: preset ?? null,
            trigger: eventsOnly ? "github" : when, projectId, tz })))}>Create</Button>
      </>}>
      <FormStack>
        <Input label="Name" value={name} autoFocus maxLength={120} onChange={(e) => setName(e.target.value)} placeholder="Nightly QA pass" />
        <Select<What> label="What runs" value={what} onValueChange={setWhat} options={[
          { label: "Agent", options: [{ value: "agent", label: "Its own agent" },
            ...PRESET_ROLES.map((r) => ({ value: `agent:${r}` as What, label: PRESET_ROLE_LABEL[r] }))] },
          { label: "Other", options: [{ value: "command", label: "Command", meta: "runs and exits" },
            { value: "server", label: "Server", meta: "answers events" }, { value: "webhook", label: "Webhook only", meta: "a signed POST" }] },
        ]} />
        <SettingRow label="When it runs" help={eventsOnly ? (kind === "server" ? "A server answers GitHub events." : REVIEWER_NEEDS_PULL_REQUEST) : undefined}>
          <Segmented<"schedule" | "github"> label="When it runs" value={eventsOnly ? "github" : when} onChange={setWhen}
            options={[{ value: "schedule", label: "Schedule", disabled: eventsOnly }, { value: "github", label: "GitHub event" }]} />
        </SettingRow>
        <SettingsNote>It starts off, owned by you. Its settings open next.</SettingsNote>
      </FormStack>
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// One automation
// ---------------------------------------------------------------------------

interface PageProps {
  client: ApiClient;
  orgName: string;
  place: AutomationsPlace;
  onPlace: (p: AutomationsPlace) => void;
  onBack: () => void;
  /** The organisation's members, for Transfer. */
  people: readonly Member[];
}

/** The input an automation's settings form edits, from the automation as read. */
function inputOf(a: Automation): AutomationInput {
  return {
    name: a.name, description: a.description, kind: a.kind, trigger: a.trigger, sessionMode: a.sessionMode, expiryDays: a.expiryDays,
    concurrency: a.concurrency, parallelMax: a.parallelMax, repos: a.repos, repoFilter: a.repoFilter, agent: a.agent,
    machineSizeId: a.machineSizeId, imageId: a.imageId, setupScript: a.setupScript, command: a.command, server: a.server,
    webhook: a.webhook, secrets: a.secrets, notifyOnFailure: a.notifyOnFailure,
  };
}

export function AutomationPage({ client, orgName, place, onPlace, onBack, people }: PageProps) {
  const [a, setA] = useState<Automation | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const [running, setRunning] = useState(false);
  const [transferring, setTransferring] = useState(false);
  const id = place.id!;
  const load = useCallback(() => {
    client.automation(id).then((x) => { setA(x); setProblem(null); }, (e: unknown) => setProblem(errorText(e)));
  }, [client, id]);
  useEffect(load, [load]);
  const runNow = useSave();
  if (!a) return problem ? <Callout tone="danger">{problem}</Callout> : <Spinner label="Loading…" />;
  const tab = place.tab ?? "settings";
  const canRun = a.canEdit || a.canTransfer;
  return (
    <div className="settingsPage" data-testid="automation-page" data-id={a.id}>
      <TextButton onClick={onBack} data-testid="automations-back">Automations</TextButton>
      <SettingsHeader title={a.name}
        description={<span className="automationKind"><KindMark kind={a.kind} role={roleOf(a)} /><TriggerSummary trigger={a.trigger} /></span>}
        actions={<>
          <Switch checked={a.enabled} label={a.enabled ? "On" : "Off"} disabled={!a.canEdit} testId="automation-enabled"
            onCheckedChange={(on) => void client.setAutomationEnabled(a.id, on).then(setA, (e: unknown) => setProblem(errorText(e)))} />
          {canRun ? (
            <Button variant="secondary" leadingIcon="play" data-testid="run-now" disabled={runNow.busy}
              onClick={() => a.trigger.kind === "github" ? setRunning(true)
                : void runNow.save(() => client.runAutomation(a.id, {}), () => onPlace({ id: a.id, tab: "runs" }), "Run started")}>
              Run now
            </Button>
          ) : null}
        </>} />
      {a.ownerLeftAt ? (
        <Callout tone="attention" data-testid="owner-left">
          <b>Owner left — transfer it.</b> {orgName}'s admins own it together until one of them hands it to someone: it stays on, runs act for
          {" "}{orgName}'s admins, and a failure notifies each of them.{" "}
          {a.canTransfer ? <TextButton onClick={() => setTransferring(true)} data-testid="owner-left-transfer">Transfer…</TextButton> : null}
        </Callout>
      ) : null}
      {problem || runNow.problem ? <Callout tone="danger">{problem ?? runNow.problem}</Callout> : null}
      <Segmented<"settings" | "runs"> label="Automation" tabs="automation" value={tab} onChange={(t) => onPlace({ id: a.id, tab: t })}
        options={[{ value: "settings", label: "Settings" }, { value: "runs", label: "Runs" }]} />
      {tab === "settings" ? (
        <AutomationSettings client={client} a={a} orgName={orgName} onSaved={setA} onTransfer={() => setTransferring(true)} />
      ) : place.runId ? (
        <RunDetail client={client} a={a} runId={place.runId} onBack={() => onPlace({ id: a.id, tab: "runs" })} />
      ) : (
        <RunsTab client={client} a={a} onOpenRun={(runId) => onPlace({ id: a.id, tab: "runs", runId })} />
      )}
      {running ? <RunNowDialog client={client} a={a} onClose={() => setRunning(false)}
        onStarted={() => { setRunning(false); onPlace({ id: a.id, tab: "runs" }); }} /> : null}
      {transferring ? <TransferDialog client={client} a={a} orgName={orgName} people={people} onClose={() => setTransferring(false)}
        onDone={(x) => { setTransferring(false); setA(x); }} /> : null}
    </div>
  );
}

function AutomationSettings({ client, a, orgName, onSaved, onTransfer }: {
  client: ApiClient;
  a: Automation;
  orgName: string;
  onSaved: (a: Automation) => void;
  onTransfer: () => void;
}) {
  const [form, setForm] = useState<AutomationInput>(() => inputOf(a));
  const [subs, setSubs] = useState<EventSubscriptions | null>(null);
  const [groups, setGroups] = useState<Array<{ project: string; repos: string[] }>>([]);
  const { busy, problem, save } = useSave();
  useEffect(() => { setForm(inputOf(a)); }, [a]);
  useEffect(() => {
    if (form.trigger.kind === "github") client.eventSubscriptions().then(setSubs, () => setSubs(null));
  }, [client, form.trigger.kind]);
  useEffect(() => {
    if (!a.projectId) client.automationRepositories().then((r) => setGroups(r.groups), () => undefined);
  }, [client, a.projectId]);
  const ro = !a.canEdit;
  const scope = a.projectId ? "project" : "organization";
  const invalid = automationProblem(form, scope);
  const dirty = JSON.stringify(form) !== JSON.stringify(inputOf(a));
  const set = <K extends keyof AutomationInput>(k: K, v: AutomationInput[K]) => setForm((f) => ({ ...f, [k]: v }));
  const trigger = form.trigger;
  const preset = form.agent && "preset" in form.agent ? form.agent : null;
  const own = form.agent && "own" in form.agent ? form.agent.own : null;
  const zones = useMemo(() => {
    try {
      return (Intl as unknown as { supportedValuesOf?: (k: string) => string[] }).supportedValuesOf?.("timeZone") ?? ["UTC"];
    } catch {
      return ["UTC"];
    }
  }, []);
  return (
    <form onSubmit={(e) => { e.preventDefault(); void save(() => client.updateAutomation(a.id, form).then(onSaved), undefined, "Automation saved"); }}>
      <fieldset disabled={ro} className="plainFieldset">
        <SettingsSection title="Trigger">
          <SettingRow label="Runs on">
            <Segmented<"schedule" | "github"> label="Runs on" value={trigger.kind} disabled={ro || a.kind === "server"} data-testid="trigger-kind"
              onChange={(k) => set("trigger", k === "schedule" ? { kind: "schedule", cron: "0 9 * * 1-5", tz: "UTC" }
                : { kind: "github", event: "pull_request", actions: [], filters: { branch: [], label: [], paths: [], author: [] } })}
              options={[{ value: "schedule", label: "Schedule" }, { value: "github", label: "GitHub event" }]} />
          </SettingRow>
          {trigger.kind === "schedule" ? (
            <SettingRow label="Schedule">
              <SchedulePicker cron={trigger.cron} tz={trigger.tz} zones={zones} disabled={ro} onChange={(s) => set("trigger", { kind: "schedule", ...s })} />
            </SettingRow>
          ) : (
            <EventTrigger trigger={trigger} subs={subs} disabled={ro} onChange={(t) => set("trigger", t)} />
          )}
          {a.projectId ? null : (
            <SettingRow label="Repositories" help={trigger.kind === "schedule" ? "What each run checks out." : "Where its events may come from."}>
              <RepoFilterField value={(form.repoFilter ?? { mode: "all", repos: [] }) as RepoFilterValue} org={orgName} groups={groups} disabled={ro}
                onChange={(v) => set("repoFilter", { mode: v.mode, repos: [...v.repos] })} />
            </SettingRow>
          )}
        </SettingsSection>

        {a.kind === "webhook" ? null : (
          <SettingsSection title="Session">
            <SettingRow label="Session" help="Resumable keeps one session, resumed by every run, until it goes unused for its expiry.">
              <Segmented<"ephemeral" | "resumable"> label="Session" value={form.sessionMode} disabled={ro}
                onChange={(m) => setForm((f) => ({ ...f, sessionMode: m, expiryDays: m === "resumable" ? f.expiryDays ?? 7 : null,
                  concurrency: m === "ephemeral" && f.concurrency === "send" ? "queue" : m === "resumable" && f.concurrency === "parallel" ? "queue" : f.concurrency }))}
                options={[{ value: "ephemeral", label: "Ephemeral" }, { value: "resumable", label: "Resumable" }]} />
              {form.sessionMode === "resumable" ? (
                <NumberInput step={1} aria-label="Expiry in days" min={1} max={30} value={form.expiryDays ?? 7} disabled={ro}
                  onValueChange={(n) => set("expiryDays", n ?? 7)} />
              ) : null}
            </SettingRow>
            <SettingRow label="While a run is going" help="What a new event does. A test run always starts its own." data-testid="automation-concurrency">
              <Select<string> label="Concurrency" value={form.concurrency} disabled={ro} onValueChange={(c) => set("concurrency", c as AutomationInput["concurrency"])}
                options={CONCURRENCY.map((c) => ({ value: c, label: CONCURRENCY_LABEL[c],
                  disabled: (c === "send" && !(a.kind === "agent" && form.sessionMode === "resumable")) || (c === "parallel" && form.sessionMode !== "ephemeral") }))} />
              {form.concurrency === "parallel" ? (
                <NumberInput step={1} aria-label="Parallel runs at most" min={2} max={10} value={form.parallelMax} disabled={ro}
                  onValueChange={(n) => set("parallelMax", n ?? 2)} />
              ) : null}
            </SettingRow>
          </SettingsSection>
        )}

        {a.kind === "agent" ? (
          <SettingsSection title="Agent" data-testid="agent-source">
            <SettingRow label="Agent">
              <Segmented<"own" | "preset"> label="Agent" value={preset ? "preset" : "own"} disabled={ro}
                onChange={(s) => set("agent", s === "own" ? { own: { prompt: "" } } : { preset: { role: "qa_browser", overrides: {} }, brief: "" })}
                options={[{ value: "own", label: "Its own agent" }, { value: "preset", label: "One of dude's agents" }]} />
              {preset ? (
                <Select<PresetRole> label="Role" value={preset.preset.role} disabled={ro}
                  onValueChange={(role) => set("agent", { ...preset, preset: { ...preset.preset, role } })}
                  options={PRESET_ROLES.map((r) => ({ value: r, label: PRESET_ROLE_LABEL[r],
                    disabled: r === "reviewer" && !(trigger.kind === "github" && trigger.event.startsWith("pull_request")),
                    meta: r === "reviewer" ? REVIEWER_NEEDS_PULL_REQUEST : undefined }))} />
              ) : null}
            </SettingRow>
            {preset ? (
              <>
                <PresetFields client={client} a={a} orgName={orgName} ro={ro} role={preset.preset.role} overrides={preset.preset.overrides}
                  onChange={(overrides) => set("agent", { ...preset, preset: { ...preset.preset, overrides } })} />
                <SettingRow label="Instructions for this run" block data-testid="role-prompt"
                  help={`${PRESET_ROLE_LABEL[preset.preset.role]} gets its own prompt, as every task gives it, then these instructions as the task brief.`}>
                  <Textarea aria-label="Task brief" rows={8} disabled={ro} value={preset.brief}
                    onChange={(e) => set("agent", { ...preset, brief: e.target.value })} />
                  <VariablesHint />
                </SettingRow>
              </>
            ) : own ? (
              <SettingRow label="Prompt" block data-testid="automation-prompt">
                <Textarea aria-label="Prompt" rows={10} disabled={ro} value={own.prompt}
                  onChange={(e) => set("agent", { own: { ...own, prompt: e.target.value } })} />
                <VariablesHint />
              </SettingRow>
            ) : null}
            <SettingsNote>It can create dude tasks and comment on GitHub as the dude App; it opens no pull requests.</SettingsNote>
          </SettingsSection>
        ) : null}

        {a.kind === "command" ? (
          <SettingsSection title="Command">
            <SettingRow label="Command" block help="A shell line. The event is at $DUDE_EVENT_PATH, with DUDE_EVENT_NAME, DUDE_EVENT_ACTION and DUDE_DELIVERY_ID set; files written to $DUDE_OUTPUT_DIR are kept.">
              <Textarea aria-label="Command" rows={4} className="ds-mono" disabled={ro} value={form.command ?? ""} onChange={(e) => set("command", e.target.value || null)} />
            </SettingRow>
          </SettingsSection>
        ) : null}

        {a.kind === "server" && form.server ? (
          <SettingsSection title="Server">
            <SettingRow label="Command"><Input aria-label="Server command" mono disabled={ro} value={form.server.command}
              onChange={(e) => set("server", { ...form.server!, command: e.target.value })} /></SettingRow>
            <SettingRow label="Port and path">
              <NumberInput step={1} aria-label="Port" min={1} max={65535} disabled={ro} value={form.server.port} onValueChange={(n) => set("server", { ...form.server!, port: n ?? 3000 })} />
              <Input aria-label="Path" mono disabled={ro} value={form.server.path} onChange={(e) => set("server", { ...form.server!, path: e.target.value })} />
            </SettingRow>
            <SettingRow label="Health check" help="GET this path until it answers; empty waits for the port.">
              <Input aria-label="Health path" mono disabled={ro} value={form.server.health ?? ""} placeholder="/healthz"
                onChange={(e) => set("server", { ...form.server!, health: e.target.value || null })} />
            </SettingRow>
            <p className="automationFact" data-testid="server-idle">Stops 30 s after its last request; the next event starts it again.</p>
          </SettingsSection>
        ) : null}

        {a.kind === "webhook" && form.webhook ? (
          <SettingsSection title="Webhook">
            <SettingRow label="URL"><Input aria-label="Webhook URL" mono disabled={ro} value={form.webhook.url} onChange={(e) => set("webhook", { url: e.target.value })} /></SettingRow>
            <SettingRow label="Signing secret" help="X-Dude-Signature-256: sha256= and the HMAC-SHA256 of the body, as GitHub signs.">
              <span className="ds-mono" data-testid="endpoint-preview">{a.webhookSecretHint ? `••••${a.webhookSecretHint}` : "None"}</span>
            </SettingRow>
          </SettingsSection>
        ) : null}

        <SettingsSection title="Notifications">
          <SettingRow label="When a run fails" help={a.ownerLeftAt ? `Every one of ${orgName}'s admins is told.` : "Its owner is told."}>
            <Switch checked={form.notifyOnFailure} label={form.notifyOnFailure ? "Notify" : "Don't notify"} disabled={ro} onCheckedChange={(v) => set("notifyOnFailure", v)} />
          </SettingRow>
        </SettingsSection>
      </fieldset>

      <SettingsSection title="Owner">
        <SettingRow label="Owner" data-testid="owner-row" help={a.canTransfer ? "Runs act for them; failures notify them." : `Only ${orgName}'s admins and the owner transfer it.`}>
          <OwnerMark owner={a.owner} left={Boolean(a.ownerLeftAt)} org={orgName} />
          {a.canTransfer ? <TextButton onClick={onTransfer} data-testid="transfer-owner">Transfer…</TextButton> : null}
        </SettingRow>
      </SettingsSection>

      {ro ? null : (
        <div className="automationSave">
          {invalid ? <Callout tone="attention">{invalid.message}</Callout> : null}
          {problem ? <Callout tone="danger">{problem}</Callout> : null}
          <Button type="submit" variant="primary" disabled={busy || !dirty || Boolean(invalid)}>Save</Button>
        </div>
      )}
    </form>
  );
}

type OverrideKey = keyof AgentOverrides;

/**
 * A preset's harness, model, time limit, machine and image: each shows the
 * role's value as Agents settings resolve it for this scope, with its own
 * override (Reset goes back to the role's).
 */
function PresetFields({ client, a, orgName, ro, role, overrides, onChange }: {
  client: ApiClient;
  a: Automation;
  orgName: string;
  ro: boolean;
  role: PresetRole;
  overrides: AgentOverrides;
  onChange: (o: AgentOverrides) => void;
}) {
  const [settings, setSettings] = useState<SettingsResponse | null>(null);
  const [tiers, setTiers] = useState<readonly ModelTier[]>([]);
  const [sizes, setSizes] = useState<readonly MachineSizeWithUse[]>([]);
  const [images, setImages] = useState<readonly ImageChoice[]>([]);
  useEffect(() => {
    (a.projectId ? client.projectSettings(a.projectId) : client.organizationSettings()).then(setSettings, () => setSettings(null));
    client.modelTiers().then((r) => setTiers(r.tiers), () => undefined);
    client.machineSizes().then((r) => setSizes(r.sizes), () => undefined);
    client.imageChoices().then((r) => setImages(r.images), () => undefined);
  }, [client, a.projectId]);
  const r = settings?.roles[role];
  const scopeName = settings?.project?.name ?? orgName;
  const roleName = PRESET_ROLE_LABEL[role];
  const tierName = (id: string | null | undefined) => tiers.find((t) => t.id === id)?.name ?? (id ? id : "No tier");
  const sizeName = (id: string | null | undefined) => sizes.find((s) => s.id === id)?.name ?? sizes.find((s) => s.isDefault)?.name ?? "Default";
  const imageName = (id: string | null | undefined) => images.find((i) => i.id === id)?.name ?? (id ?? "The project's runtime image");
  const resolved: Record<OverrideKey, string> = {
    harness: r ? HARNESS_LABEL[r.harness.value] : "…",
    tierId: r ? tierName(r.tier.value) : "…",
    timeLimitMinutes: r ? (r.timeLimitMinutes.value ? `${r.timeLimitMinutes.value} min` : "No limit") : "…",
    machineSizeId: r ? sizeName(r.machineSize.value) : "…",
    imageId: r ? imageName(r.image.value) : "…",
  };
  const setOne = <K extends OverrideKey>(k: K, v: AgentOverrides[K] | undefined) => {
    const next = { ...overrides };
    if (v === undefined || v === null || v === "") delete next[k];
    else next[k] = v;
    onChange(next);
  };
  const first: Record<OverrideKey, AgentOverrides[OverrideKey]> = {
    harness: r?.harness.value ?? "opencode",
    tierId: r?.tier.value ?? tiers[0]?.id,
    timeLimitMinutes: r?.timeLimitMinutes.value ?? 120,
    machineSizeId: r?.machineSize.value ?? sizes.find((s) => s.isDefault)?.id ?? sizes[0]?.id,
    imageId: r?.image.value ?? images[0]?.id,
  };
  const control = (k: OverrideKey) => {
    switch (k) {
      case "harness":
        return <Select<string> aria-label="Harness" value={overrides.harness ?? ""} disabled={ro}
          options={HARNESSES.map((h) => ({ value: h, label: HARNESS_LABEL[h] }))} onValueChange={(v) => setOne("harness", v as Harness)} />;
      case "tierId":
        return <Select<string> aria-label="Model" value={overrides.tierId ?? ""} disabled={ro}
          options={tiers.map((t) => ({ value: t.id, label: t.name, meta: t.model ?? "No model" }))} onValueChange={(v) => setOne("tierId", v)} />;
      case "timeLimitMinutes":
        return <NumberInput step={1} aria-label="Time limit in minutes" min={30} max={10_080} disabled={ro} value={overrides.timeLimitMinutes ?? 120}
          onValueChange={(n) => setOne("timeLimitMinutes", n ?? undefined)} />;
      case "machineSizeId":
        return <Select<string> aria-label="Machine" value={overrides.machineSizeId ?? ""} disabled={ro}
          options={sizes.map((s) => ({ value: s.id, label: s.name, meta: `${s.cpus} CPUs` }))} onValueChange={(v) => setOne("machineSizeId", v)} />;
      case "imageId":
        return <Select<string> aria-label="Image" value={overrides.imageId ?? ""} disabled={ro}
          options={images.map((i) => ({ value: i.id, label: i.name }))} onValueChange={(v) => setOne("imageId", v)} />;
    }
  };
  const rows: Array<[OverrideKey, string, string]> = [
    ["harness", "Harness", "The coding agent that runs it."],
    ["tierId", "Model", "The model tier it asks for."],
    ["timeLimitMinutes", "Time limit", "Without progress, in minutes."],
    ["machineSizeId", "Machine", "CPUs · memory · disk."],
    ["imageId", "Image", "What it works in."],
  ];
  const from = `${roleName} · ${scopeName}`;
  return (
    <div data-testid="preset-fields">
      {rows.map(([k, label, help]) => {
        const set = overrides[k] !== undefined;
        return (
          <SettingRow key={k} label={label} help={help} data-testid={`preset-${k}`}
            source={set
              ? <SettingSource source="project" from={roleName} inherited={resolved[k]} onReset={ro ? undefined : () => setOne(k, undefined)} />
              : <SettingSource source="organization" from={from} />}>
            {set ? control(k) : (
              <>
                <span className="automationFact" data-testid={`preset-${k}-value`}>{resolved[k]}</span>
                {ro || first[k] === undefined ? null : <TextButton data-testid={`preset-${k}-override`} onClick={() => setOne(k, first[k])}>Override for this automation</TextButton>}
              </>
            )}
          </SettingRow>
        );
      })}
    </div>
  );
}

function VariablesHint() {
  return (
    <small className="automationHint">
      Variables: {AUTOMATION_PROMPT_VARIABLES.map((v) => <code key={v.name} title={v.description}>{`{{${v.name}}}`} </code>)}
    </small>
  );
}

function EventTrigger({ trigger, subs, disabled, onChange }: {
  trigger: Extract<Trigger, { kind: "github" }>;
  subs: EventSubscriptions | null;
  disabled: boolean;
  onChange: (t: Extract<Trigger, { kind: "github" }>) => void;
}) {
  const events = subs ? [...new Set([...subs.events, trigger.event])] : [trigger.event];
  const filters = trigger.filters;
  const setFilter = (k: keyof typeof filters, v: string[]) => onChange({ ...trigger, filters: { ...filters, [k]: v } });
  return (
    <>
      <SettingRow label="Event" help={subs ? (subs.source === "seen"
        ? "GitHub did not say what it sends dude: these are the events that have arrived."
        : "The events this organisation's GitHub sends dude.") : undefined}>
        <Select<string> label="Event" value={trigger.event} disabled={disabled} options={events.map((e) => ({ value: e, label: e }))}
          onValueChange={(event) => onChange({ ...trigger, event, actions: [] })} />
        {subs?.settingsUrl ? <a href={subs.settingsUrl} target="_blank" rel="noreferrer">Add events in the App's permissions</a> : null}
      </SettingRow>
      <SettingRow label="Actions" help="None means every action.">
        <ChipsInput label="Actions" values={trigger.actions} disabled={disabled} placeholder="opened, synchronize"
          onChange={(actions) => onChange({ ...trigger, actions })} />
      </SettingRow>
      <SettingsSection title="Filters" data-testid="event-filters">
        <SettingRow label="Branch" help="Globs; any one of them."><ChipsInput label="Branch" values={filters.branch} disabled={disabled} onChange={(v) => setFilter("branch", v)} /></SettingRow>
        <SettingRow label="Label"><ChipsInput label="Label" values={filters.label} disabled={disabled} onChange={(v) => setFilter("label", v)} /></SettingRow>
        <SettingRow label="Paths" help="Globs, pull request events only: dude reads the pull request's files when it runs.">
          <ChipsInput label="Paths" values={filters.paths} disabled={disabled} onChange={(v) => setFilter("paths", v)} /></SettingRow>
        <SettingRow label="Author" help="!login leaves someone out."><ChipsInput label="Author" values={filters.author} disabled={disabled} onChange={(v) => setFilter("author", v)} /></SettingRow>
      </SettingsSection>
    </>
  );
}

// ---------------------------------------------------------------------------
// Runs
// ---------------------------------------------------------------------------

function RunsTab({ client, a, onOpenRun }: { client: ApiClient; a: Automation; onOpenRun: (id: string) => void }) {
  const [data, setData] = useState<AutomationRunsResponse | null>(null);
  const [status, setStatus] = useState("all");
  const [problem, setProblem] = useState<string | null>(null);
  const fresh = useSave();
  const load = useCallback(() => {
    client.automationRuns(a.id, status === "all" ? undefined : status).then(setData, (e: unknown) => setProblem(errorText(e)));
  }, [client, a.id, status]);
  useEffect(load, [load]);
  return (
    <div data-testid="automation-runs">
      {data?.session ? (
        <div className="automationSession" data-testid="session-strip">
          <SessionMark mode="resumable" expiryDays={a.expiryDays} />
          <span>Since {ago(data.session.since)} · {data.session.resumes} runs resumed it · last {ago(data.session.lastRunAt)}
            {data.session.expiresAt ? ` · expires ${formatTimestamp(data.session.expiresAt, "datetime")}` : ""}</span>
          {a.canEdit || a.canTransfer ? (
            <Button size="sm" variant="secondary" leadingIcon="retry" disabled={fresh.busy}
              onClick={() => void fresh.save(() => client.startAutomationFresh(a.id), load, "The next run starts fresh")}>Start fresh now</Button>
          ) : null}
        </div>
      ) : null}
      <Select<string> label="Status" size="sm" value={status} onValueChange={setStatus}
        options={[{ value: "all", label: "Every run" }, ...(["queued", "running", "succeeded", "failed", "skipped", "cancelled"] as const).map((s) => ({ value: s, label: RUN_WORD[s] }))]} />
      {problem ?? fresh.problem ? <Callout tone="danger">{problem ?? fresh.problem}</Callout> : null}
      {!data ? <Spinner label="Loading…" /> : data.runs.length === 0 ? <EmptyState title="No runs yet" /> : (
        <Table density="compact">
          <THead><Tr><Th>Status</Th><Th>Why</Th><Th hideWhenNarrow>Session</Th><Th hideWhenNarrow>Cost</Th><Th>When</Th></Tr></THead>
          <TBody>
            {data.runs.map((r) => (
              <Tr key={r.id} interactive onClick={() => onOpenRun(r.id)} data-testid="run-row" data-id={r.id}>
                <Td><RunStatus status={r.status} /></Td>
                <Td>{r.reason ?? (r.event ? `${r.event}${r.action ? ` · ${r.action}` : ""}${r.payloadCount > 1 ? ` · ${r.payloadCount} events` : ""}` : r.source === "schedule" ? "Scheduled" : "Run now")}
                  {r.source === "test" ? <Badge size="sm" tone="info">Test</Badge> : null}</Td>
                <Td hideWhenNarrow muted>{r.session ?? "—"}</Td>
                <Td hideWhenNarrow muted>{r.costUsd !== null ? `$${r.costUsd.toFixed(2)}` : "—"}</Td>
                <Td muted>{ago(r.createdAt)}</Td>
              </Tr>
            ))}
          </TBody>
        </Table>
      )}
    </div>
  );
}

function RunDetail({ client, a, runId, onBack }: { client: ApiClient; a: Automation; runId: string; onBack: () => void }) {
  const [run, setRun] = useState<AutomationRunDetail | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  useEffect(() => { client.automationRun(a.id, runId).then(setRun, (e: unknown) => setProblem(errorText(e))); }, [client, a.id, runId]);
  if (!run) return problem ? <Callout tone="danger">{problem}</Callout> : <Spinner label="Loading…" />;
  return (
    <div data-testid="run-detail">
      <TextButton onClick={onBack}>Runs</TextButton>
      <SettingsSection title={<span><RunStatus status={run.status} /> {ago(run.createdAt)}</span>}>
        {run.reason ? <SettingsNote>{run.reason}</SettingsNote> : null}
        {a.kind === "server" && run.containerStartedAt ? (
          <ol className="automationTimeline" data-testid="server-timeline">
            <li><time>{formatTimestamp(run.containerStartedAt, "time")}</time> Started</li>
            {run.requests.map((q) => (
              <li key={q.id}><time>{formatTimestamp(q.at, "time")}</time> POST {q.httpStatus ?? q.error ?? "no answer"}{q.try > 1 ? " (retried)" : ""}</li>
            ))}
            {run.containerStoppedAt ? <li><time>{formatTimestamp(run.containerStoppedAt, "time")}</time> {run.stopReason === "idle" ? "Idle 30 s · stopped" : "Stopped"}</li> : null}
          </ol>
        ) : null}
        {run.requests.length > 0 ? (
          <Table density="compact">
            <THead><Tr><Th>Delivery</Th><Th>Try</Th><Th>Answer</Th><Th>ms</Th></Tr></THead>
            <TBody>{run.requests.map((q) => (
              <Tr key={q.id}><Td mono>{q.deliveryId ?? "—"}</Td><Td>{q.try}</Td><Td>{q.httpStatus ?? q.error}</Td><Td>{q.ms ?? "—"}</Td></Tr>
            ))}</TBody>
          </Table>
        ) : null}
      </SettingsSection>
      <SettingsSection title="Trigger payload">
        {run.payloadsPurged ? <SettingsNote>Payloads are kept 30 days.</SettingsNote>
          : <pre className="automationPayload">{JSON.stringify(run.payloads.length === 1 ? run.payloads[0]!.body : run.payloads, null, 2)}</pre>}
      </SettingsSection>
      {run.logTail ? <SettingsSection title="Log"><pre className="automationPayload">{run.logTail}</pre></SettingsSection> : null}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Run now on an event, and Transfer
// ---------------------------------------------------------------------------

/**
 * A JSON body's validity: its value, or where and why it does not parse
 * (1-based). The place is found by scanning, not read from the parser's
 * message: only V8 says where (JavaScriptCore, Safari's, does not).
 */
export function checkJson(text: string): { ok: true; value: unknown } | { ok: false; line: number; column: number; message: string } {
  try {
    return { ok: true, value: JSON.parse(text) };
  } catch {
    const { at, message } = jsonError(text);
    const before = text.slice(0, at).split("\n");
    return { ok: false, line: before.length, column: before[before.length - 1]!.length + 1, message };
  }
}

/** Where a JSON text first breaks, and what was expected there. */
function jsonError(s: string): { at: number; message: string } {
  let i = 0;
  const ws = () => { while (i < s.length && " \t\n\r".includes(s[i]!)) i++; };
  const fail = (message: string): never => { throw { at: i, message }; };
  const str = () => {
    i++;
    while (i < s.length && s[i] !== '"') {
      if (s[i] === "\\") i++;
      else if (s[i] === "\n") fail("Unterminated string");
      i++;
    }
    if (i >= s.length) fail("Unterminated string");
    i++;
  };
  const value = (): void => {
    ws();
    const c = s[i];
    if (c === "{") {
      i++; ws();
      if (s[i] === "}") { i++; return; }
      for (;;) {
        ws();
        if (s[i] !== '"') fail("Expected a property name in double quotes");
        str(); ws();
        if (s[i] !== ":") fail("Expected ':' after property name");
        i++; value(); ws();
        if (s[i] === ",") { i++; continue; }
        if (s[i] === "}") { i++; return; }
        fail("Expected ',' or '}' after property value");
      }
    }
    if (c === "[") {
      i++; ws();
      if (s[i] === "]") { i++; return; }
      for (;;) {
        value(); ws();
        if (s[i] === ",") { i++; continue; }
        if (s[i] === "]") { i++; return; }
        fail("Expected ',' or ']' after array element");
      }
    }
    if (c === '"') return str();
    const lit = /^(-?(0|[1-9]\d*)(\.\d+)?([eE][+-]?\d+)?|true|false|null)/.exec(s.slice(i));
    if (!lit) fail(i >= s.length ? "Unexpected end of JSON" : "Unexpected token");
    i += lit![0].length;
  };
  try {
    value(); ws();
    if (i < s.length) fail("Unexpected text after the JSON value");
    return { at: s.length, message: "Not valid JSON" };
  } catch (e) {
    return e as { at: number; message: string };
  }
}

/** A test event's body, GitHub's common format at its least. */
export function testTemplate(event: string, action: string | null): Record<string, unknown> {
  const base: Record<string, unknown> = { ...(action ? { action } : {}), repository: { full_name: "acme/web", default_branch: "main" },
    sender: { login: "you" }, organization: { login: "acme" }, installation: { id: 1 } };
  if (event.startsWith("pull_request")) {
    base["pull_request"] = { number: 1, title: "A test pull request", head: { ref: "feature" }, base: { ref: "main" }, user: { login: "you" }, labels: [], updated_at: new Date().toISOString() };
  }
  if (event === "issue_comment") base["comment"] = { id: 1, body: "A test comment", user: { login: "you" }, created_at: new Date().toISOString() };
  if (event === "check_suite") base["check_suite"] = { id: 1, head_branch: "main", head_sha: "0".repeat(40), status: "completed", conclusion: "success" };
  if (event === "check_run") base["check_run"] = { id: 1, name: "test", status: "completed", conclusion: "success", head_sha: "0".repeat(40), check_suite: { head_branch: "main" } };
  if (event === "push") base["ref"] = "refs/heads/main";
  return base;
}

function RunNowDialog({ client, a, onClose, onStarted }: { client: ApiClient; a: Automation; onClose: () => void; onStarted: () => void }) {
  const trigger = a.trigger as Extract<Trigger, { kind: "github" }>;
  const [mode, setMode] = useState<"past" | "write">("past");
  const [deliveries, setDeliveries] = useState<PastDelivery[] | null>(null);
  const [picked, setPicked] = useState<string | null>(null);
  const [action, setAction] = useState<string | null>(trigger.actions[0] ?? null);
  const [deliveryId, setDeliveryId] = useState(() => `test-${crypto.randomUUID()}`);
  const [text, setText] = useState(() => JSON.stringify(testTemplate(trigger.event, trigger.actions[0] ?? null), null, 2));
  const [verdict, setVerdict] = useState<EventVerdict | null>(null);
  const { busy, problem, save } = useSave();
  useEffect(() => { client.automationDeliveries(a.id).then((r) => setDeliveries(r.deliveries), () => setDeliveries([])); }, [client, a.id]);
  const json = checkJson(text);
  const objectBody = json.ok && json.value && typeof json.value === "object" && !Array.isArray(json.value);
  useEffect(() => {
    if (mode !== "write" || !objectBody) { setVerdict(null); return; }
    const t = setTimeout(() => client.evaluateTestEvent(a.id, (json as { value: Record<string, unknown> }).value).then(setVerdict, () => setVerdict(null)), 250);
    return () => clearTimeout(t);
  }, [client, a.id, mode, text, objectBody]); // eslint-disable-line react-hooks/exhaustive-deps
  const canRun = mode === "past" ? Boolean(picked) : Boolean(objectBody && verdict?.matches);
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title="Run now on an event" size="lg" footerProblem={problem}
      footer={<>
        <Button variant="secondary" onClick={onClose}>Cancel</Button>
        <Button variant="primary" disabled={busy || !canRun} data-testid="test-run"
          onClick={() => void save(() => client.runAutomation(a.id, mode === "past" ? { deliveryId: picked! }
            : { testEvent: { event: trigger.event, action, deliveryId, body: (json as { value: Record<string, unknown> }).value } }), onStarted, "Run started")}>
          {mode === "past" ? "Run on this delivery" : "Run with this event"}
        </Button>
      </>}>
      <FormStack>
        <Segmented<"past" | "write"> label="Event" value={mode} onChange={setMode} data-testid="test-mode"
          options={[{ value: "past", label: "A past delivery" }, { value: "write", label: "Write a test event" }]} />
        {mode === "past" ? (
          !deliveries ? <Spinner label="Loading…" /> : deliveries.length === 0 ? <EmptyState title={`No ${trigger.event} deliveries in the last 30 days`} /> : (
            <Table density="compact" data-testid="test-delivery">
              <THead><Tr><Th>Delivery</Th><Th>Action</Th><Th>Repository</Th><Th>By</Th><Th>When</Th></Tr></THead>
              <TBody>{deliveries.map((d) => (
                <Tr key={d.id} interactive selected={picked === d.id} onClick={() => setPicked(d.id)}>
                  <Td mono>{d.id.slice(0, 12)}</Td><Td>{d.action ?? "—"}</Td><Td>{d.repository ?? "—"}</Td><Td>{d.sender ?? "—"}</Td><Td muted>{ago(d.receivedAt)}</Td>
                </Tr>
              ))}</TBody>
            </Table>
          )
        ) : (
          <>
            <Input label="X-GitHub-Event" value={trigger.event} readOnly mono />
            <Select<string> label="Action" value={action ?? ""} onValueChange={(v) => {
              setAction(v || null);
              const parsed = checkJson(text);
              if (parsed.ok && parsed.value && typeof parsed.value === "object") setText(JSON.stringify({ ...(parsed.value as object), action: v }, null, 2));
            }} options={(trigger.actions.length ? trigger.actions : ["opened", "closed", "edited", "synchronize", "created", "completed", "submitted"]).map((x) => ({ value: x, label: x }))} />
            <Input label="X-GitHub-Delivery" value={deliveryId} mono onChange={(e) => setDeliveryId(e.target.value)} />
            <Textarea aria-label="Event body" className="ds-mono" rows={14} value={text} onChange={(e) => setText(e.target.value)} data-testid="test-editor" />
            <TextButton onClick={() => setText(JSON.stringify(testTemplate(trigger.event, action), null, 2))} data-testid="reset-template">Reset to template</TextButton>
            <span data-testid="test-checks" role="status">
              {json.ok ? (objectBody ? "Valid JSON." : "The body is a JSON object.") : `Not valid JSON, line ${json.line}, column ${json.column}: ${json.message}`}
              {" "}
              {!objectBody ? "Filters are checked once the JSON is valid."
                : !verdict ? "Checking the filters…"
                : verdict.matches ? <>Filters are checked against this event: <b>it matches.</b></>
                : <><b>It would be skipped because of {FILTER_LABEL[verdict.filter]}</b>: {verdict.because}</>}
            </span>
          </>
        )}
        <SettingsNote>A test run starts its own run, with a fresh session, whatever is running. It is a real run: it can comment and create tasks.</SettingsNote>
      </FormStack>
    </Dialog>
  );
}

function TransferDialog({ client, a, orgName, people, onClose, onDone }: {
  client: ApiClient;
  a: Automation;
  orgName: string;
  people: readonly Member[];
  onClose: () => void;
  onDone: (a: Automation) => void;
}) {
  const [to, setTo] = useState<string>("");
  const { busy, problem, save } = useSave();
  const who = people.find((p) => p.id === to);
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={`Transfer ${a.name}`} size="sm" footerProblem={problem}
      footer={<>
        <Button variant="secondary" onClick={onClose}>Cancel</Button>
        <Button variant="primary" disabled={busy || !to} data-testid="transfer-confirm"
          onClick={() => void save(() => client.transferAutomation(a.id, to).then(onDone), undefined, "Owner changed")}>Transfer</Button>
      </>}>
      <FormStack>
        <Select<string> label="New owner" value={to} onValueChange={setTo} placeholder={`One of ${orgName}'s members`}
          options={people.filter((p) => p.id !== a.owner?.id).map((p) => ({ value: p.id, label: p.name, meta: p.role === "admin" ? "Admin" : "Member" }))} />
        {who ? <SettingsNote>Runs from now on act for {who.name}; failures notify them.</SettingsNote> : null}
      </FormStack>
    </Dialog>
  );
}
