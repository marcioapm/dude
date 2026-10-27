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
  Breadcrumb,
  ChipsInput,
  CodeEditor,
  KindMark,
  ListSwitch,
  MarkdownDocument,
  NextRuns,
  OwnerMark,
  PromptHistory,
  RepoFilterField,
  SchedulePicker,
  ScopeChip,
  PresetSecrets,
  SecretPicker,
  Segmented,
  SessionMark,
  SettingField,
  SettingFields,
  SettingRow,
  SettingsDisclosure,
  SettingsHeader,
  SettingsMeta,
  SettingsNote,
  SettingsSection,
  SettingSource,
  SizeMeter,
  StatusMark,
  Switch,
  TextButton,
  TriggerSummary,
  type RepoFilterValue,
} from "@dude/design-system/components";
import { formatDuration, formatTimestamp } from "@dude/design-system";
import {
  Badge,
  Button,
  Callout,
  Checkbox,
  ChoiceList,
  Dialog,
  EmptyState,
  Fieldset,
  FormStack,
  IconButton,
  Input,
  NumberInput,
  RowMenu,
  SearchField,
  Select,
  Spinner,
  Tab,
  TabList,
  TabPanel,
  Table,
  Tabs,
  TBody,
  Td,
  Th,
  THead,
  Tr,
  Textarea,
  KeyValueList,
  type ChoiceOption,
} from "@dude/design-system/primitives";
import {
  AUTOMATION_KIND_LABEL,
  AUTOMATION_PROMPT_VARIABLES,
  DEFAULT_VERIFY_OF,
  INCOMING_PROMPT_VARIABLES,
  INCOMING_RATE_DEFAULT,
  AUTOMATION_POST_RETRY,
  COMMAND_MAX_BYTES,
  ORG_AUTOMATION_PICKS,
  PRESET_SECRET_RECIPIENT,
  PRESET_SECRETS,
  CONCURRENCY,
  CONCURRENCY_LABEL,
  EVENT_ACTIONS,
  FILTER_LABEL,
  HARNESSES,
  HARNESS_LABEL,
  PRESET_ROLE_LABEL,
  PRESET_ROLES,
  REVIEWER_NEEDS_PULL_REQUEST,
  automationProblem,
  isTimeZone,
  parseCron,
  secretPicksInForce,
  type AgentOverrides,
  type Automation,
  type AutomationInput,
  type AutomationKind,
  type AutomationPromptVersion,
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
  type OrgSecret,
  type ProjectSecret,
  type SecretRef,
  type SettingsResponse,
  type Trigger,
  type AgentRole,
} from "@dude/domain";
import type { ApiClient, Member } from "../api/client.ts";
import { errorText, useSave } from "../hooks/useSave.tsx";
import { AutomationToolList } from "./automationTools.tsx";
import { AutomationFeed, CommandLog, ContainerLog, EV_LOG_EXITED, EV_LOG_LINE, LiveMark, RunNowLine, activityCount, useNow, useRunActivity } from "./automationFeed.tsx";
import { useVisibleInterval } from "../hooks/useVisibleInterval.ts";
import { useReloadOnEvents } from "../hooks/useEventStream.ts";
import { project } from "../api/conversation.ts";
import { useOrgSecrets } from "./OrgSecrets.tsx";
import { DeliveriesTab, DeliveryDetail, holdSecret, IncomingTriggerLine, IncomingTriggerRows, ProjectsRow, RotateDialog, RunNowIncomingDialog,
  SecretOnce, takeSecret } from "./automationIncoming.tsx";

/** Where the automations pages are: a list, one automation (its tab), one run or one delivery. */
export interface AutomationsPlace {
  readonly id?: string | undefined;
  readonly tab?: "settings" | "runs" | "deliveries" | undefined;
  readonly runId?: string | undefined;
  readonly deliveryId?: string | undefined;
}

/** `sub` of a settings place ("aut_1/runs/arn_2", "aut_1/deliveries/dlv_3") as the pages read it. */
export function automationsPlace(sub: string | undefined): AutomationsPlace {
  const [id, tab, item] = (sub ?? "").split("/");
  if (tab === "deliveries") return { id, tab: "deliveries", deliveryId: item || undefined };
  return { id: id || undefined, tab: tab === "runs" ? "runs" : id ? "settings" : undefined, runId: item || undefined };
}

export function automationsSub(p: AutomationsPlace): string | undefined {
  if (!p.id) return undefined;
  if (p.tab === "deliveries") return p.deliveryId ? `${p.id}/deliveries/${p.deliveryId}` : `${p.id}/deliveries`;
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

/** The list's glyph for a run's status, in the status vocabulary's shapes. */
const RUN_MARK: Record<AutomationRun["status"], "pending" | "running" | "completed" | "failed" | "aborted"> = {
  queued: "pending", running: "running", succeeded: "completed", failed: "failed", skipped: "aborted", cancelled: "aborted",
};

const NEXT_RUN = new Intl.DateTimeFormat("en-GB", { weekday: "short", day: "numeric", month: "short", hour: "2-digit", minute: "2-digit", hour12: false });

/** "Sat 24 Oct 02:00" for a schedule that is on; "On event" or "Off" otherwise. */
export function nextRun(a: Pick<Automation, "enabled" | "trigger" | "nextRunAt">): string {
  if (!a.enabled) return "Off";
  if (a.trigger.kind === "github") return "On event";
  if (a.trigger.kind === "incoming") return "On delivery";
  if (!a.nextRunAt) return "—";
  const parts = Object.fromEntries(NEXT_RUN.formatToParts(new Date(a.nextRunAt)).map((p) => [p.type, p.value]));
  return `${parts["weekday"]} ${parts["day"]} ${parts["month"]} ${parts["hour"]}:${parts["minute"]}`;
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
    { label: "Any", options: [{ value: ANY, label: "All kinds" }] },
    { label: "Agent", options: [
      { value: "agent", label: "Any agent" },
      { value: "agent:own", label: "Its own agent" },
      ...PRESET_ROLES.map((r) => ({ value: `agent:${r}`, label: PRESET_ROLE_LABEL[r] })),
    ] },
    { label: "Runs a program", options: [
      { value: "command", label: "Command" },
      { value: "server", label: "Server" },
      { value: "webhook", label: AUTOMATION_KIND_LABEL.webhook },
    ] },
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
          <SearchField label="Search automations" placeholder="Search automations" value={filters.q} onValueChange={set("q")} />
          <Select<string> aria-label="Scope" size="sm" value={filters.scope} onValueChange={set("scope")}
            options={[{ value: ANY, label: "All scopes" }, { value: "organization", label: orgName }, { value: "project", label: "Projects" }]} />
          <Select<string> aria-label="Trigger" size="sm" value={filters.trigger} onValueChange={set("trigger")}
            options={[{ value: ANY, label: "All triggers" }, { value: "schedule", label: "Schedule" }, { value: "github", label: "GitHub event" },
              { value: "incoming", label: "Incoming webhook" }]} />
          <Select<string> aria-label="Kind" size="sm" value={filters.kind} onValueChange={set("kind")} options={kinds} />
          <Select<string> aria-label="On or off" size="sm" value={filters.enabled} onValueChange={set("enabled")}
            options={[{ value: ANY, label: "On and off" }, { value: "true", label: "On" }, { value: "false", label: "Off" }]} />
          <Select<string> aria-label="Owner" size="sm" value={filters.owner} onValueChange={set("owner")} data-testid="owner-filter"
            options={[{ value: ANY, label: "Anyone's" }, { value: "mine", label: "Mine" }]} />
        </div>
      )}
      {problem ? <Callout tone="danger">{problem}</Callout> : null}
      {!list ? <Spinner label="Loading…" /> : list.length === 0 ? (
        <EmptyState data-testid="automations-empty" title="No automations yet"
          description="An automation runs an agent, a command, a server or a webhook on a schedule or when GitHub tells dude something happened." />
      ) : (
        <>
          <p className="automationCount" data-testid="automations-count">{list.length === 1 ? "1 automation" : `${list.length} automations`}</p>
          <AutomationTable rows={list} orgName={orgName} onOpen={onOpen} onToggle={toggle} showScope={!projectId} />
        </>
      )}
      {projectId && here.length > 0 ? (
        <SettingsSection title={`Also running here, from ${orgName}`} data-testid="also-running">
          <SettingsNote icon="layers">Organisation automations whose repositories include this project's. A project cannot turn them off.</SettingsNote>
          <AutomationTable rows={here} orgName={orgName} onOpen={onOpenOrg ?? onOpen} showScope={false} />
        </SettingsSection>
      ) : null}
      {creating ? <NewAutomationDialog client={client} projectId={projectId} onClose={() => setCreating(false)}
        onCreated={(a) => { setCreating(false); holdSecret(a); onOpen(a.id); }} /> : null}
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
          <Th width="19%">Name</Th>
          <Th width="15%">Kind · session</Th>
          <Th>Trigger</Th>
          <Th hideWhenNarrow width={64}>Owner</Th>
          <Th hideWhenNarrow width="11%">Last run</Th>
          <Th hideWhenNarrow width="11%">Next run</Th>
        </Tr>
      </THead>
      <TBody>
        {rows.map((a) => (
          <Tr key={a.id} interactive onClick={() => onOpen(a.id)} data-testid="automation-row" data-id={a.id}>
            <Td>
              <ListSwitch checked={a.enabled} label={a.name} disabled={!onToggle || !a.canEdit} testId="automation-enabled"
                onCheckedChange={(on) => onToggle?.(a, on)} />
            </Td>
            <Td wrap>
              <span className="automationName">
                <TextButton onClick={(e) => { e.stopPropagation(); onOpen(a.id); }}>{a.name}</TextButton>
                {showScope ? <ScopeChip org={orgName} project={a.projectName} projectId={a.projectId} /> : null}
              </span>
            </Td>
            <Td wrap><span className="automationKind" data-stack="true"><KindMark kind={a.kind} role={roleOf(a)} /><SessionMark mode={a.sessionMode} expiryDays={a.expiryDays} /></span></Td>
            <Td wrap><TriggerSummary trigger={a.trigger} wrap /></Td>
            <Td hideWhenNarrow><OwnerMark owner={a.owner} left={Boolean(a.ownerLeftAt)} org={orgName} avatarOnly /></Td>
            <Td hideWhenNarrow muted wrap>{a.lastRun
              ? <span className="automationLastRun"><StatusMark status={RUN_MARK[a.lastRun.status]} size="sm" iconOnly label={RUN_WORD[a.lastRun.status]} />
                {a.lastRun.status === "succeeded" ? "" : `${RUN_WORD[a.lastRun.status]} `}{ago(a.lastRun.at)}</span>
              : "Never"}</Td>
            <Td hideWhenNarrow muted wrap>{nextRun(a)}</Td>
          </Tr>
        ))}
      </TBody>
    </Table>
  );
}

