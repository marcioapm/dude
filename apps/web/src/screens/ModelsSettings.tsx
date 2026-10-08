/**
 * Organisation settings → Models: the tiers agents are given (everyone
 * reads; admins add, change, test and remove them), how a tier reaches the
 * proxy, and — once, after the upgrade — what became of each role's model.
 */

import { useEffect, useMemo, useState } from "react";
import {
  AgentAvatar,
  FlowSteps,
  NameChips,
  ProjectAvatar,
  SettingsDisclosure,
  SettingsHeader,
  SettingsMeta,
  SettingsSection,
  TierLine,
  TierMark,
  UsedBy,
} from "@dude/design-system/components";
import { formatTimestamp } from "@dude/design-system";
import {
  Button,
  Callout,
  Dialog,
  FormRow,
  FormStack,
  Input,
  RowMenu,
  Select,
  Spinner,
  Table,
  TBody,
  Td,
  Textarea,
  Th,
  THead,
  Tr,
} from "@dude/design-system/primitives";
import {
  TIER_DESCRIPTION_MAX,
  TIER_EFFORTS,
  TIER_MODEL_MAX,
  TIER_NAME_MAX,
  type ModelTestResult,
  type ModelTier,
  type ModelTiersResponse,
  type ModelTierUse,
  type ModelTierWithUse,
  type ProxyModels,
  type TierEffort,
} from "@dude/domain";
import type { ApiClient } from "../api/client.ts";
import { errorText, FormDialog, useSave } from "../hooks/useSave.tsx";
import {
  proxyKnows,
  testResultWords,
  tierDraftOf,
  tierDraftProblems,
  tierEffortLabel,
  tierInput,
  tierMark,
  tierUsedByWords,
  tierUseName,
  tierUseWhere,
  upgradeLines,
  type TierDraft,
} from "../tiers.ts";
import { roleLabel } from "../machines.ts";

/**
 * The organisation's tiers, loaded once per client by the settings screen
 * and passed to the Models page and every role's tier field; `setTiers`
 * takes what a change on the Models page answered. A failed load leaves no
 * tiers, and `problem` says why, on the Models page and each Model field.
 */
export function useModelTiers(client: ApiClient) {
  const [tiers, setTiers] = useState<ModelTiersResponse | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  useEffect(() => {
    client.modelTiers().then(setTiers, (err: unknown) => {
      setProblem(errorText(err));
      setTiers({ tiers: [], canEdit: false, upgrade: [] });
    });
  }, [client]);
  return { tiers, setTiers, problem };
}

/** The proxy's model ids, read when a tier dialog opens. */
function useProxyModels(client: ApiClient) {
  const [models, setModels] = useState<ProxyModels | null>(null);
  useEffect(() => {
    client.proxyModels().then(setModels, (err: unknown) => setModels({ models: [], source: null, problem: errorText(err) }));
  }, [client]);
  return models;
}

/** An agent's face; the fixer wears the implementer's. */
const agentFace = (role: string) =>
  <AgentAvatar key={`r-${role}`} role={(role === "fixer" ? "implementer" : role) as "implementer"} size="xs" />;

const faceKey = (use: ModelTierUse) => (use.kind === "organization" ? `r-${use.role}` : `p-${use.project!.id}`);

const faceOf = (use: ModelTierUse) =>
  use.kind === "organization"
    ? agentFace(use.role)
    : <ProjectAvatar key={faceKey(use)} project={use.project!} size={16} />;

/** One face per role and per project, in the order they are named. */
function faces(uses: readonly ModelTierUse[]) {
  const seen = new Set<string>();
  return uses.filter((u) => {
    const key = faceKey(u);
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  }).slice(0, 4).map(faceOf);
}

/** How a tier reaches the agent, explained once. */
function HowItWorks({ example, proxy }: { example: ModelTier | null; proxy: string | null }) {
  const name = example?.name ?? "Coder";
  const model = example?.model ?? "its model";
  return (
    <FlowSteps data-testid="models-explainer" steps={[
      { title: "An agent asks for a tier", children: <>Implementer → <code>{name}</code>, set in Agents or a project.</> },
      { title: "dude requests its model", children: <>{name} → <code>{model}</code>, set by an admin on this page and sent as-is on every call.</> },
      { title: "The proxy serves it", children: <>That model if it can, or a fallback from its own config{proxy ? <> at <code>{proxy}</code></> : null}. dude doesn’t see which.</> },
    ]} />
  );
}

