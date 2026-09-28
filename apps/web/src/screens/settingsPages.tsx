/**
 * The pages the organization's settings and a project's share: an agent
 * role (its model, effort, time limit, whether it runs, and its prompt),
 * and delivery. On a project each value says where it comes from — "From
 * Acme", or "Overridden" with what it overrides and Reset — and a change
 * is stored as an override; on the organization it is the default every
 * project starts from.
 */

import { useEffect, useState, type ReactNode } from "react";
import {
  AgentAvatar,
  FINDING_SEVERITY_SPECS,
  MarkdownDocument,
  PromptHistory,
  SegmentedControl,
  SettingField,
  SettingFields,
  SettingRow,
  SettingSource,
  SettingsDisclosure,
  SettingsHeader,
  SettingsMeta,
  SettingsSection,
  Switch,
  TextButton,
  Markdown,
} from "@dude/design-system/components";
import { formatTimestamp } from "@dude/design-system";
import { Button, Callout, Checkbox, Dialog, Input, Select } from "@dude/design-system/primitives";
import {
  EFFORTS,
  findingSeveritySchema,
  REVIEWER_CATEGORIES,
  REVIEWER_CATEGORY_LABEL,
  SETTINGS_ROLE_DESCRIPTION,
  SETTINGS_ROLE_LABEL,
  type FullDeliveryPolicy,
  type ProjectPromptMode,
  type PromptHistory as PromptHistoryData,
  type PromptState,
  type Setting,
  type SettingsPatch,
  type SettingsResponse,
  type SettingsRole,
} from "@dude/domain";
import type { ApiClient } from "../api/client.ts";
import { errorText, useSave } from "../hooks/useSave.tsx";
import { deliveryPatch, deliveryValues, effortLabel, TIME_LIMITS, timeLimitLabel } from "../settings.ts";

/** Where these settings are: an organization's, or a project's over it. */
export interface SettingsScope {
  client: ApiClient;
  settings: SettingsResponse;
  /** Save a change and take the settings the server answers with. */
  patch: (patch: SettingsPatch, done: string) => Promise<void>;
  /** The settings after something else changed them (a prompt saved or restored). */
  replace: (settings: SettingsResponse) => void;
}

const isProject = (s: SettingsResponse) => Boolean(s.project);

/** A value's source, on a project; nothing on the organization. */
function Source<T>({ scope, setting, reset }: { scope: SettingsScope; setting: Setting<T>; reset: () => void }) {
  if (!isProject(scope.settings)) return null;
  return (
    <SettingSource
      source={setting.source}
      from={scope.settings.organization.name}
      onReset={scope.settings.canEdit ? reset : undefined}
    />
  );
}

// ---------------------------------------------------------------------------
// An agent role
// ---------------------------------------------------------------------------

const NONE = "__none__";