// ---------------------------------------------------------------------------
// New automation
// ---------------------------------------------------------------------------

const NEW_KINDS: ReadonlyArray<ChoiceOption<AutomationKind>> = [
  { value: "agent", label: "Agent", icon: "agent", description: "A coding agent with a prompt." },
  { value: "command", label: "Command", icon: "terminal", description: "A command that runs in a container and exits." },
  { value: "server", label: "Server", icon: "globe", description: "A server that answers GitHub events." },
  { value: "webhook", label: AUTOMATION_KIND_LABEL.webhook, icon: "send", description: "No machine: dude sends a signed POST to a URL you give." },
];

export function NewAutomationDialog({ client, projectId, onClose, onCreated }: {
  client: ApiClient;
  projectId: string | null;
  onClose: () => void;
  onCreated: (a: Automation & { incomingSecret?: string }) => void;
}) {
  const [name, setName] = useState("");
  const [kind, setKind] = useState<AutomationKind>("agent");
  const [agentAs, setAgentAs] = useState<"own" | PresetRole>("own");
  const [when, setWhen] = useState<"schedule" | "github" | "incoming">("schedule");
  const { busy, problem, save } = useSave();
  const preset = kind === "agent" && agentAs !== "own" ? agentAs : undefined;
  const eventsOnly = kind === "server" || preset === "reviewer";
  const tz = Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  const trigger = eventsOnly ? "github" : when;
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title="New automation"
      footerProblem={problem}
      footer={<>
        <Button variant="secondary" onClick={onClose}>Cancel</Button>
        <Button variant="primary" disabled={busy || !name.trim()} data-testid="create-automation"
          onClick={() => void save(async () => onCreated(await client.createAutomation({ name: name.trim(), kind, preset: preset ?? null,
            trigger, projectId, tz })))}>Create</Button>
      </>}>
      <FormStack>
        <Input label="Name" value={name} autoFocus maxLength={120} onChange={(e) => setName(e.target.value)} placeholder="Nightly QA pass" />
        <Fieldset legend="What runs">
          <ChoiceList<AutomationKind> label="What runs" value={kind} onChange={setKind} options={NEW_KINDS} data-testid="new-automation-kind" />
        </Fieldset>
        {kind === "agent" ? (
          <Select<"own" | PresetRole> label="Agent" value={agentAs} onValueChange={setAgentAs}
            options={[{ value: "own", label: "Its own agent" }, ...PRESET_ROLES.map((r) => ({ value: r, label: PRESET_ROLE_LABEL[r] }))]} />
        ) : null}
        <Fieldset legend="When it runs"
          hint={eventsOnly ? (kind === "server" ? "A server answers GitHub events." : REVIEWER_NEEDS_PULL_REQUEST)
            : when === "incoming" ? "Its own URL: anything that can POST JSON starts it. The URL and secret are on its settings." : undefined}>
          <Segmented<"schedule" | "github" | "incoming"> label="When it runs" value={trigger} onChange={setWhen}
            data-testid="new-automation-trigger"
            options={[{ value: "schedule", label: "Schedule", disabled: eventsOnly }, { value: "github", label: "GitHub event" },
              { value: "incoming", label: "Incoming webhook", disabled: eventsOnly }]} />
        </Fieldset>
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
    webhook: a.webhook, secrets: a.secrets, notifyOnFailure: a.notifyOnFailure, projectIds: a.projectIds ?? [],
  };
}

