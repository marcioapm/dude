/**
 * The pages the organization's settings and a project's share: an agent
 * role (its model tier, time limit, machine, whether it runs, and its prompt),
 * and delivery. On a project each value says where it comes from — "From
 * Acme", or "Overridden" with what it overrides and Reset — and a change
 * is stored as an override; on the organization it is the default every
 * project starts from.
 */

import { useEffect, useMemo, useState, type ReactNode } from "react";
import {
  AgentAvatar,
  FINDING_SEVERITY_SPECS,
  MarkdownDocument,
  PromptHistory,
  Segmented,
  SettingField,
  SettingFields,
  SettingRow,
  SettingSource,
  SettingsDisclosure,
  SettingsHeader,
  SettingsMeta,
  SettingsNote,
  SettingsSection,
  Switch,
  TextButton,
  Markdown,
} from "@dude/design-system/components";
import { formatTimestamp, plural } from "@dude/design-system";
import { Button, Callout, Checkbox, Dialog, Input, Select } from "@dude/design-system/primitives";
import {
  DEFAULT_HARNESS,
  HARNESSES,
  HARNESS_DESCRIPTION,
  HARNESS_LABEL,
  harnessMisfit,
  PROMPT_VARIABLES,
  findingSeveritySchema,
  REVIEWER_CATEGORIES,
  REVIEWER_CATEGORY_LABEL,
  SETTINGS_ROLE_DESCRIPTION,
  SETTINGS_ROLE_LABEL,
  type FullDeliveryPolicy,
  type Harness,
  type MachineSizeWithUse,
  type ModelTier,
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
import { deliveryPatch, deliveryValues, TIME_LIMITS, timeLimitLabel } from "../settings.ts";
import { tierEffortLabel } from "../tiers.ts";
import { MachineSelect } from "./MachinesSettings.tsx";
import { INHERIT_TIER, tierOption } from "./ModelsSettings.tsx";
import { ImageField, imageWords, type ImageChoices } from "../images.tsx";

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

export function RolePage({ scope, role, sizes, tiers, tiersProblem, images, onOpenRun, onManageSizes, onManageTiers, onManageImages }: {
  scope: SettingsScope;
  role: SettingsRole;
  /** The organisation's sizes, as the settings screen loaded them; null while loading. */
  sizes: readonly MachineSizeWithUse[] | null;
  /** The organisation's model tiers, as the settings screen loaded them; null while loading. */
  tiers: readonly ModelTier[] | null;
  /** Why the tiers could not be loaded. */
  tiersProblem?: string | null | undefined;
  /** The organisation's images, as the settings screen loaded them. */
  images?: ImageChoices | undefined;
  onOpenRun?: ((runId: string) => void) | undefined;
  /** Open the organisation's Machines page (its admins). */
  onManageSizes?: (() => void) | undefined;
  /** Open the organisation's Models page (its admins). */
  onManageTiers?: (() => void) | undefined;
  /** Open the organisation's Images page (its admins). */
  onManageImages?: (() => void) | undefined;
}) {
  const { settings } = scope;
  const r = settings.roles[role];
  const project = isProject(settings);
  const canEdit = settings.canEdit;
  const orgName = settings.organization.name;
  const [history, setHistory] = useState(false);

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
        <HarnessField scope={scope} role={role} tiers={tiers} />
        <TierField scope={scope} role={role} tiers={tiers} tiersProblem={tiersProblem ?? null} onManageTiers={onManageTiers} />
        {role === "conductor" ? null : (
          <SettingField
            label="Time limit without progress"
            source={<Source scope={scope} setting={r.timeLimitMinutes} reset={() => void set({ timeLimitMinutes: null }, "Time limit reset")} />}
          >
            <Select
              aria-label="Time limit without progress"
              disabled={!canEdit}
              value={String(r.timeLimitMinutes.value ?? NONE)}
              onValueChange={(v) => void set({ timeLimitMinutes: v === NONE ? null : Number(v) }, "Time limit saved")}
              options={TIME_LIMITS.map((m) => ({ value: m === null ? NONE : String(m), label: timeLimitLabel(m) }))}
              hint="After this long without progress, the owner is told. Runs are stopped at 4 hours."
            />
          </SettingField>
        )}
        <MachineField scope={scope} role={role} sizes={sizes} onManageSizes={onManageSizes} />
      </SettingFields>
      {images ? <RoleImageField scope={scope} role={role} images={images} onManageImages={onManageImages} /> : null}
      {role === "fixer" ? (
        <SettingsNote icon="info">The fixer runs on the implementer’s harness, tier, time limit, machine and image unless you give it its own.</SettingsNote>
      ) : null}
      <PromptSection scope={scope} role={role} onHistory={() => setHistory(true)} />
      {history ? (
        <HistoryDialog scope={scope} role={role} onClose={() => setHistory(false)} onOpenRun={onOpenRun} />
      ) : null}
    </>
  );
}