export function RolePage({ scope, role, onOpenRun }: { scope: SettingsScope; role: SettingsRole; onOpenRun?: ((runId: string) => void) | undefined }) {
  const { settings } = scope;
  const r = settings.roles[role];
  const project = isProject(settings);
  const canEdit = settings.canEdit;
  const orgName = settings.organization.name;
  const [model, setModel] = useState(r.model.value ?? "");
  const [history, setHistory] = useState(false);
  useEffect(() => setModel(r.model.value ?? ""), [r.model.value]);

  const set = (change: NonNullable<SettingsPatch["roles"]>[SettingsRole], done: string) =>
    scope.patch({ roles: { [role]: change } }, done);

  return (
    <>
      <SettingsHeader
        leading={<AgentAvatar role={role === "fixer" ? "implementer" : role} size="lg" />}
        title={SETTINGS_ROLE_LABEL[role]}
        description={SETTINGS_ROLE_DESCRIPTION[role]}
        actions={
          r.enabled ? (
            <span data-testid="role-enabled">
              <Switch
                checked={r.enabled.value}
                disabled={!canEdit}
                label={r.enabled.value ? (project ? `On in ${settings.project!.name}` : "On") : project ? `Off in ${settings.project!.name}` : "Off by default"}
                onCheckedChange={(on) => void set({ enabled: on }, on ? `${SETTINGS_ROLE_LABEL[role]} on` : `${SETTINGS_ROLE_LABEL[role]} off`)}
              />
            </span>
          ) : (
            <Switch checked disabled label="Always on" onCheckedChange={() => {}} />
          )
        }
      />
      {r.enabled && project ? (
        <Source scope={scope} setting={r.enabled} reset={() => void set({ enabled: null }, "Back to " + orgName + "’s")} />
      ) : null}
      <SettingFields>
        <SettingField
          label="Model"
          htmlFor={`model-${role}`}
          source={<Source scope={scope} setting={r.model} reset={() => void set({ model: null }, "Model reset")} />}
        >
          <Input
            id={`model-${role}`}
            mono
            value={model}
            disabled={!canEdit}
            placeholder={role === "fixer" ? "The implementer’s" : "provider/model"}
            data-testid="role-model"
            onChange={(e) => setModel(e.target.value)}
            onBlur={() => {
              const next = model.trim();
              if (next !== (r.model.value ?? "")) void set({ model: next || null }, "Model saved");
            }}
            onKeyDown={(e) => e.key === "Enter" && (e.target as HTMLInputElement).blur()}
          />
        </SettingField>
        <SettingField
          label="Reasoning effort"
          source={<Source scope={scope} setting={r.effort} reset={() => void set({ effort: null }, "Effort reset")} />}
        >
          <Select
            aria-label="Reasoning effort"
            disabled={!canEdit}
            value={r.effort.value ?? NONE}
            onValueChange={(v) => void set({ effort: v === NONE ? null : (v as (typeof EFFORTS)[number]) }, "Effort saved")}
            options={[{ value: NONE, label: effortLabel(null) }, ...EFFORTS.map((e) => ({ value: e, label: effortLabel(e) }))]}
          />
        </SettingField>
        <SettingField
          label="Time limit per session"
          source={<Source scope={scope} setting={r.timeLimitMinutes} reset={() => void set({ timeLimitMinutes: null }, "Time limit reset")} />}
        >
          <Select
            aria-label="Time limit per session"
            disabled={!canEdit}
            value={String(r.timeLimitMinutes.value ?? NONE)}
            onValueChange={(v) => void set({ timeLimitMinutes: v === NONE ? null : Number(v) }, "Time limit saved")}
            options={TIME_LIMITS.map((m) => ({ value: m === null ? NONE : String(m), label: timeLimitLabel(m) }))}
          />
        </SettingField>
      </SettingFields>
      <PromptSection scope={scope} role={role} onHistory={() => setHistory(true)} />
      {history ? (
        <HistoryDialog scope={scope} role={role} onClose={() => setHistory(false)} onOpenRun={onOpenRun} />
      ) : null}
    </>
  );
}

function changedBy(p: PromptState): ReactNode {
  if (!p.versionId) return "dude’s built-in prompt";
  const when = p.updatedAt ? formatTimestamp(p.updatedAt, "relative") : "";
  return `Last changed by ${p.updatedBy?.name ?? "dude"} · ${when}`;
}

function PromptSection({ scope, role, onHistory }: { scope: SettingsScope; role: SettingsRole; onHistory: () => void }) {
  const { settings, client } = scope;
  const r = settings.roles[role];
  const project = settings.project;
  const orgName = settings.organization.name;
  const org = r.prompt.organization;
  const own = r.prompt.project;
  const [problem, setProblem] = useState<string | null>(null);
  const [editing, setEditing] = useState(false);
  // A project's choice between adding, replacing and using the organization's
  // is made before its text: switching to "add" or "replace" opens the editor.
  const [mode, setMode] = useState<ProjectPromptMode>(own?.mode ?? "inherit");
  useEffect(() => setMode(own?.mode ?? "inherit"), [own?.mode]);

  async function save(body: string, chosen: ProjectPromptMode = mode) {
    setProblem(null);
    try {
      scope.replace(await client.savePrompt(role, project ? { projectId: project.id, mode: chosen, body } : { body }));
    } catch (err) {
      setProblem(errorText(err));
      throw err;
    }
  }

  const historyLink = (
    <TextButton onClick={onHistory} data-testid="prompt-history">
      History
    </TextButton>
  );
  return (
    <SettingsSection
      title="Prompt"
      actions={
        project ? (
          <SegmentedControl
            aria-label="Prompt"
            disabled={!settings.canEdit || editing}
            value={mode}
            onValueChange={(m) => {
              setMode(m);
              // Using the organization's needs no text: it is saved at once.
              if (m === "inherit") void save("", "inherit").catch(() => setMode(own?.mode ?? "inherit"));
            }}
            options={[
              { value: "add", label: `Add to ${orgName}’s` },
              { value: "replace", label: `Replace ${orgName}’s` },
              { value: "inherit", label: `Use ${orgName}’s` },
            ]}
          />
        ) : (
          <SettingsMeta>
            {changedBy(org)} · {historyLink}
          </SettingsMeta>
        )
      }
    >
      {problem ? <Callout tone="danger">{problem}</Callout> : null}
      {project && mode !== "replace" ? (
        <SettingsDisclosure
          summary={
            <>
              <b>{orgName}’s {SETTINGS_ROLE_LABEL[role].toLowerCase()} prompt</b> · {org.body.split("\n").length} lines ·{" "}
              {mode === "add" ? `comes first; ${project.name}’s is added after it` : `what ${project.name}’s agents are told`}
            </>
          }
        >
          <Markdown source={org.body} variant="document" />
        </SettingsDisclosure>
      ) : null}
      {project && mode === "inherit" ? null : (
        <MarkdownDocument
          key={`${role}-${mode}`}
          data-testid="prompt-document"
          source={project ? (own?.mode === mode ? own.body : "") : org.body}
          empty={project ? `Nothing ${mode === "add" ? "added" : "here"} yet.` : "No prompt yet."}
          meta={project && own?.versions ? <SettingsMeta>{changedBy(own)} · {historyLink}</SettingsMeta> : undefined}
          defaultEditing={Boolean(project) && own?.mode !== mode && settings.canEdit}
          onEditingChange={setEditing}
          onSave={settings.canEdit ? (body) => save(body) : undefined}
        />
      )}
    </SettingsSection>
  );
}