export function AutomationPage({ client, orgName, place, onPlace, onBack, people }: PageProps) {
  const [a, setA] = useState<Automation | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const [running, setRunning] = useState(false);
  const [transferring, setTransferring] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const duplicate = useSave();
  const id = place.id!;
  // An incoming webhook's secret, shown once: after Create, a save that made it, or Rotate.
  const [secret, setSecret] = useState<{ value: string; rotated: boolean } | null>(() => {
    const held = takeSecret(id);
    return held ? { value: held, rotated: false } : null;
  });
  const [rotating, setRotating] = useState(false);
  const load = useCallback(() => {
    client.automation(id).then((x) => { setA(x); setProblem(null); }, (e: unknown) => setProblem(errorText(e)));
  }, [client, id]);
  useEffect(load, [load]);
  const runNow = useSave();
  if (!a) return problem ? <Callout tone="danger">{problem}</Callout> : <Spinner label="Loading…" />;
  const tab = place.tab ?? "settings";
  const canRun = a.canEdit || a.canTransfer;
  const hasDeliveries = a.trigger.kind !== "schedule";
  return (
    <div className="settingsPage" data-testid="automation-page" data-id={a.id}>
      <Breadcrumb size="sm" data-testid="automation-breadcrumb" items={[
        { id: "automations", label: "Automations", onSelect: onBack },
        ...(tab === "runs" && place.runId
          ? [{ id: "name", label: a.name, onSelect: () => onPlace({ id: a.id }) },
             { id: "runs", label: "Runs", onSelect: () => onPlace({ id: a.id, tab: "runs" }) },
             { id: "run", label: "Run", mono: true }]
          : tab === "deliveries" && place.deliveryId
          ? [{ id: "name", label: a.name, onSelect: () => onPlace({ id: a.id }) },
             { id: "deliveries", label: "Deliveries", onSelect: () => onPlace({ id: a.id, tab: "deliveries" }) },
             { id: "delivery", label: place.deliveryId, mono: true }]
          : [{ id: "name", label: a.name }]),
      ]} />
      <SettingsHeader title={a.name}
        description={<span className="automationKind"><KindMark kind={a.kind} role={roleOf(a)} /><TriggerSummary trigger={a.trigger} wrap={a.trigger.kind === "incoming"} /></span>}
        actions={<>
          <Switch checked={a.enabled} label={a.enabled ? "On" : "Off"} disabled={!a.canEdit} testId="automation-enabled"
            onCheckedChange={(on) => void client.setAutomationEnabled(a.id, on).then(setA, (e: unknown) => setProblem(errorText(e)))} />
          {canRun ? (
            <Button variant="secondary" leadingIcon="play" data-testid="run-now" disabled={runNow.busy}
              onClick={() => a.trigger.kind !== "schedule" ? setRunning(true)
                 : void runNow.save(async () => {
                   const started = await client.runAutomation(a.id, {});
                   onPlace({ id: a.id, tab: "runs", runId: started.runId });
                 }, undefined, "Run started")}>
              Run now
            </Button>
          ) : null}
          <RowMenu label={`More for ${a.name}`} align="end"
            trigger={<IconButton icon="more" label={`More for ${a.name}`} data-testid="automation-more" />}
            items={[
              { id: "duplicate", label: "Duplicate", icon: "copy", disabled: !a.canEdit,
                onSelect: () => void duplicate.save(async () => { const copy = await client.duplicateAutomation(a.id); holdSecret(copy); onPlace({ id: copy.id }); }, undefined, "Duplicated: it starts off") },
              { kind: "separator" },
              { id: "delete", label: "Delete…", icon: "archive", tone: "danger", disabled: !a.canEdit, onSelect: () => setDeleting(true) },
            ]} />
        </>} />
      {a.ownerLeftAt ? (
        <Callout tone="attention" data-testid="owner-left">
          <b>Owner left — transfer it.</b> {orgName}'s admins own it together until one of them hands it to someone: it stays on, runs act for
          {" "}{orgName}'s admins, and a failure notifies each of them.{" "}
          {a.canTransfer ? <TextButton onClick={() => setTransferring(true)} data-testid="owner-left-transfer">Transfer…</TextButton> : null}
        </Callout>
      ) : null}
      {problem || runNow.problem || duplicate.problem ? <Callout tone="danger">{problem ?? runNow.problem ?? duplicate.problem}</Callout> : null}
      <Tabs value={tab} onValueChange={(t) => onPlace({ id: a.id, tab: t as "settings" | "runs" | "deliveries" })}>
        <TabList aria-label="Automation">
          <Tab value="settings">Settings</Tab>
          <Tab value="runs">Runs</Tab>
          {hasDeliveries ? <Tab value="deliveries">Deliveries</Tab> : null}
        </TabList>
        <TabPanel value="settings">
          <AutomationSettings key={a.id} client={client} a={a} orgName={orgName} onTransfer={() => setTransferring(true)} onTry={() => setRunning(true)}
            onSaved={(x) => { setA(x); if (x.incomingSecret) setSecret({ value: x.incomingSecret, rotated: false }); }}
            onRotate={() => setRotating(true)} />
        </TabPanel>
        <TabPanel value="runs">
          {place.runId ? <RunDetail client={client} a={a} runId={place.runId} onDelivery={(d) => onPlace({ id: a.id, tab: "deliveries", deliveryId: d })} />
            : <RunsTab client={client} a={a} onOpenRun={(runId) => onPlace({ id: a.id, tab: "runs", runId })} />}
        </TabPanel>
        {hasDeliveries ? (
          <TabPanel value="deliveries">
            {tab !== "deliveries" ? null : place.deliveryId
              ? <DeliveryDetail client={client} a={a} id={place.deliveryId} onRun={(runId) => onPlace({ id: a.id, tab: "runs", runId })}
                onRunWith={() => setRunning(true)} />
              : <DeliveriesTab client={client} a={a} onOpen={(d) => onPlace({ id: a.id, tab: "deliveries", deliveryId: d })}
                onRun={(runId) => onPlace({ id: a.id, tab: "runs", runId })} />}
          </TabPanel>
        ) : null}
      </Tabs>
      {running && a.trigger.kind === "incoming" ? <RunNowIncomingDialog client={client} a={a} picked={place.deliveryId} checkJson={checkJson}
        onClose={() => setRunning(false)} onStarted={(runId) => { setRunning(false); onPlace({ id: a.id, tab: "runs", runId }); }} /> : null}
      {running && a.trigger.kind === "github" ? <RunNowDialog client={client} a={a} onClose={() => setRunning(false)}
        onStarted={(runId) => { setRunning(false); onPlace({ id: a.id, tab: "runs", runId }); }} /> : null}
      {rotating ? <RotateDialog client={client} a={a} onClose={() => setRotating(false)}
        onRotated={(x, value) => { setRotating(false); setA(x); setSecret({ value, rotated: true }); }} /> : null}
      {secret && a.trigger.kind === "incoming" ? <SecretOnce secret={secret.value} trigger={a.trigger} origin={a.publicOrigin} rotated={secret.rotated} onClose={() => setSecret(null)} /> : null}
      {transferring ? <TransferDialog client={client} a={a} orgName={orgName} people={people} onClose={() => setTransferring(false)}
        onDone={(x) => { setTransferring(false); setA(x); }} /> : null}
      {deleting ? <DeleteDialog client={client} a={a} onClose={() => setDeleting(false)} onDeleted={() => { setDeleting(false); onBack(); }} /> : null}
    </div>
  );
}

const SECRETS_LOADING = "The project's secrets are still loading; save again in a moment.";