export function ModelsPage({ client, orgName, tiers, problem, setTiers }: {
  client: ApiClient;
  orgName: string;
  tiers: ModelTiersResponse | null;
  problem: string | null;
  setTiers: (t: ModelTiersResponse) => void;
}) {
  const proxy = useProxyModels(client);
  const [editing, setEditing] = useState<{ tier: ModelTierWithUse | null; test: boolean } | null>(null);
  const [removing, setRemoving] = useState<ModelTierWithUse | null>(null);
  const { save, problem: saveProblem } = useSave();

  if (!tiers || problem) return <div className="centered">{problem ? <Callout tone="danger">{problem}</Callout> : <Spinner label="Loading…" />}</div>;
  const canEdit = tiers.canEdit;
  const example = tiers.tiers.find((t) => t.name === "Coder" && t.model) ?? tiers.tiers.find((t) => t.model) ?? null;

  return (
    <div className="settingsPage" data-testid="models-page">
      <SettingsHeader title="Models"
        description={`Agents are given a tier — ${tiers.tiers.slice(0, 3).map((t) => t.name).join(", ").replace(/, ([^,]*)$/, " or $1")} — not a model. Each tier names the model dude asks the LLM proxy for; change it here and every agent on that tier follows from its next session.`} />
      {saveProblem ? <Callout tone="danger">{saveProblem}</Callout> : null}
      {canEdit && tiers.upgrade.length ? (
        <UpgradeNotes tiers={tiers} onDone={() => void save(async () => setTiers(await client.dismissTierUpgrade()))} />
      ) : null}
      <HowItWorks example={example} proxy={proxy?.source ?? null} />

      <SettingsSection title="Tiers" data-testid="model-tiers"
        actions={<>
          <SettingsMeta>Only admins change these</SettingsMeta>
          {canEdit ? <Button size="sm" variant="secondary" leadingIcon="plus" onClick={() => setEditing({ tier: null, test: false })} data-testid="add-model-tier">Add tier</Button> : null}
        </>}>
        <Table>
          <THead>
            <Tr>
              <Th>Tier</Th>
              <Th>Requests</Th>
              <Th>Effort</Th>
              <Th hideWhenNarrow>Changed</Th>
              <Th hideWhenNarrow>Used by</Th>
              {canEdit ? <Th align="right" width="48px"><span className="ds-sr-only">Actions</span></Th> : null}
            </Tr>
          </THead>
          <TBody>
            {tiers.tiers.map((t) => (
              <TierRow key={t.id} tier={t} canEdit={canEdit} only={tiers.tiers.length <= 1}
                onEdit={(test) => setEditing({ tier: t, test })} onRemove={() => setRemoving(t)} />
            ))}
          </TBody>
        </Table>
      </SettingsSection>

      {editing ? (
        <TierDialog client={client} existing={editing.tier} testNow={editing.test} proxy={proxy}
          onClose={() => setEditing(null)} onSaved={setTiers} />
      ) : null}
      {removing ? (
        <RemoveTierDialog client={client} orgName={orgName} tier={removing} others={tiers.tiers.filter((t) => t.id !== removing.id)}
          onClose={() => setRemoving(null)} onRemoved={setTiers} />
      ) : null}
    </div>
  );
}

/** A tier's row: its mark and name, the model it requests, its effort, who changed it, who uses it, and an admin's menu. */
function TierRow({ tier: t, canEdit, only, onEdit, onRemove }: {
  tier: ModelTierWithUse;
  canEdit: boolean;
  /** The organisation's only tier, which cannot be removed. */
  only: boolean;
  onEdit: (test: boolean) => void;
  onRemove: () => void;
}) {
  const mark = tierMark(t);
  return (
    <Tr data-tier={t.name}>
      <Td wrap><TierLine icon={mark.icon} tone={mark.tone} name={t.name} description={t.description || undefined} /></Td>
      {t.model ? (
        <Td fit mono data-model-cell>{t.model}</Td>
      ) : (
        <Td fit data-model-cell data-unset><span className="tierUnset">Not set</span></Td>
      )}
      <Td fit muted={!t.effort} data-effort-cell>{tierEffortLabel(t.effort)}</Td>
      <Td fit muted hideWhenNarrow>
        {t.updatedBy ? `${t.updatedBy.name.split(" ")[0]} · ` : ""}{formatTimestamp(t.updatedAt, "relative")}
      </Td>
      <Td wrap hideWhenNarrow><UsedBy faces={faces(t.usedBy)}>{tierUsedByWords(t.usedBy)}</UsedBy></Td>
      {canEdit ? (
        <Td align="right">
          <RowMenu size="sm" label={`Actions for ${t.name}`} items={[
            { id: "edit", label: "Change model…", icon: "edit", onSelect: () => onEdit(false) },
            { id: "test", label: "Send a test message", icon: "send", onSelect: () => onEdit(true),
              disabled: !t.model, disabledReason: t.model ? undefined : "It names no model yet" },
            { kind: "separator" },
            { id: "remove", label: "Remove…", tone: "danger", icon: "cross", onSelect: onRemove,
              disabled: only, disabledReason: only ? "Every agent needs a tier" : undefined },
          ]} />
        </Td>
      ) : null}
    </Tr>
  );
}