function HistoryDialog({ scope, role, onClose, onOpenRun }: {
  scope: SettingsScope;
  role: SettingsRole;
  onClose: () => void;
  onOpenRun?: ((runId: string) => void) | undefined;
}) {
  const { client, settings } = scope;
  const project = settings.project;
  const [history, setHistory] = useState<PromptHistoryData | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const load = () => client.promptHistory(role, project?.id).then(setHistory, (err: unknown) => setProblem(errorText(err)));
  useEffect(() => {
    void load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [role, project?.id]);

  const where = project ? project.name : settings.organization.name;
  return (
    <Dialog
      open
      onOpenChange={(open) => !open && onClose()}
      size="xl"
      title={`${SETTINGS_ROLE_LABEL[role]} prompt · history`}
      description={history ? `${where} · ${history.versions.length} ${history.versions.length === 1 ? "version" : "versions"} · every save is kept` : where}
    >
      {problem ? <Callout tone="danger">{problem}</Callout> : null}
      {history ? (
        <div data-testid="prompt-history-dialog">
          <PromptHistory
            emptyText={project ? `${project.name} has not saved a prompt of its own.` : "Never edited: agents are told dude’s built-in prompt."}
            versions={history.versions.map((v) => ({
              id: v.id,
              number: v.number,
              body: v.body,
              note: v.note,
              author: v.createdBy,
              when: formatTimestamp(v.createdAt, "datetime"),
              current: v.current,
              mode: v.mode === "replace" ? `replaces ${settings.organization.name}’s` : v.mode === "add" ? (v.body.trim() ? `adds to ${settings.organization.name}’s` : `uses ${settings.organization.name}’s`) : undefined,
              sessions: {
                count: v.sessions.count,
                recent: v.sessions.recent.map((s) => ({
                  id: s.runId,
                  label: `${s.taskKey} · ${s.taskTitle} · ${s.phase}`,
                  onOpen: onOpenRun ? () => onOpenRun(s.runId) : undefined,
                })),
              },
            }))}
            onRestore={
              settings.canEdit
                ? async (id) => {
                    try {
                      scope.replace(await client.restorePrompt(id));
                      await load();
                    } catch (err) {
                      setProblem(errorText(err));
                    }
                  }
                : undefined
            }
          />
        </div>
      ) : null}
    </Dialog>
  );
}

// ---------------------------------------------------------------------------
// Delivery
// ---------------------------------------------------------------------------

const SEVERITIES = findingSeveritySchema.options;

export function DeliveryPage({ scope }: { scope: SettingsScope }) {
  const { settings } = scope;
  const d = settings.delivery;
  const canEdit = settings.canEdit;
  const [form, setForm] = useState<FullDeliveryPolicy>(() => deliveryValues(d));
  const { busy, problem, save } = useSave();
  useEffect(() => setForm(deliveryValues(d)), [d]);

  const changes = deliveryPatch(d, form);
  const dirty = Object.keys(changes).length > 0;
  const setField = <K extends keyof FullDeliveryPolicy>(k: K, v: FullDeliveryPolicy[K]) => setForm((f) => ({ ...f, [k]: v }));
  const toggle = <K extends "requiredReviewers" | "blockingSeverities">(k: K, v: string, on: boolean) =>
    setField(k, (on ? [...form[k], v] : form[k].filter((x) => x !== v)) as FullDeliveryPolicy[K]);
  const number = (k: keyof FullDeliveryPolicy, min: number, max: number, help: string, label: string) => (
    <SettingRow
      label={label}
      help={help}
      htmlFor={`delivery-${k}`}
      source={<Source scope={scope} setting={d[k] as Setting<unknown>} reset={() => void scope.patch({ delivery: { [k]: null } }, `${label} reset`)} />}
    >
      <Input
        id={`delivery-${k}`}
        type="number"
        min={min}
        max={max}
        disabled={!canEdit}
        style={{ width: 96 }}
        value={String(form[k])}
        data-testid={`delivery-${k}`}
        onChange={(e) => setField(k, Number(e.target.value) as never)}
      />
    </SettingRow>
  );
  const valid =
    form.requiredReviewers.length > 0 &&
    form.blockingSeverities.length > 0 &&
    [form.maxReviewIterations, form.maxAttemptsPerFinding, form.parkAfterMinutes].every((n) => Number.isInteger(n) && n >= 1) &&
    [form.maxPrFixIterations, form.idleNudgeMinutes].every((n) => Number.isInteger(n) && n >= 0);

  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        void save(() => scope.patch({ delivery: changes }, "Delivery saved"));
      }}
      data-testid="delivery-form"
      className="settingsForm"
    >
      <SettingsHeader title="Delivery" description="How a task goes from Deliver to a pull request." />
      <SettingsSection title="Review">
        <SettingRow
          label="Reviewers every delivery runs"
          help="Others join when a change touches their area."
          source={<Source scope={scope} setting={d.requiredReviewers} reset={() => void scope.patch({ delivery: { requiredReviewers: null } }, "Reviewers reset")} />}
        >
          {REVIEWER_CATEGORIES.map((c) => (
            <Checkbox key={c} label={REVIEWER_CATEGORY_LABEL[c]} disabled={!canEdit} checked={form.requiredReviewers.includes(c)}
              onCheckedChange={(on) => toggle("requiredReviewers", c, on === true)} />
          ))}
        </SettingRow>
        <SettingRow
          label="Findings that send it back"
          help="Lower severities go into the pull request for a person to weigh."
          source={<Source scope={scope} setting={d.blockingSeverities} reset={() => void scope.patch({ delivery: { blockingSeverities: null } }, "Severities reset")} />}
        >
          {SEVERITIES.map((s) => (
            <Checkbox key={s} label={FINDING_SEVERITY_SPECS[s].label} disabled={!canEdit} checked={form.blockingSeverities.includes(s)}
              onCheckedChange={(on) => toggle("blockingSeverities", s, on === true)} />
          ))}
        </SettingRow>
      </SettingsSection>
      <SettingsSection title="Loop">
        {number("maxReviewIterations", 1, 20, "Review → fix cycles before the owner is asked.", "Review rounds")}
        {number("maxAttemptsPerFinding", 1, 20, "Fixes one finding may survive before it is raised on its own.", "Attempts per finding")}
        {number("maxPrFixIterations", 0, 20, "Fixes for pull request comments before the owner is asked.", "Pull request fix rounds")}
        <SettingRow
          label="Simplify pass"
          help="After review, before the pull request."
          source={<Source scope={scope} setting={d.simplify} reset={() => void scope.patch({ delivery: { simplify: null } }, "Simplify reset")} />}
        >
          <Switch checked={form.simplify} disabled={!canEdit} label={form.simplify ? "On" : "Off"} onCheckedChange={(v) => setField("simplify", v)} />
        </SettingRow>
        <SettingRow
          label="Browser test"
          help="A tester starts the app, uses the change as a person would, and records a video. Say how to start the app in the tester’s prompt."
          source={<Source scope={scope} setting={d.test} reset={() => void scope.patch({ delivery: { test: null } }, "Browser test reset")} />}
        >
          <span data-testid="delivery-test">
            <Switch checked={form.test} disabled={!canEdit} label={form.test ? "On" : "Off"} onCheckedChange={(v) => setField("test", v)} />
          </span>
        </SettingRow>
      </SettingsSection>
      <SettingsSection title="Waiting on people">
        {number("parkAfterMinutes", 1, 1440, "An agent waiting on a person gives up its machine after this many minutes.", "Park an agent after")}
        {number("idleNudgeMinutes", 0, 1440, "Mid-turn, silent, running nothing: it is asked to carry on or ask. 0 never.", "Nudge a quiet agent after")}
      </SettingsSection>
      {problem ? <Callout tone="danger">{problem}</Callout> : null}
      {!valid ? <Callout tone="danger">Choose at least one reviewer and one blocking severity, and whole numbers in range.</Callout> : null}
      {canEdit ? (
        <Button type="submit" variant="primary" disabled={busy || !dirty || !valid} data-testid="delivery-save">
          Save
        </Button>
      ) : null}
    </form>
  );
}