function AutomationSettings({ client, a, orgName, onSaved, onTransfer, onTry, onRotate }: {
  client: ApiClient;
  a: Automation;
  orgName: string;
  onSaved: (a: Automation & { incomingSecret?: string }) => void;
  onTransfer: () => void;
  onTry: () => void;
  onRotate: () => void;
}) {
  const [form, setForm] = useState<AutomationInput>(() => inputOf(a));
  const [subs, setSubs] = useState<EventSubscriptions | null>(null);
  const [groups, setGroups] = useState<Array<{ projectId: string; project: string; repos: string[] }>>([]);
  const { busy, problem, save } = useSave();
  // A project's own secrets, null until read: a save resolves picks against their names, so it waits for them.
  const [project, setProject] = useState<ProjectSecret[] | null>(a.projectId ? null : []);
  useEffect(() => {
    if (!a.projectId) return;
    let live = true;
    client.projectSecrets(a.projectId).then((r) => live && setProject(r.secrets), () => live && setProject([]));
    return () => { live = false; };
  }, [client, a.projectId]);
  const picksInForce = (refs: SecretRef[]) => project ? secretPicksInForce(refs, project.map((s) => s.name)) : null;
  useEffect(() => { setForm(inputOf(a)); }, [a]);
  useEffect(() => {
    if (form.trigger.kind === "github") client.eventSubscriptions().then(setSubs, () => setSubs(null));
  }, [client, form.trigger.kind]);
  useEffect(() => {
    if (!a.projectId) client.automationRepositories().then((r) => setGroups(r.groups), () => undefined);
  }, [client, a.projectId]);
  // Whether post_to_chat can post: the organisation's Slack installation; unknown (null) shows no callout.
  const [slackInstalled, setSlackInstalled] = useState<boolean | null>(null);
  useEffect(() => {
    if (a.kind !== "agent") return;
    client.chatOverview().then((o) => setSlackInstalled(o.providers.some((p) => p.provider.id === "slack" && p.installed)), () => setSlackInstalled(null));
  }, [client, a.kind]);
  const ro = !a.canEdit;
  const scope = a.projectId ? "project" : "organization";
  const invalid = automationProblem(form, scope);
  // A problem its own field shows: Save stays live and, tried, says "Not saved" there and under the button.
  const fieldProblem = invalid?.path === "projectIds" || invalid?.path === "trigger.groupBy";
  const [tried, setTried] = useState(false);
  const dirty = JSON.stringify(form) !== JSON.stringify(inputOf(a)) || fieldProblem;
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
  // Saving the prompt or brief saves the automation with it, so each save is a version in its history.
  const saveText = async (agent: AutomationInput["agent"]) => {
    const secrets = picksInForce(form.secrets);
    if (!secrets) throw new Error(SECRETS_LOADING);
    const next = { ...form, agent, secrets };
    const wrong = automationProblem(next, scope);
    if (wrong) throw new Error(wrong.message);
    onSaved(await client.updateAutomation(a.id, next));
  };
  return (
    <form className="settingsForm" onSubmit={(e) => {
      e.preventDefault();
      setTried(true);
      if (invalid) return;
      const secrets = picksInForce(form.secrets);
      if (secrets) void save(() => client.updateAutomation(a.id, { ...form, secrets }).then(onSaved), undefined, "Automation saved");
    }}>
      <fieldset disabled={ro} className="plainFieldset">
        <SettingsSection title="Trigger">
          <SettingRow label="Runs on">
            <Segmented<Trigger["kind"]> label="Runs on" value={trigger.kind} disabled={ro || a.kind === "server"} data-testid="trigger-kind"
              onChange={(k) => set("trigger", k === "schedule" ? { kind: "schedule", cron: "0 9 * * 1-5", tz: "UTC" }
                : k === "incoming" ? { kind: "incoming", hookId: a.trigger.kind === "incoming" ? a.trigger.hookId : "hk_000000000000", verify: DEFAULT_VERIFY_OF.signature,
                  conditions: [], groupBy: "", ratePerHour: INCOMING_RATE_DEFAULT }
                : { kind: "github", event: "pull_request", actions: [], filters: { branch: [], label: [], paths: [], author: [] } })}
              options={[{ value: "schedule", label: "Schedule" }, { value: "github", label: "GitHub event" },
                { value: "incoming", label: "Incoming webhook", disabled: a.kind === "server" }]} />
          </SettingRow>
          {trigger.kind === "schedule" ? (
            <SettingRow label="Schedule">
              <SchedulePicker cron={trigger.cron} tz={trigger.tz} zones={zones} disabled={ro} zoneSource={`${orgName}'s`}
                next={<ScheduleNext client={client} cron={trigger.cron} tz={trigger.tz} />}
                onChange={(s) => set("trigger", { kind: "schedule", ...s })} />
            </SettingRow>
          ) : trigger.kind === "incoming" ? (
            <IncomingTriggerRows trigger={trigger} hint={a.trigger.kind === "incoming" ? a.incomingSecretHint : null} ro={ro} origin={a.publicOrigin}
              groupError={tried && invalid?.path === "trigger.groupBy" ? invalid.message : undefined}
              onChange={(t) => set("trigger", t)} onRotate={onRotate} />
          ) : (
            <EventTrigger trigger={trigger} subs={subs} disabled={ro} onChange={(t) => set("trigger", t)} />
          )}
          {a.projectId ? null : trigger.kind === "incoming" ? (
            <ProjectsRow projects={groups.map((g) => ({ id: g.projectId, name: g.project }))} value={form.projectIds} ro={ro}
              error={tried && invalid?.path === "projectIds" ? invalid.message : undefined}
              onChange={(ids) => setForm((f) => ({ ...f, projectIds: ids, repos: Array.isArray(f.repos) ? "all" : f.repos }))} />
          ) : trigger.kind === "schedule" && a.kind !== "webhook" ? (
            <SettingRow label="Checked out" help="What each run checks out. None: it starts in an empty workspace, with its image's tools." data-testid="org-checked-out">
              <Segmented<"none" | "some"> label="Checked out" value={form.repos === "none" ? "none" : "some"} disabled={ro} data-testid="repos-checked-out"
                onChange={(m) => setForm((f) => ({ ...f, repos: m === "none" ? "none" : "all", repoFilter: m === "none" ? null : f.repoFilter }))}
                options={[{ value: "none", label: "None" }, { value: "some", label: "Some" }]} />
              {form.repos !== "none" ? (
                <RepoFilterField value={(form.repoFilter ?? { mode: "all", repos: [] }) as RepoFilterValue} org={orgName} groups={groups} disabled={ro}
                  onChange={(v) => set("repoFilter", { mode: v.mode, repos: [...v.repos] })} />
              ) : null}
            </SettingRow>
          ) : (
            <SettingRow label="Repositories" help={trigger.kind === "schedule" ? "What each run checks out." : "Where its events may come from."}>
              <RepoFilterField value={(form.repoFilter ?? { mode: "all", repos: [] }) as RepoFilterValue} org={orgName} groups={groups} disabled={ro}
                onChange={(v) => set("repoFilter", { mode: v.mode, repos: [...v.repos] })} />
            </SettingRow>
          )}
          {a.projectId && a.kind !== "webhook" ? (
            <ProjectReposRow client={client} projectId={a.projectId} ro={ro} value={form.repos} onChange={(v) => set("repos", v)} />
          ) : null}
          {trigger.kind !== "schedule" && (a.canEdit || a.canTransfer) ? (
            <SettingRow label="Try it" help={trigger.kind === "incoming" ? "Runs it once on a past delivery or JSON you write, as its own run."
              : "Runs it once on a past delivery or an event you write, as its own run."}>
              <Button variant="secondary" size="sm" leadingIcon="play" data-testid="try-it" onClick={onTry}>
                {trigger.kind === "incoming" ? "Test with a payload…" : "Test with an event…"}</Button>
            </SettingRow>
          ) : null}
        </SettingsSection>

        {a.kind === "webhook" ? null : (
          <SettingsSection title="Session">
            {a.kind === "command" ? null : (
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
            )}
            <SettingRow label="While a run is going" data-testid="automation-concurrency"
              help={trigger.kind === "incoming" ? "What a delivery of another group does. A delivery whose group's run is still going is always sent to it. A test run always starts its own."
                : "What a new event does. A test run always starts its own."}>
              <Select<string> aria-label="Concurrency" value={form.concurrency} disabled={ro} onValueChange={(c) => set("concurrency", c as AutomationInput["concurrency"])}
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
                onChange={(s) => setForm((f) => ({ ...f, agent: s === "own" ? { own: { prompt: "" } } : { preset: { role: "qa_browser", overrides: {} }, brief: "" },
                  // One of dude's agents gets what its role is given; it keeps no picks.
                  secrets: s === "preset" ? [] : f.secrets }))}
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
                  <PromptEditor client={client} a={a} ro={ro} label="Task brief" text={preset.brief}
                    onSave={(brief) => saveText({ ...preset, brief })} onRestored={onSaved} />
                </SettingRow>
              </>
            ) : own ? (
              <>
                <RuntimeFields client={client} ro={ro} agent value={own}
                  onChange={(next) => set("agent", { own: { ...next, prompt: own.prompt } })} />
                <SettingRow label="Prompt" block data-testid="automation-prompt">
                  <PromptEditor client={client} a={a} ro={ro} label="Prompt" text={own.prompt}
                    onSave={(prompt) => saveText({ own: { ...own, prompt } })} onRestored={onSaved} />
                </SettingRow>
              </>
            ) : null}
            <SettingRow label="Tools" block help="What its agent can call, besides the shell in its machine." data-testid="tools-row">
              <AutomationToolList orgName={orgName} slackInstalled={slackInstalled} />
            </SettingRow>
            <SettingsNote>It opens no pull requests.{trigger.kind === "incoming" ? " The payload is outside text: the prompt marks it so." : ""}</SettingsNote>
          </SettingsSection>
        ) : null}

        {a.kind === "command" || a.kind === "server" ? (
          <SettingsSection title="Runs in">
            <RuntimeFields client={client} ro={ro} agent={false}
              value={{ ...(form.machineSizeId ? { machineSizeId: form.machineSizeId } : {}), ...(form.imageId ? { imageId: form.imageId } : {}) }}
              onChange={(next) => setForm((f) => ({ ...f, machineSizeId: next.machineSizeId ?? null, imageId: next.imageId ?? null }))} />
          </SettingsSection>
        ) : null}

        {a.kind !== "webhook" ? (
          <SettingsSection title="Setup script">
            <SettingRow label="Setup script" block help="Runs in each checkout before the run starts; a repository's own setup script takes its place.">
              <CodeEditor aria-label="Setup script" language="shell" readOnly={ro} value={form.setupScript ?? ""} minLines={3} maxLines={16}
                placeholder="bun install" data-testid="setup-script"
                onChange={(v) => set("setupScript", v || null)}
                footer={<SizeMeter bytes={new TextEncoder().encode(form.setupScript ?? "").length} limit={COMMAND_MAX_BYTES} />} />
            </SettingRow>
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
            <SettingsNote>{AUTOMATION_POST_RETRY}</SettingsNote>
          </SettingsSection>
        ) : null}

        {a.kind !== "webhook" ? <AutomationSecrets client={client} a={a} orgName={orgName} preset={preset?.preset.role ?? null} ro={ro}
          project={project} chosen={form.secrets} onChange={(refs) => set("secrets", refs)} /> : null}
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
          {invalid && !(tried && fieldProblem) ? <Callout tone="attention">{invalid.message}</Callout> : null}
          {tried && fieldProblem && invalid ? <Callout tone="danger" data-testid="not-saved"><b>Not saved:</b> {invalid.message}</Callout> : null}
          {problem ? <Callout tone="danger">{problem}</Callout> : null}
          <Button type="submit" variant="primary" disabled={busy || !dirty || (Boolean(invalid) && !fieldProblem)} loading={project === null}>Save</Button>
        </div>
      )}
    </form>
  );
}