// ---------------------------------------------------------------------------
// After the upgrade: what became of each role's model
// ---------------------------------------------------------------------------

/** What the upgrade changed that an admin should check, after the banner's first sentence; " " when nothing. */
function upgradeSummary(changed: number, made: number): string {
  const roles = changed ? `${changed === 1 ? "One role now asks" : `${changed} roles now ask`} for a different model than before` : "";
  const overrides = made ? `${made === 1 ? "one project override" : `${made} project overrides`} matched no tier and got ${made === 1 ? "its own" : "their own"}` : "";
  if (roles && overrides) return ` ${roles}, and ${overrides} — check them.`;
  if (roles || overrides) return ` ${roles || overrides} — check them.`;
  return " ";
}

function UpgradeNotes({ tiers, onDone }: { tiers: ModelTiersResponse; onDone: () => void }) {
  const lines = upgradeLines(tiers.upgrade);
  const changed = lines.filter((l) => l.modelChanged && !l.project).reduce((n, l) => n + l.roles.length, 0);
  const made = lines.filter((l) => l.newTier).length;
  const modelOf = (name: string) => tiers.tiers.find((t) => t.name === name)?.model ?? null;
  return (
    <Callout tone="info" data-testid="tier-upgrade">
      <FormStack>
        <span>
          <b>Agents now pick a tier.</b> Each role got its starting tier, and each tier asks for the model most of its roles already ran on.
          {upgradeSummary(changed, made)}
        </span>
        <Table density="compact" data-testid="tier-upgrade-notes">
          <TBody>
            {lines.map((l) => {
              const now = modelOf(l.tierName);
              return (
                <Tr key={l.key} data-note={l.oldModel}>
                  <Td fit>
                    <UsedBy faces={l.project
                      ? [<ProjectAvatar key="p" project={l.project} size={16} />]
                      : l.roles.map(agentFace)}>
                      <span className="ds-mono">{l.oldModel}</span>
                    </UsedBy>
                  </Td>
                  <Td muted wrap>{l.who ?? l.roles.map(roleLabel).join(", ")}</Td>
                  <Td align="right" fit>
                    → {l.newTier ? `New tier “${l.tierName}”` : l.tierName}
                    {l.modelChanged && now ? <> <span className="tierChanged">now {now}</span></> : null}
                  </Td>
                </Tr>
              );
            })}
          </TBody>
        </Table>
        <span><Button size="sm" variant="secondary" onClick={onDone} data-testid="tier-upgrade-done">Done</Button></span>
      </FormStack>
    </Callout>
  );
}

// ---------------------------------------------------------------------------
// Adding, changing and testing a tier
// ---------------------------------------------------------------------------

type TestState = { model: string; result: ModelTestResult } | "sending" | { error: string } | null;

/** The effort picker's value for the model's default (null). */
const DEFAULT_EFFORT = "__default__";
const EFFORT_OPTIONS = [
  { value: DEFAULT_EFFORT, label: tierEffortLabel(null) },
  ...TIER_EFFORTS.map((e) => ({ value: e, label: tierEffortLabel(e) })),
];

function submitLabel(existing: boolean, unlisted: boolean): string {
  if (existing) return unlisted ? "Save anyway" : "Save";
  return unlisted ? "Add tier anyway" : "Add tier";
}