/**
 * The harness a role's sessions run on: OpenCode, Claude Code or Codex,
 * apart from the tier it asks for. Layered as the machine is. A harness
 * that cannot run the tier's model is warned of here and still saved: the
 * Run fails saying so when it is built.
 */
function HarnessField({ scope, role, tiers }: { scope: SettingsScope; role: SettingsRole; tiers: readonly ModelTier[] | null }) {
  const { settings } = scope;
  const h = settings.roles[role].harness;
  const project = isProject(settings);
  const orgName = settings.organization.name;
  const fixer = role === "fixer";
  const own = project ? (h.source === "project" ? h.value : null) : h.followsImplementer ? null : h.value;
  const inherited: Harness = h.followsImplementer ? h.value : project ? (h.organization ?? DEFAULT_HARNESS) : DEFAULT_HARNESS;
  const offersInherit = project || fixer;
  const inheritLabel = fixer ? "The implementer’s" : `From ${orgName}`;
  const set = (harness: Harness | null, done: string) => void scope.patch({ roles: { [role]: { harness } } }, done);
  const model = tiers?.find((t) => t.id === settings.roles[role].tier.value)?.model ?? null;
  const misfit = harnessMisfit(h.value, model);
  const options = [
    ...(offersInherit ? [{ value: INHERIT_HARNESS, label: `${inheritLabel} · ${HARNESS_LABEL[inherited]}` }] : []),
    ...HARNESSES.map((x) => ({ value: x, label: HARNESS_LABEL[x], description: HARNESS_DESCRIPTION[x] })),
  ];
  return (
    <SettingField
      label="Harness"
      htmlFor={`harness-${role}`}
      source={project ? (
        <SettingSource source={h.source} from={orgName} inherited={h.source === "project" ? HARNESS_LABEL[h.organization ?? DEFAULT_HARNESS] : undefined}
          onReset={settings.canEdit ? () => set(null, "Harness reset") : undefined} />
      ) : null}
    >
      <Select id={`harness-${role}`} aria-label="Harness" data-testid="role-harness" disabled={!settings.canEdit}
        value={own ?? (offersInherit ? INHERIT_HARNESS : DEFAULT_HARNESS)} options={options}
        hint="The coding agent that runs it. The model tier below says which model it asks for."
        onValueChange={(v) => set(v === INHERIT_HARNESS ? null : (v as Harness), v === INHERIT_HARNESS ? "Harness reset" : "Harness saved")} />
      {misfit ? <Callout tone="attention" data-testid="role-harness-misfit">{misfit}</Callout> : null}
    </SettingField>
  );
}

const INHERIT_HARNESS = "__inherit__";

/**
 * The model tier a role's sessions run on. A project stores only its
 * override; naming none follows the organisation's (or, for the fixer,
 * the implementer's). Under it, on the organisation, the model the tier
 * requests; on a project, where the value comes from and Reset.
 */
function TierField({ scope, role, tiers, tiersProblem, onManageTiers }: {
  scope: SettingsScope;
  role: SettingsRole;
  tiers: readonly ModelTier[] | null;
  tiersProblem: string | null;
  onManageTiers?: (() => void) | undefined;
}) {
  const { settings } = scope;
  const t = settings.roles[role].tier;
  const project = isProject(settings);
  const orgName = settings.organization.name;
  const fixer = role === "fixer";
  const byId = (id: string | null | undefined) => tiers?.find((x) => x.id === id) ?? null;
  // What this layer sets, and what naming none here resolves to.
  const own = project ? (t.source === "project" ? t.value : null) : t.followsImplementer ? null : t.value;
  const inherited = t.followsImplementer ? byId(t.value) : project ? byId(t.organization) : null;
  const offersInherit = project || fixer;
  const inheritLabel = fixer ? "The implementer’s" : `From ${orgName}`;
  const set = (tier: string | null, done: string) => void scope.patch({ roles: { [role]: { tier } } }, done);
  const current = byId(t.value);
  // The first option: what naming none here follows; on the organisation, a placeholder while no tier is set.
  let first: Array<{ value: string; label: string; description?: ReactNode; disabled?: boolean }> = [];
  if (offersInherit) {
    first = [{ value: INHERIT_TIER, label: `${inheritLabel}${inherited ? ` · ${inherited.name}` : ""}`,
      description: inherited ? <span className="ds-mono">{inherited.model ?? "Not set"}</span> : undefined }];
  } else if (own === null) {
    first = [{ value: INHERIT_TIER, label: "No tier", disabled: true }];
  }
  const options = [...first, ...(tiers ?? []).map(tierOption)];
  let footer: ReactNode = project ? `Tiers are ${orgName}’s — ask an admin to change one` : "Only admins change tiers.";
  if (onManageTiers) footer = <TextButton onClick={onManageTiers} data-testid="manage-tiers">Manage tiers in Models</TextButton>;
  let hint: ReactNode;
  if (!project && current) {
    hint = current.model
      ? <span data-testid="role-tier-requests">Requests <code>{current.model}</code> · {tierEffortLabel(current.effort).toLowerCase()}</span>
      : `${current.name} names no model yet`;
  }
  return (
    <SettingField
      label="Model"
      htmlFor={`tier-${role}`}
      source={project ? (
        <SettingSource source={t.source} from={orgName} inherited={t.source === "project" ? byId(t.organization)?.name : undefined}
          onReset={settings.canEdit ? () => set(null, "Model reset") : undefined} />
      ) : null}
    >
      {tiers ? (
        <Select id={`tier-${role}`} aria-label="Model" data-testid="role-tier" disabled={!settings.canEdit}
          value={own !== null && tiers.some((x) => x.id === own) ? own : INHERIT_TIER} options={options} footer={footer}
          hint={hint}
          onValueChange={(v) => set(v === INHERIT_TIER ? null : v, v === INHERIT_TIER ? "Model reset" : "Model saved")} />
      ) : (
        <Select aria-label="Model" disabled options={[{ value: "loading", label: "Loading…" }]} value="loading" />
      )}
      {tiersProblem ? (
        <Callout tone="danger" data-testid="role-tier-problem">{`${orgName}’s tiers could not be loaded: ${tiersProblem}`}</Callout>
      ) : null}
    </SettingField>
  );
}