// Wait for valid schedule inputs to settle before asking the scheduler for its next runs.
function ScheduleNext({ client, cron, tz }: { client: ApiClient; cron: string; tz: string }) {
  const [times, setTimes] = useState<Date[] | null>(null);
  useEffect(() => {
    let live = true;
    setTimes(null);
    if (!parseCron(cron).ok || !isTimeZone(tz)) return;
    const t = setTimeout(() => client.scheduleNext(cron, tz)
      .then((r) => { if (live) setTimes(r.times.map((x) => new Date(x))); }, () => { if (live) setTimes(null); }), 300);
    return () => { live = false; clearTimeout(t); };
  }, [client, cron, tz]);
  return times ? <span className="automationNext" data-testid="schedule-next">Next: <NextRuns times={times} tz={tz} /></span> : null;
}

/** A project automation's checkouts: none, or the repositories ticked (every one is "all"). */
function ProjectReposRow({ client, projectId, ro, value, onChange }: {
  client: ApiClient; projectId: string; ro: boolean; value: AutomationInput["repos"]; onChange: (v: AutomationInput["repos"]) => void;
}) {
  const [repos, setRepos] = useState<Array<{ id: string; name: string }> | null>(null);
  useEffect(() => {
    client.getProject(projectId).then((p) => setRepos(p.repositories.map((r) => ({ id: r.id, name: r.name }))), () => setRepos([]));
  }, [client, projectId]);
  const chosen = value === "all" ? (repos ?? []).map((r) => r.id) : value === "none" ? [] : value;
  const pick = (ids: string[]) => onChange(ids.length === 0 ? "none" : repos && ids.length === repos.length ? "all" : ids);
  return (
    <SettingRow label="Checked out" help="The project's repositories each run starts with.">
      <Segmented<"none" | "some"> label="Checked out" value={value === "none" ? "none" : "some"} disabled={ro} data-testid="repos-checked-out"
        onChange={(m) => onChange(m === "none" ? "none" : "all")}
        options={[{ value: "none", label: "None" }, { value: "some", label: "Some" }]} />
      {value !== "none" && repos ? (
        <div role="group" aria-label="Repositories checked out" className="automationActions">
          {repos.map((r) => <Checkbox key={r.id} label={r.name} checked={chosen.includes(r.id)} disabled={ro}
            onCheckedChange={(on) => pick(on ? [...chosen, r.id] : chosen.filter((x) => x !== r.id))} />)}
        </div>
      ) : null}
    </SettingRow>
  );
}

function AutomationSecrets({ client, a, orgName, preset, ro, project, chosen, onChange }: {
  client: ApiClient; a: Automation; orgName: string; preset: PresetRole | null; ro: boolean;
  project: ProjectSecret[] | null; chosen: SecretRef[]; onChange: (refs: SecretRef[]) => void;
}) {
  const org = useOrgSecrets(client);
  const own = a.projectId && project ? { name: a.projectName ?? "the project", secrets: project } : undefined;
  return (
    <SettingsSection title="Secrets">
      {org.secrets === null || project === null ? (
        <SettingRow label="Secrets">{org.problem ? <span>{org.problem}</span> : <Spinner label="Loading secrets…" />}</SettingRow>
      ) : preset ? (
        <SettingRow label="Secrets" help={PRESET_SECRETS(PRESET_ROLE_LABEL[preset])} data-testid="preset-secrets">
          <PresetSecretsLine role={preset} orgName={orgName} org={org.secrets} project={own} />
        </SettingRow>
      ) : (
        <SettingRow label="Secrets" block help={ORG_AUTOMATION_PICKS(orgName)} data-testid="automation-secrets">
          <SecretPicker orgName={orgName} org={org.secrets} disabled={ro} value={chosen} data-testid="secret-picker"
            project={own} onChange={onChange} />
        </SettingRow>
      )}
    </SettingsSection>
  );
}

/** What a dude-role preset's runs get: its role's ticks at both levels, the project's winning a name. */
function PresetSecretsLine({ role, orgName, org, project }: {
  role: PresetRole; orgName: string; org: readonly OrgSecret[]; project?: { name: string; secrets: readonly ProjectSecret[] } | undefined;
}) {
  const recipient = PRESET_SECRET_RECIPIENT[role];
  const own = new Set((project?.secrets ?? []).map((s) => s.name));
  const fromProject = recipient ? (project?.secrets ?? []).filter((s) => s.recipients.includes(recipient)) : [];
  const fromOrg = recipient ? org.filter((s) => s.recipients.includes(recipient) && !own.has(s.name)) : [];
  return (
    <PresetSecrets role={PRESET_ROLE_LABEL[role]} data-testid="preset-secrets-line"
      given={[{ from: orgName, names: fromOrg.map((s) => s.name) }, ...(project ? [{ from: project.name, names: fromProject.map((s) => s.name) }] : [])]}
      none={recipient ? undefined : "no secrets: it is not one of the agents a secret is given to"} />
  );
}

type OverrideKey = keyof AgentOverrides;

const DEFAULT = "__default";

/**
 * What an automation's own agent, command or server runs on: harness,
 * model and time limit (an agent's), machine and image. Each left at the
 * default is unset, and the run gets the organisation's default.
 */