function TierDialog({ client, existing, testNow, proxy, onClose, onSaved }: {
  client: ApiClient;
  existing: ModelTierWithUse | null;
  /** Send a test message as it opens. */
  testNow: boolean;
  proxy: ProxyModels | null;
  onClose: () => void;
  onSaved: (t: ModelTiersResponse) => void;
}) {
  const [draft, setDraft] = useState<TierDraft>(() => tierDraftOf(existing));
  const { busy, problem, save } = useSave();
  const [test, setTest] = useState<TestState>(null);
  const set = <K extends keyof TierDraft>(k: K, v: TierDraft[K]) => setDraft((d) => ({ ...d, [k]: v }));
  const problems = tierDraftProblems(draft);
  const model = draft.model.trim();
  const listed = proxy && !proxy.problem ? proxy.models : null;
  const known = model ? proxyKnows(model, listed) : null;
  const users = existing?.usedBy ?? [];
  const valid = Object.keys(problems).length === 0;
  const testable = Boolean(model) && !problems.model && !problems.options && !problems.headers;

  const sendTest = () => {
    if (!testable) return;
    const input = tierInput(draft);
    setTest("sending");
    client.testModel({ model, effort: input.effort, options: input.options, headers: input.headers })
      .then(setTest, (err: unknown) => setTest({ error: errorText(err) }));
  };
  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(() => { if (testNow) sendTest(); }, []);

  const submit = submitLabel(existing !== null, known === false);
  return (
    <FormDialog open onOpenChange={(open) => !open && onClose()} size="md"
      title={existing ? existing.name : "Add a tier"}
      description={existing ? `The model every agent on ${existing.name} requests from the proxy, and how hard it thinks.` : "A kind of work agents can be given. Pick it in Agents or a project."}
      submitLabel={submit} submitTestId="model-tier-save" canSubmit={!busy && valid} problem={problem}
      footerStart={
        <Button size="sm" variant="quiet" leadingIcon="check" onClick={sendTest} disabled={!testable || test === "sending"}
          data-testid="model-tier-test">
          {test === "sending" ? "Sending…" : "Send a test message"}
        </Button>
      }
      onSubmit={() => void save(async () => {
        const input = tierInput(draft);
        onSaved(existing ? await client.updateModelTier(existing.id, input) : await client.addModelTier(input));
        onClose();
      }, undefined, existing ? `${draft.name.trim()} saved` : `${draft.name.trim()} added`)}>
      {existing && users.length ? (
        <Callout tone="info" data-testid="model-tier-users">
          On {existing.name} now: <b>{users.map(tierUseName).join(", ")}</b>. They move from their next session;
          running sessions finish on {existing.model ? <code>{existing.model}</code> : "what they started with"}.
        </Callout>
      ) : null}
      <FormRow>
        <Input label="Name" autoFocus={!existing} value={draft.name} maxLength={TIER_NAME_MAX} onChange={(e) => set("name", e.target.value)}
          data-testid="model-tier-name" error={draft.name.trim() && problems.name ? problems.name : undefined} />
        <Input label="What it’s for" value={draft.description} maxLength={TIER_DESCRIPTION_MAX} onChange={(e) => set("description", e.target.value)}
          data-testid="model-tier-description" error={problems.description} />
      </FormRow>
      <Input label="Model to request" mono autoFocus={Boolean(existing)} value={draft.model} maxLength={TIER_MODEL_MAX} placeholder="Not set"
        onChange={(e) => set("model", e.target.value)} data-testid="model-tier-model"
        hint="Exactly as the proxy names it." error={model && problems.model ? problems.model : undefined} />
      {listed && listed.length ? (
        <FormStack>
          <SettingsMeta>Names the proxy knows</SettingsMeta>
          <NameChips label="Names the proxy knows" names={listed} value={model} onPick={(n) => set("model", n)} />
          <SettingsMeta><span>From <code>{proxy?.source ? `${proxy.source}/models` : "the proxy’s /v1/models"}</code>, as suggestions; typing any other name is fine.</span></SettingsMeta>
        </FormStack>
      ) : (
        <SettingsMeta>The proxy’s list of models could not be read{proxy?.problem ? ` (${proxy.problem})` : ""}; type the name as the proxy knows it.</SettingsMeta>
      )}
      {known === false ? (
        <Callout tone="attention" data-testid="model-tier-unlisted">
          The proxy doesn’t list <code>{model}</code>. If it serves it under another name, or not at all, sessions on this tier fail at their first call
          — send a test message to see what it answers.
        </Callout>
      ) : null}
      <Select label="Reasoning effort" aria-label="Reasoning effort" data-testid="model-tier-effort"
        value={draft.effort ?? DEFAULT_EFFORT} options={EFFORT_OPTIONS}
        onValueChange={(v) => set("effort", v === DEFAULT_EFFORT ? null : (v as TierEffort))}
        hint="None turns thinking off, so the chat shows none." />
      <SettingsDisclosure summary="Advanced: OpenCode model options and request headers">
        <FormStack>
          <Textarea label="OpenCode model options" mono rows={3} value={draft.options} placeholder={"{\"effort\": \"xhigh\"}"}
            onChange={(e) => set("options", e.target.value)} data-testid="model-tier-options"
            hint="A JSON object merged over what the effort sets; its keys win. Keys the provider does not know are dropped without a word."
            error={problems.options} />
          <Textarea label="Request headers" mono rows={2} value={draft.headers} placeholder={"{\"X-Team\": \"platform\"}"}
            onChange={(e) => set("headers", e.target.value)} data-testid="model-tier-headers"
            hint="A JSON object of header names to values, sent on every request." error={problems.headers} />
        </FormStack>
      </SettingsDisclosure>
      <TestOutcome test={test} />
    </FormDialog>
  );
}