/**
 * The machine size a role's sessions run on. A project stores only its
 * override; naming none follows the organisation's (or, for the fixer, the
 * implementer's), and on the organisation, the default size.
 */
function MachineField({ scope, role, sizes, onManageSizes }: { scope: SettingsScope; role: SettingsRole; sizes: readonly MachineSizeWithUse[] | null; onManageSizes?: (() => void) | undefined }) {
  const { settings } = scope;
  const ms = settings.roles[role].machineSize;
  const project = isProject(settings);
  const orgName = settings.organization.name;
  const fixer = role === "fixer";
  const byId = (id: string | null | undefined) => sizes?.find((s) => s.id === id) ?? null;
  const fallback = sizes?.find((s) => s.isDefault) ?? null;
  // What this layer sets, and what naming none here resolves to.
  const own = project ? (ms.source === "project" ? ms.value : null) : ms.followsImplementer ? null : ms.value;
  const inherited = (ms.followsImplementer ? byId(ms.value) : project ? byId(ms.organization) : null) ?? fallback;
  const inheritLabel = fixer ? "The implementer’s" : project ? `From ${orgName}` : "Default";
  const set = (machineSize: string | null, done: string) => void scope.patch({ roles: { [role]: { machineSize } } }, done);
  return (
    <SettingField
      label="Machine"
      htmlFor={`machine-${role}`}
      source={project ? (
        <SettingSource source={ms.source} from={orgName} inherited={ms.source === "project" ? (byId(ms.organization) ?? fallback)?.name : undefined}
          onReset={settings.canEdit ? () => set(null, "Machine reset") : undefined} />
      ) : null}
    >
      {sizes ? (
        <MachineSelect id={`machine-${role}`} testId="role-machine" sizes={sizes} value={own} inherited={inherited}
          inheritLabel={inheritLabel}
          inheritDescription={fixer ? "Runs on the implementer’s size" : project ? `Follows ${orgName}’s setting` : "Follows whichever size is the default"}
          disabled={!settings.canEdit}
          footer={project ? `Sizes are ${orgName}’s.` : onManageSizes
            ? <>CPUs · memory · disk. <TextButton onClick={onManageSizes} data-testid="manage-sizes">Manage sizes in Machines</TextButton></>
            : "CPUs · memory · disk. Only admins change sizes."}
          onChange={(id) => set(id, id ? "Machine saved" : "Machine reset")} />
      ) : (
        <Select aria-label="Machine" disabled options={[{ value: "loading", label: "Loading…" }]} value="loading" />
      )}
    </SettingField>
  );
}

/**
 * The image a role's sessions run in, over the project's image: a project
 * stores only its override, naming none follows the organisation's (for
 * the fixer, the implementer's), and none anywhere is the project's own
 * runtime image, then the organisation's default base.
 */