function RuntimeFields({ client, ro, agent, value, onChange }: {
  client: ApiClient;
  ro: boolean;
  agent: boolean;
  value: AgentOverrides;
  onChange: (v: AgentOverrides) => void;
}) {
  const [tiers, setTiers] = useState<readonly ModelTier[]>([]);
  const [sizes, setSizes] = useState<readonly MachineSizeWithUse[]>([]);
  const [images, setImages] = useState<readonly ImageChoice[]>([]);
  useEffect(() => {
    if (agent) client.modelTiers().then((r) => setTiers(r.tiers), () => undefined);
    client.machineSizes().then((r) => setSizes(r.sizes), () => undefined);
    client.imageChoices().then((r) => setImages(r.images), () => undefined);
  }, [client, agent]);
  const setOne = <K extends OverrideKey>(k: K, v: AgentOverrides[K] | undefined) => {
    const next = { ...value };
    if (v === undefined || (v as unknown) === DEFAULT) delete next[k];
    else next[k] = v;
    onChange(next);
  };
  const defaultSize = sizes.find((s) => s.isDefault)?.name;
  return (
    <SettingFields>
      {agent ? (
        <>
          <SettingField label="Harness">
            <Select<string> aria-label="Harness" value={value.harness ?? DEFAULT} disabled={ro}
              options={[{ value: DEFAULT, label: "OpenCode (default)" }, ...HARNESSES.map((h) => ({ value: h, label: HARNESS_LABEL[h] }))]}
              onValueChange={(v) => setOne("harness", v === DEFAULT ? undefined : v as Harness)} />
          </SettingField>
          <SettingField label="Model">
            <Select<string> aria-label="Model" value={value.tierId ?? DEFAULT} disabled={ro}
              options={[{ value: DEFAULT, label: "The default tier" }, ...tiers.map((t) => ({ value: t.id, label: t.name, meta: t.model ?? "No model" }))]}
              onValueChange={(v) => setOne("tierId", v === DEFAULT ? undefined : v)} />
          </SettingField>
          <SettingField label="Time limit">
            <Select<string> aria-label="Time limit" value={value.timeLimitMinutes ? String(value.timeLimitMinutes) : DEFAULT} disabled={ro}
              options={[{ value: DEFAULT, label: "No limit" }, ...[30, 60, 120, 240, 480].map((m) => ({ value: String(m), label: m < 60 ? `${m} min` : `${m / 60} hour${m === 60 ? "" : "s"}` }))]}
              onValueChange={(v) => setOne("timeLimitMinutes", v === DEFAULT ? undefined : Number(v))} />
          </SettingField>
        </>
      ) : null}
      <SettingField label="Machine">
        <Select<string> aria-label="Machine" value={value.machineSizeId ?? DEFAULT} disabled={ro}
          options={[{ value: DEFAULT, label: defaultSize ? `${defaultSize} (default)` : "The default size" },
            ...sizes.map((s) => ({ value: s.id, label: s.name, meta: `${s.cpus} CPUs` }))]}
          onValueChange={(v) => setOne("machineSizeId", v === DEFAULT ? undefined : v)} />
      </SettingField>
      <SettingField label="Image">
        <Select<string> aria-label="Image" value={value.imageId ?? DEFAULT} disabled={ro}
          options={[{ value: DEFAULT, label: "The project's runtime image" }, ...images.map((i) => ({ value: i.id, label: i.name }))]}
          onValueChange={(v) => setOne("imageId", v === DEFAULT ? undefined : v)} />
      </SettingField>
    </SettingFields>
  );
}

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
  const rolePrompt = r?.prompt.project?.mode === "replace" ? r.prompt.project.body
    : [r?.prompt.organization.body, r?.prompt.project?.mode === "add" ? r.prompt.project.body : ""].filter(Boolean).join("\n\n");
  return (
    <div data-testid="preset-fields">
      {r ? (
        <SettingsDisclosure summary={`${roleName}'s prompt (from ${scopeName}'s Agents settings)`}>
          <pre className="automationPayload" data-testid="preset-role-prompt">{rolePrompt || `${roleName}'s built-in prompt.`}</pre>
        </SettingsDisclosure>
      ) : null}
      {rows.map(([k, label, help]) => {
        const set = overrides[k] !== undefined;
        return (
          <SettingRow key={k} label={label} help={help} data-testid={`preset-${k}`}
            source={set
              ? <SettingSource source="project" from={roleName} inherited={resolved[k]} onReset={ro ? undefined : () => setOne(k, undefined)} />
              : <SettingSource source="organization" from={from} />}>
            {set ? control(k) : (
              <>
                <span className="automationValueBox" data-testid={`preset-${k}-value`}>{resolved[k]}</span>
                {ro || first[k] === undefined ? null : <TextButton data-testid={`preset-${k}-override`} onClick={() => setOne(k, first[k])}>Override for this automation</TextButton>}
              </>
            )}
          </SettingRow>
        );
      })}
    </div>
  );
}

/**
 * An agent automation's prompt or brief: the role pages' Markdown editor,
 * with `{{ }}` inserting the automation's variables, and its history —
 * who saved each version and when, each one readable and restorable.
 */