function TestOutcome({ test }: { test: TestState }) {
  if (!test || test === "sending") return null;
  if ("error" in test) return <Callout tone="danger" data-testid="model-tier-test-result">{test.error}</Callout>;
  const r = test.result;
  return (
    <Callout tone={r.ok ? "success" : "danger"} data-testid="model-tier-test-result" data-ok={r.ok}>
      <code>{test.model}</code> {testResultWords(r)}.
    </Callout>
  );
}

// ---------------------------------------------------------------------------
// Removing a tier
// ---------------------------------------------------------------------------

function RemoveTierDialog({ client, orgName, tier, others, onClose, onRemoved }: {
  client: ApiClient;
  orgName: string;
  tier: ModelTierWithUse;
  others: ModelTier[];
  onClose: () => void;
  onRemoved: (t: ModelTiersResponse) => void;
}) {
  const { busy, problem, save } = useSave();
  const inUse = tier.usedBy.length > 0;
  const [to, setTo] = useState<string>(() => others[0]?.id ?? "");
  const options = useMemo(() => others.map((t) => ({ value: t.id, label: t.name, meta: t.model ?? "Not set" })), [others]);
  const names = tier.usedBy.map(tierUseName);
  const description = inUse
    ? `${names.length === 1 ? `${names[0]} uses it` : `${names.slice(0, -1).join(", ")} and ${names.at(-1)} use it`}. Choose the tier ${names.length === 1 ? "it gets" : "they get"} instead; nothing is left on a tier that is gone.`
    : "Nothing uses it.";
  return (
    <Dialog open onOpenChange={(open) => !open && onClose()} tone="danger" size="md" title={`Remove ${tier.name}?`} description={description}
      footer={
        <>
          <Button variant="quiet" onClick={onClose}>Cancel</Button>
          <Button variant="danger" solid disabled={busy || (inUse && !to)} data-testid="remove-model-tier"
            onClick={() => void save(async () => {
              onRemoved(await client.removeModelTier(tier.id, inUse ? to : null));
              onClose();
            }, undefined, `${tier.name} removed`)}>
            {inUse ? "Remove and move them" : "Remove"}
          </Button>
        </>
      }>
      <FormStack>
        {inUse ? (
          <Table density="compact" data-testid="model-tier-uses">
            <TBody>
              {tier.usedBy.map((u, i) => (
                <Tr key={i}>
                  <Td wrap><UsedBy faces={[faceOf(u)]}>{tierUseName(u)}</UsedBy></Td>
                  <Td align="right" muted fit>{tierUseWhere(u, orgName)}</Td>
                </Tr>
              ))}
            </TBody>
          </Table>
        ) : null}
        {inUse ? (
          <Select label="Move them to" aria-label="Move them to" value={to} onValueChange={setTo} options={options} data-testid="model-tier-move"
            hint={`Sessions running on ${tier.name} now finish on it; past sessions keep saying ${tier.name}.`} />
        ) : null}
        {problem ? <Callout tone="danger">{problem}</Callout> : null}
      </FormStack>
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// The tier field: an agent role's
// ---------------------------------------------------------------------------

/** What names no tier here: on a project, the organisation's. */
export const INHERIT_TIER = "__inherit__";

/** A tier as a picker's option: its mark and name, what it is for under it, and its model as muted meta. */
export function tierOption(t: ModelTier) {
  const mark = tierMark(t);
  return {
    value: t.id,
    label: <span className="tierOption"><TierMark icon={mark.icon} tone={mark.tone} size="sm" />{t.name}</span>,
    meta: <span className="ds-mono">{t.model ?? "Not set"}</span>,
    description: t.description || undefined,
  };
}