function RoleImageField({ scope, role, images, onManageImages }: { scope: SettingsScope; role: SettingsRole; images: ImageChoices; onManageImages?: (() => void) | undefined }) {
  const { settings } = scope;
  const img = settings.roles[role].image;
  const project = isProject(settings);
  const orgName = settings.organization.name;
  const fixer = role === "fixer";
  const own = project ? (img.source === "project" ? img.value : null) : img.followsImplementer ? null : img.value;
  const inherited = img.followsImplementer ? imageWords(images.images, img.value) : project ? imageWords(images.images, img.organization ?? null) : null;
  const noneLabel = fixer && inherited ? `The implementer’s · ${inherited}` : inherited ? `From ${orgName} · ${inherited}` : "The project’s runtime image";
  const set = (image: string | null, done: string) => void scope.patch({ roles: { [role]: { image } } }, done);
  return (
    <SettingsSection title="Image">
      <SettingRow label={`${SETTINGS_ROLE_LABEL[role]}’s image`} help={project ? `${SETTINGS_ROLE_LABEL[role]}, on this project. Without one, ${orgName}’s choice for it.` : "What this role works in, over each project’s runtime image. Without one, the project’s."}
        data-testid="role-image-row"
        source={project ? (
          <SettingSource source={img.source} from={orgName} inherited={img.source === "project" ? imageWords(images.images, img.organization ?? null) ?? "the project’s" : undefined}
            onReset={settings.canEdit ? () => set(null, "Image reset") : undefined} />
        ) : null}>
        <ImageField testId="role-image" label={`${SETTINGS_ROLE_LABEL[role]}’s image`} images={images.images} value={own} orgName={orgName}
          allowNone={fixer ? "The implementer’s" : project ? `From ${orgName}` : "The project’s runtime image"} noneLabel={noneLabel}
          disabled={!settings.canEdit} onManage={onManageImages}
          onChange={(id) => set(id, id ? "Image saved" : "Image reset")} />
      </SettingRow>
    </SettingsSection>
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
          <Segmented
            label="Prompt"
            size="sm"
            disabled={!settings.canEdit || editing}
            value={mode}
            onChange={(m) => {
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
              <b>{orgName}’s {SETTINGS_ROLE_LABEL[role].toLowerCase()} prompt</b> · {plural(org.body.split("\n").length, "line")} ·{" "}
              {mode === "add" ? `comes first; ${project.name}’s is added after it` : `what ${project.name}’s agents are told`}
            </>
          }
        >
          <Markdown source={org.body} variant="prompt" breaks />
        </SettingsDisclosure>
      ) : null}
      {project && mode === "inherit" ? null : (
        <MarkdownDocument
          key={`${role}-${mode}`}
          data-testid="prompt-document"
          breaks
          source={project ? (own?.mode === mode ? own.body : "") : org.body}
          emptyText={project ? `Nothing ${mode === "add" ? "added" : "here"} yet.` : "No prompt yet."}
          meta={project && own?.versions ? <SettingsMeta>{changedBy(own)} · {historyLink}</SettingsMeta> : undefined}
          defaultEditing={Boolean(project) && own?.mode !== mode && settings.canEdit}
          onEditingChange={setEditing}
          variables={PROMPT_VARIABLES}
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

  const orgName = settings.organization.name;
  // Memoised: PromptHistory diffs each version against the one before.
  const versions = useMemo(
    () =>
      history?.versions.map((v) => ({
        id: v.id,
        number: v.number,
        body: v.body,
        note: v.note,
        author: v.createdBy,
        when: formatTimestamp(v.createdAt, "datetime"),
        current: v.current,
        mode: v.mode === "replace" ? `replaces ${orgName}’s` : v.mode === "add" ? (v.body.trim() ? `adds to ${orgName}’s` : `uses ${orgName}’s`) : undefined,
        sessions: {
          count: v.sessions.count,
          recent: v.sessions.recent.map((s) => ({
            id: s.runId,
            label: `${s.taskKey} · ${s.taskTitle} · ${s.phase}`,
            onOpen: onOpenRun ? () => onOpenRun(s.runId) : undefined,
          })),
        },
      })),
    [history, orgName, onOpenRun],
  );

  const where = project ? project.name : orgName;
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
            versions={versions!}
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
    [form.maxReviewIterations, form.maxAttemptsPerFinding, form.parkAfterMinutes, form.conductorWarmMinutes, form.conductorEditLines,
      form.conductorEditFiles].every((n) => Number.isInteger(n) && n >= 1) &&
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
        {number("conductorWarmMinutes", 1, 1440, "After it answers in Chat, the conductor stays running this long; then the next message resumes it.", "Keep the conductor warm for")}
      </SettingsSection>
      <SettingsSection title="Conductor’s edits">
        {number("conductorEditLines", 1, 10_000, "Changed lines (added and removed) the conductor may publish at once; past it, it delegates to an implementer.", "Lines the conductor may change")}
        {number("conductorEditFiles", 1, 1000, "Files the conductor may change in one publish; past it, it delegates.", "Files the conductor may change")}
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