function PromptEditor({ client, a, ro, label, text, onSave, onRestored }: {
  client: ApiClient;
  a: Automation;
  ro: boolean;
  label: string;
  text: string;
  onSave: (text: string) => Promise<void>;
  onRestored: (a: Automation) => void;
}) {
  const [versions, setVersions] = useState<AutomationPromptVersion[] | null>(null);
  const [open, setOpen] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);
  const load = useCallback(() => {
    client.automationPromptHistory(a.id).then((r) => setVersions(r.versions), (e: unknown) => setProblem(errorText(e)));
  }, [client, a.id]);
  useEffect(load, [load, a.updatedAt]);
  const latest = versions?.[0];
  const shown = useMemo(() => (versions ?? []).map((v) => ({
    id: v.id, number: v.number, body: v.body, note: v.restoredFrom ? `Restored version ${v.restoredFrom}` : "",
    author: v.createdBy, when: formatTimestamp(v.createdAt, "datetime"), current: v.current, sessions: { count: 0, recent: [] },
  })), [versions]);
  const meta = (
    <SettingsMeta>
      {latest ? `Last changed by ${latest.createdBy?.name ?? "its creator"} · ${formatTimestamp(latest.createdAt, "relative")}` : "Never changed"}
      {" · "}<TextButton onClick={() => setOpen(true)} data-testid="automation-prompt-history">History</TextButton>
    </SettingsMeta>
  );
  return (
    <>
      <MarkdownDocument key={`${a.id}-${a.updatedAt}`} data-testid="automation-prompt-document" breaks label={label} source={text}
        emptyText="Nothing written yet." meta={meta} variables={a.trigger.kind === "incoming" ? INCOMING_PROMPT_VARIABLES : AUTOMATION_PROMPT_VARIABLES}
        onSave={ro ? undefined : async (body) => {
          setProblem(null);
          try {
            await onSave(body);
          } catch (e) {
            setProblem(errorText(e));
            throw e;
          }
        }} />
      {problem ? <Callout tone="danger">{problem}</Callout> : null}
      {open ? (
        <Dialog open onOpenChange={(o) => !o && setOpen(false)} size="xl" title={`${a.name} · ${label.toLowerCase()} history`}
          description={`${versions?.length ?? 0} ${versions?.length === 1 ? "version" : "versions"} · every save is kept`}>
          <div data-testid="automation-prompt-history-dialog">
            <PromptHistory versions={shown} emptyText="Never saved: it still has the text it was created with."
              onRestore={ro ? undefined : async (id) => {
                try {
                  onRestored(await client.restoreAutomationPrompt(a.id, id));
                  load();
                } catch (e) {
                  setProblem(errorText(e));
                }
              }} />
          </div>
        </Dialog>
      ) : null}
    </>
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
  const known = EVENT_ACTIONS[trigger.event];
  return (
    <>
      <SettingRow label="Event" help={subs ? (subs.source === "seen"
        ? "GitHub did not say what it sends dude: these are the events that have arrived."
        : "The events this organisation's GitHub sends dude.") : undefined}>
        <Select<string> aria-label="Event" value={trigger.event} disabled={disabled} options={events.map((e) => ({ value: e, label: e }))}
          onValueChange={(event) => onChange({ ...trigger, event, actions: [] })} />
        {subs?.settingsUrl ? <a href={subs.settingsUrl} target="_blank" rel="noreferrer">Add events in the App's permissions</a> : null}
      </SettingRow>
      <SettingRow label="Actions" help="None ticked means every action.">
        {known ? (
          <div role="group" aria-label="Actions" className="automationActions" data-testid="event-actions">
            {[...new Set([...known, ...trigger.actions])].map((act) => (
              <Checkbox key={act} label={act} checked={trigger.actions.includes(act)} disabled={disabled}
                onCheckedChange={(on) => onChange({ ...trigger, actions: on ? [...trigger.actions, act] : trigger.actions.filter((x) => x !== act) })} />
            ))}
          </div>
        ) : (
          <ChipsInput label="Actions" values={trigger.actions} disabled={disabled} placeholder="opened, synchronize"
            onChange={(actions) => onChange({ ...trigger, actions })} />
        )}
      </SettingRow>
      <SettingRow label="Filters" block help="Values in one filter are any of them; filters all hold." data-testid="event-filters">
        <SettingFields>
          <SettingField label="Branch"><ChipsInput label="Branch" values={filters.branch} disabled={disabled} placeholder="main, release/*" onChange={(v) => setFilter("branch", v)} /></SettingField>
          <SettingField label="Label"><ChipsInput label="Label" values={filters.label} disabled={disabled} onChange={(v) => setFilter("label", v)} /></SettingField>
          <SettingField label="Paths"><ChipsInput label="Paths" values={filters.paths} disabled={disabled} placeholder="src/**" onChange={(v) => setFilter("paths", v)} /></SettingField>
          <SettingField label="Author"><ChipsInput label="Author" values={filters.author} disabled={disabled} placeholder="!dependabot[bot]" onChange={(v) => setFilter("author", v)} /></SettingField>
        </SettingFields>
        <SettingsNote>Branch and paths are globs; paths read a pull request's files when it runs; !login leaves someone out.</SettingsNote>
      </SettingRow>
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
  useReloadOnEvents({ client, all: true }, load, 1000, (event) => {
    // Only this automation's run events and its runs' calls change the list; anything else needs no re-read.
    const ours = event.payload.automationId === a.id || data?.runs.some((r) => r.id === event.automationRunId);
    return !ours || !(event.eventType.startsWith("automation.run.") || event.eventType === "agent.tool.called" || event.eventType === "agent.tool.completed");
  });
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
          <THead><Tr><Th style={{ width: 100 }}>Status</Th><Th style={{ width: "60%" }}>Why</Th>{a.sessionMode === "resumable" ? <Th hideWhenNarrow>Session</Th> : null}<Th hideWhenNarrow>Cost</Th><Th>When</Th></Tr></THead>
          <TBody>
            {data.runs.map((r) => (
              <Tr key={r.id} interactive onClick={() => onOpenRun(r.id)} data-testid="run-row" data-id={r.id}>
                <Td><RunStatus status={r.status} /></Td>
                <Td>{r.reason ?? (r.event ? `${r.event}${r.action ? ` · ${r.action}` : ""}${r.payloadCount > 1 ? ` · ${r.payloadCount} events` : ""}` : r.source === "schedule" ? "Scheduled" : "Run now")}
                  {r.source === "test" ? <Badge size="sm" tone="info">Test</Badge> : null}
                  {r.status === "running" && r.now ? <RunNowLine tool={r.now.tool} args={r.now.args} /> : null}</Td>
                 {a.sessionMode === "resumable" ? <Td hideWhenNarrow muted>{r.session ?? "—"}</Td> : null}
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

function RunDetail({ client, a, runId, onDelivery }: { client: ApiClient; a: Automation; runId: string; onDelivery: (id: string) => void }) {
  const [run, setRun] = useState<AutomationRunDetail | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const { events } = useRunActivity(client, runId, a.kind === "agent");
  // Only facts-changing events trigger a detail read; log traffic stays in the feed.
  const moved = events.filter((e) => e.eventType === "automation.run.ended" || e.eventType === "automation.delivery.sent" ||
    e.eventType === "automation.log.exited" || e.eventType === "agent.tool.dude").at(-1)?.cursor ?? 0;
  useEffect(() => {
    const timer = setTimeout(() => { void client.automationRun(a.id, runId).then(setRun, (e: unknown) => setProblem(errorText(e))); }, moved ? 1000 : 0);
    return () => clearTimeout(timer);
  }, [client, a.id, runId, moved]);
  // Cost and server requests can change without an agent event. Stop polling once the run ends.
  useVisibleInterval(() => {
    if (run?.status === "running" || run?.status === "queued") {
      void client.automationRun(a.id, runId).then(setRun, (e: unknown) => setProblem(errorText(e)));
    }
  }, 4000);
  const tick = useNow(run?.status === "running");
  // Projected once for the hint; the feed projects its own with the run's status.
  const hint = useMemo(() => a.kind === "agent" && run && run.status !== "running"
    && events.some((e) => e.eventType !== EV_LOG_LINE && e.eventType !== EV_LOG_EXITED) ? activityCount(project(events).turns) : "",
  [a.kind, run?.status, events]); // eslint-disable-line react-hooks/exhaustive-deps
  if (!run) return problem ? <Callout tone="danger">{problem}</Callout> : <Spinner label="Loading…" />;
  const live = run.status === "running";
  // The call the run failed on (a post_to_chat without Slack), as the API names it.
  const failedCall = run.failedCallId ? run.toolCalls.find((c) => c.id === run.failedCallId) ?? null : null;
  const started = run.startedAt ?? run.createdAt;
  const elapsed = (run.endedAt ? Date.parse(run.endedAt) : tick) - Date.parse(started);
  const fallback = run.logTail && events.length === 0 ? <pre className="automationPayload" data-testid="run-log-tail">{run.logTail}</pre> : null;
  return (
    <div data-testid="run-detail" className="automationRunDetail" data-kind={a.kind}>
      <SettingsSection title="Run">
        <p className="automationRunHead"><RunStatus status={run.status} />
          {live ? <LiveMark /> : null}
          <span>Started <span className="ds-tnum">{formatTimestamp(started, "time")}</span></span>
          {run.status !== "queued" ? <span className="ds-hint ds-tnum">· {formatDuration(Math.max(0, elapsed))}</span> : null}
          {run.costUsd !== null ? <span className="automationRunCost">· ${run.costUsd.toFixed(2)}{live ? " so far" : ""}</span> : null}</p>
        <RunTriggerLine a={a} run={run} onDelivery={onDelivery} />
        {run.reason && failedCall ? (
          <Callout tone="danger" data-testid="run-failed">
            <b>Failed.</b> {run.reason}. {run.toolCalls.some((c) => c.posted) ? "That message was not posted" : "Nothing was posted"};{" "}
            {a.owner?.name ?? "its owner"} was told.
          </Callout>
        ) : run.reason && run.status === "failed" ? (
          <Callout tone="danger" data-testid="run-failed"><b>Failed.</b> {run.reason}{/[.!]$/.test(run.reason) ? "" : "."}{a.notifyOnFailure ? ` ${a.owner?.name ?? "Its owner"} was told.` : ""}</Callout>
        ) : run.reason ? <SettingsNote>{run.reason}</SettingsNote> : null}
        {a.kind === "server" && run.containerStartedAt ? (
          <ol className="automationTimeline" data-testid="server-timeline">
            <li><time>{formatTimestamp(run.containerStartedAt, "time")}</time> Started</li>
            {run.requests.map((q) => (
              <li key={q.id}><time>{formatTimestamp(q.at, "time")}</time> POST {q.httpStatus ?? q.error ?? "no answer"}{q.try > 1 ? " (retried)" : ""}</li>
            ))}
            {run.containerStoppedAt ? <li><time>{formatTimestamp(run.containerStoppedAt, "time")}</time> {run.stopReason === "idle" ? "Idle 30 s · stopped" : "Stopped"}</li>
              : live ? <li className="ds-hint" data-testid="server-waiting"><time>{formatTimestamp(new Date(tick), "time")}</time> Waiting for the next event · stops after 30 s idle</li> : null}
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
      {run.payloads.length === 0 && !run.payloadsPurged || a.trigger.kind === "incoming" ? null : (
      <SettingsDisclosure icon="braces" summary="Trigger payload">
        {run.payloadsPurged ? <SettingsNote>Payloads are kept 30 days.</SettingsNote>
          : <pre className="automationPayload">{JSON.stringify(run.payloads.length === 1 ? run.payloads[0]!.body : run.payloads, null, 2)}</pre>}
      </SettingsDisclosure>
      )}
      {a.kind === "agent" ? (
        <SettingsSection title="Activity" data-testid="run-activity"
          actions={<span className="ds-hint">{live ? "Follows the latest · scroll up to stop" : hint}</span>}>
          {fallback ?? <AutomationFeed run={run} events={events} name={a.name}
            role={a.agent && "preset" in a.agent ? a.agent.preset.role as AgentRole : "investigator"} live={live} onDelivery={onDelivery} />}
        </SettingsSection>
      ) : a.kind === "command" ? (
        <SettingsSection title="Output" data-testid="run-output">
          {fallback ?? <CommandLog command={a.command ?? ""} events={events} live={live} />}
        </SettingsSection>
      ) : a.kind === "server" ? (
        <SettingsSection title="Container log" data-testid="run-container-log">
          {fallback ?? <ContainerLog image={a.server?.command ?? "server"} events={events} live={live} />}
        </SettingsSection>
      ) : run.logTail ? <SettingsSection title="Log"><pre className="automationPayload">{run.logTail}</pre></SettingsSection> : null}
    </div>
  );
}

/** The run's trigger as one line: a schedule's due time and how late it started; an incoming delivery; a GitHub event; Run now. */
function RunTriggerLine({ a, run, onDelivery }: { a: Automation; run: AutomationRunDetail; onDelivery: (id: string) => void }) {
  if (a.trigger.kind === "incoming") return <KeyValueList items={[{ label: "Trigger", value: <IncomingTriggerLine a={a} run={run} onDelivery={onDelivery} /> }]} />;
  const late = run.scheduledFor && run.startedAt ? Math.round((Date.parse(run.startedAt) - Date.parse(run.scheduledFor)) / 1000) : null;
  const value = a.trigger.kind === "schedule" && run.source === "schedule" ? (
    <span className="automationTriggerLine"><TriggerSummary trigger={a.trigger} />
      {run.scheduledFor ? <span className="ds-hint"> · due {formatTimestamp(run.scheduledFor, "datetime")}{late !== null && late >= 0 ? `, started ${late} s later` : ""}</span> : null}</span>
  ) : run.event ? (
    <span className="automationTriggerLine"><span className="ds-mono">{run.event}</span>{run.action ? ` · ${run.action}` : ""}
      {run.payloads[0]?.deliveryId ? <span className="ds-hint ds-mono"> · {run.payloads[0].deliveryId}</span> : null}
      {run.payloadCount > 1 ? <span className="ds-hint"> · {run.payloadCount - 1} later {run.payloadCount === 2 ? "delivery" : "deliveries"} sent to this run</span> : null}</span>
  ) : <span>{run.source === "test" ? "Test run" : "Run now"}{run.startedBy ? ` by ${run.startedBy.name}` : ""}</span>;
  return <KeyValueList items={[{ label: "Trigger", value }]} />;
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

function RunNowDialog({ client, a, onClose, onStarted }: { client: ApiClient; a: Automation; onClose: () => void; onStarted: (runId: string) => void }) {
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
          onClick={() => void save(async () => { const result = await client.runAutomation(a.id, mode === "past" ? { deliveryId: picked! }
            : { testEvent: { event: trigger.event, action, deliveryId, body: (json as { value: Record<string, unknown> }).value } }); onStarted(result.runId); }, () => undefined, "Run started")}>
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
            <SettingFields>
              <SettingField label="X-GitHub-Event"><Input aria-label="X-GitHub-Event" hint="The trigger's event." value={trigger.event} readOnly mono /></SettingField>
              <SettingField label="Action">
                <Select<string> aria-label="Action" value={action ?? ""} onValueChange={(v) => {
                  setAction(v || null);
                  const parsed = checkJson(text);
                  if (parsed.ok && parsed.value && typeof parsed.value === "object") setText(JSON.stringify({ ...(parsed.value as object), action: v }, null, 2));
                }} options={(trigger.actions.length ? trigger.actions : EVENT_ACTIONS[trigger.event] ?? ["opened", "closed", "edited", "created", "completed"]).map((x) => ({ value: x, label: x }))} />
                <small className="automationHint">The trigger's actions.</small>
              </SettingField>
              <SettingField label="X-GitHub-Delivery"><Input aria-label="X-GitHub-Delivery" hint="Made up; starts with test-." value={deliveryId} mono onChange={(e) => setDeliveryId(e.target.value)} /></SettingField>
            </SettingFields>
            <CodeEditor aria-label="Event body" language="plain" value={text} onChange={setText} data-testid="test-editor" minLines={10} maxLines={20}
              diagnostics={json.ok ? (objectBody ? [] : [{ severity: "error", line: 1, from: 0, to: 1, message: "The body must be a JSON object." }])
                : [{ severity: "error", line: json.line, from: Math.max(0, json.column - 1), to: json.column, message: json.message }]}
              header={<span className="ds-mono">json · body of {trigger.event}</span>}
              footer={<span className="automationEditorFoot">
                <span>{text.split("\n").length} lines</span>
                <TextButton onClick={() => setText(JSON.stringify(testTemplate(trigger.event, action), null, 2))} data-testid="reset-template">Reset to template</TextButton>
              </span>} />
            <ul className="automationChecks" data-testid="test-checks" role="status">
              <li data-ok={json.ok && objectBody ? "true" : "false"}>
                {json.ok ? (objectBody ? "✓ Valid JSON." : "× The body must be a JSON object.") : `× Not valid JSON, line ${json.line}, column ${json.column}: ${json.message}`}
              </li>
              <li data-ok={verdict?.matches ? "true" : "false"}>
                {!objectBody ? "Filters are checked once the JSON is valid."
                  : !verdict ? "Checking the filters…"
                  : verdict.matches ? <>✓ Filters are checked against this event: <b>it matches.</b></>
                  : <>⊘ <b>It would be skipped because of {FILTER_LABEL[verdict.filter]}</b>: {verdict.because}</>}
              </li>
            </ul>
          </>
        )}
        <SettingsNote>A test run starts its own run, with a fresh session, whatever is running. It is a real run: it can comment and create tasks.</SettingsNote>
      </FormStack>
    </Dialog>
  );
}

/**
 * Delete, confirmed: says how many runs' history goes with it and, for a
 * kept session, that its container is ended.
 */
function DeleteDialog({ client, a, onClose, onDeleted }: { client: ApiClient; a: Automation; onClose: () => void; onDeleted: () => void }) {
  const [runs, setRuns] = useState<AutomationRunsResponse | null>(null);
  const { busy, problem, save } = useSave();
  useEffect(() => { client.automationRuns(a.id).then(setRuns, () => setRuns(null)); }, [client, a.id]);
  const n = runs?.total ?? 0;
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()} title={`Delete ${a.name}?`} size="sm" footerProblem={problem}
      footer={<>
        <Button variant="secondary" onClick={onClose}>Cancel</Button>
        <Button variant="danger" solid disabled={busy} data-testid="delete-confirm"
          onClick={() => void save(() => client.deleteAutomation(a.id), onDeleted, "Automation deleted")}>Delete</Button>
      </>}>
      <FormStack>
        <p data-testid="delete-consequences">
          {!runs ? "Reading its runs…" : n === 0 ? "It has no runs." : n === 1 ? "Its one run goes with it: history, log and payload." : `Its ${n} runs go with it: their history, logs and payloads.`}
          {runs?.session ? " Its kept session is ended: the container it resumes is stopped and its disk is gone." : ""}
          {" "}This cannot be undone. Turning it off keeps everything.
        </p>
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
