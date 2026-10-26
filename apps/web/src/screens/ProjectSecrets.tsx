/**
 * Project settings → Servers → Secrets: environment variables given only
 * to the previews and agent roles each one names. Write-only: the API
 * shows a name, who gets it and the value's last characters, so a value
 * is typed once, here, and changed rather than edited. Who gets one and
 * its value change apart. The organisation's secrets follow the project's
 * own, read-only, each one the project holds a name of overridden.
 *
 * The dialogs serve Organisation settings → Secrets too (OrgSecrets.tsx):
 * there a secret may be ticked for nobody, and the ticks apply to every
 * project's agents.
 */

import { useRef, useState, type KeyboardEvent, type ReactNode } from "react";
import { formatTimestamp } from "@dude/design-system";
import { GivenTo, GivenToFieldset, OverriddenBy, SettingSource, SettingsNote, SettingsSection, TextButton } from "@dude/design-system/components";
import { Button, Callout, Dialog, EmptyState, FormActions, Input, RowMenu, SecretField, Table, TBody, Td, Th, THead, Tooltip, Tr, useToast } from "@dude/design-system/primitives";
import {
  DEFAULT_SECRET_RECIPIENTS, ORG_SECRET_NOBODY, SECRET_NAME_HELP, SECRET_RECIPIENT_OPTIONS, SECRET_RECIPIENTS_REQUIRED, SECRET_VALUE_HELP,
  SHORT_SECRET_WARNING, resolveSecrets, secretNameProblem, secretRecipientLabel, secretTooShortToRedact, secretValueProblem,
  type OrgSecret, type ProjectSecret, type RecipeEnvNames, type SecretRecipient,
} from "@dude/domain";
import type { ApiClient } from "../api/client.ts";
import { FormDialog, errorText } from "../hooks/useSave.tsx";
import "../secrets.css";

export const SECRETS_HELP = "Values the project's previews and agents need but the repository must not hold. Each one reaches only who you give it to, as an environment variable.";
export const SECRETS_NOTE = "Agent sessions and previews that start after you save get it; running ones keep what they have.";
export const GIVEN_TO_HINT = "Agent sessions and previews that start after you save get it.";
export const NO_TALKERS_NOTE = "Conductor and brainstorm never get secrets: they read text anyone can write, such as PR comments.";
export const REDACTION_NOTE = "dude replaces these values in agent output, artifacts it serves and work it pushes with [REDACTED:NAME], and refuses to push work holding one. That is defence in depth, not a guarantee: give agents dedicated, least-privilege credentials.";
export const EGRESS_NOTE = "Pair a secret with the project's network allowlist, such as *.snowflakecomputing.com, so an agent can use it only where it should.";
export const ORG_NONE_TICKED = "None ticked: only automations that pick it get it.";

export type Editing =
  | { kind: "add" }
  | { kind: "recipients"; secret: ProjectSecret }
  | { kind: "value"; secret: ProjectSecret }
  | { kind: "remove"; secret: ProjectSecret };

/**
 * A dialog's lifecycle: each one opened is a generation of its own; closing
 * one moves it on. A request belongs to the dialog that made it: once that
 * has closed, its answer may still refresh the list and toast, but it
 * closes, focuses and says nothing in a newer dialog. Busy and problem are
 * the current dialog's alone, so a newer dialog is never held by an older
 * request.
 */
export function useSecretDialogs(onChanged: () => void, focusAfter: () => void) {
  const [editing, setEditing] = useState<Editing | null>(null);
  const { toast } = useToast();
  const generation = useRef(0);
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);
  function open(next: Editing | null): void {
    generation.current++;
    setBusy(false);
    setProblem(null);
    setEditing(next);
  }
  const close = () => open(null);
  const save = async (action: () => Promise<unknown>, done: string) => {
    const mine = generation.current;
    const current = () => generation.current === mine;
    setBusy(true);
    setProblem(null);
    try {
      await action();
    } catch (err) {
      if (current()) {
        setBusy(false);
        setProblem(errorText(err));
      }
      return;
    }
    toast({ title: done, tone: "success" });
    onChanged();
    if (!current()) return;
    close();
    focusAfter();
  };
  return { editing, open, close, save, busy, problem };
}

/** The organisation's secrets a project's list shows after its own, and where they change. */
export interface InheritedSecrets {
  orgName: string;
  secrets: readonly OrgSecret[];
  /** Open Organisation settings → Secrets. */
  onManage?: (() => void) | undefined;
}

export function ProjectSecretsSection({ client, projectId, secrets, recipes, canEdit, onChanged, inherited }: {
  client: ApiClient;
  projectId: string;
  secrets: readonly ProjectSecret[];
  /** The project's servers: a name one of them sets in its env cannot be a secret. */
  recipes: readonly RecipeEnvNames[];
  canEdit: boolean;
  onChanged: () => void;
  inherited?: InheritedSecrets | undefined;
}) {
  const addButton = useRef<HTMLButtonElement>(null);
  const { editing, open, close, save, busy, problem } = useSecretDialogs(onChanged, () => addButton.current?.focus());
  const addAction = canEdit ? (
    <Button ref={addButton} variant="secondary" leadingIcon="plus" onClick={() => open({ kind: "add" })} data-testid="add-secret">Add secret</Button>
  ) : undefined;
  const rows = resolveSecrets(inherited?.secrets ?? [], secrets);
  const orgName = inherited?.orgName ?? "";

  return (
    <SettingsSection title="Secrets" id="secrets" data-testid="project-secrets">
      <SettingsNote icon="braces">{SECRETS_HELP}</SettingsNote>
      {canEdit ? null : <SettingsNote icon="info">Only project editors can change secrets.</SettingsNote>}
      {rows.length === 0 ? (
        <div data-testid="secrets-empty">
          <EmptyState compact icon="braces" title="No secrets"
            description="Add a value a preview or an agent needs but shouldn’t be in the repository: a seed script’s API key, a test database password."
            action={addAction} />
        </div>
      ) : (
        <>
          <Table density="compact" aria-label="Secrets" data-testid="secrets">
            <THead>
              <Tr>
                <Th width="40%">Name</Th>
                <Th>Given to</Th>
                <Th hideWhenNarrow width={88}>Value</Th>
                {canEdit ? <Th align="right" width="48px"><span className="ds-sr-only">Actions</span></Th> : null}
              </Tr>
            </THead>
            <TBody>
              {rows.map(({ source, secret: s, overridden }) => (
                <Tooltip key={`${source}:${s.name}`} side="bottom" content={changedWords(s)}>
                  <Tr data-secret={s.name} data-source={source} data-overridden={overridden ? "true" : undefined}>
                    <Td wrap>
                      <span className="secretName">
                        <span className={overridden ? "ds-mono secretStruck" : "ds-mono"}>{s.name}</span>
                        {source === "organization" ? <SettingSource source="organization" from={orgName} /> : null}
                      </span>
                    </Td>
                    <Td wrap>
                      {overridden ? <OverriddenBy name={s.name} />
                        : <GivenTo given={s.recipients.map(secretRecipientLabel)} none={source === "organization" ? ORG_SECRET_NOBODY : undefined} />}
                    </Td>
                    <Td mono muted hideWhenNarrow><span aria-label={`ends in ${s.hint}`}>…{s.hint}</span></Td>
                    {canEdit ? (
                      <Td align="right">
                        {source === "project" ? (
                          <RowMenu size="sm" label={`Actions for ${s.name}`} items={[
                            { id: "value", label: "Change value…", icon: "edit", onSelect: () => open({ kind: "value", secret: s }) },
                            { id: "recipients", label: "Change who gets it…", icon: "human", onSelect: () => open({ kind: "recipients", secret: s }) },
                            { kind: "separator" },
                            { id: "remove", label: "Remove", tone: "danger", onSelect: () => open({ kind: "remove", secret: s }) },
                          ]} />
                        ) : <span className="ds-sr-only">Read-only here: {orgName}&rsquo;s secret</span>}
                      </Td>
                    ) : null}
                  </Tr>
                </Tooltip>
              ))}
            </TBody>
          </Table>
          {addAction ? <FormActions>{addAction}</FormActions> : null}
        </>
      )}
      {inherited && inherited.secrets.length > 0 ? (
        <SettingsNote icon="layers">
          <span data-testid="inherited-note">
            {orgName}&rsquo;s secrets are changed in{" "}
            {inherited.onManage ? <TextButton onClick={inherited.onManage} data-testid="org-secrets-link">Organisation settings → Secrets</TextButton>
              : "Organisation settings → Secrets"}
            . A secret added here with the same name takes the place of {orgName}&rsquo;s in this project.
          </span>
        </SettingsNote>
      ) : null}
      <div className="projectSecretsNote" data-testid="secrets-note"><SettingsNote icon="info">{SECRETS_NOTE}</SettingsNote></div>
      <SettingsNote icon="lock">{REDACTION_NOTE}</SettingsNote>
      {editing?.kind === "add" ? (
        <AddSecretDialog taken={secrets.map((s) => s.name)} recipes={recipes} busy={busy} problem={problem} onClose={close}
          onSubmit={(name, value, recipients) => void save(() => client.addProjectSecret(projectId, name, value, recipients), `${name} added`)} />
      ) : null}
      {editing?.kind === "recipients" ? (
        <RecipientsDialog secret={editing.secret} busy={busy} problem={problem} onClose={close}
          onSubmit={(recipients) => void save(() => client.changeProjectSecretRecipients(projectId, editing.secret.name, recipients),
            recipientsDone(editing.secret.name, recipients))} />
      ) : null}
      {editing?.kind === "value" ? (
        <ChangeValueDialog secret={editing.secret} busy={busy} problem={problem} onClose={close}
          onSubmit={(value) => void save(() => client.replaceProjectSecret(projectId, editing.secret.name, value), `${editing.secret.name} value changed`)} />
      ) : null}
      <RemoveSecretDialog editing={editing} busy={busy} problem={problem} onClose={close}
        description="New previews and agent sessions won't get it. Ones already running keep it until they start again."
        onRemove={(name) => void save(() => client.removeProjectSecret(projectId, name), `${name} removed`)} />
    </SettingsSection>
  );
}

export function recipientsDone(name: string, recipients: readonly SecretRecipient[]): string {
  return recipients.length ? `${name} now given to ${recipients.map(secretRecipientLabel).join(", ")}` : `${name} now given to automations that pick it only`;
}

export function changedWords(s: ProjectSecret): string {
  const when = formatTimestamp(s.updatedAt, "relative");
  return s.updatedBy ? `Changed by ${s.updatedBy.name} · ${when}` : `Changed ${when}`;
}

/** Ctrl/⌘+Enter in the value submits: Enter there is a new line (or, masked, nothing). */
function submitOnModEnter(canSubmit: boolean, submit: () => void) {
  return (e: KeyboardEvent<HTMLDivElement>) => {
    const inValue = e.target instanceof HTMLTextAreaElement || (e.target instanceof HTMLInputElement && e.target.type === "password");
    if (e.key !== "Enter" || !(e.ctrlKey || e.metaKey) || !inValue) return;
    e.preventDefault();
    if (canSubmit) submit();
  };
}

/** A value's problem worth saying while typing: an empty one only disables the button. */
function valueError(value: string): string | undefined {
  return value === "" ? undefined : secretValueProblem(value) ?? undefined;
}

/** The value's hint, with the short-value warning when dude would not redact it. */
function valueHint(value: string): string {
  return secretTooShortToRedact(value) ? `${SECRET_VALUE_HELP} ${SHORT_SECRET_WARNING}` : SECRET_VALUE_HELP;
}

/** Whose secret a dialog writes: a project's needs a recipient; an organisation's may have none, and its ticks reach every project. */
export type SecretLevel = { kind: "project" } | { kind: "organization"; orgName: string };

const PROJECT_LEVEL: SecretLevel = { kind: "project" };

function Recipients({ level, value, onChange, disabled, after }: {
  level: SecretLevel; value: SecretRecipient[]; onChange: (v: SecretRecipient[]) => void; disabled: boolean; after?: ReactNode;
}) {
  const org = level.kind === "organization";
  return (
    <>
      <GivenToFieldset options={SECRET_RECIPIENT_OPTIONS} value={value} onChange={(v) => onChange(v as SecretRecipient[])} disabled={disabled}
        hint={org ? `${GIVEN_TO_HINT} Ticks apply to every ${level.orgName} project's agents.` : GIVEN_TO_HINT}
        error={!org && value.length === 0 ? SECRET_RECIPIENTS_REQUIRED : undefined} data-testid="secret-recipients" />
      {org && value.length === 0 ? <SettingsNote icon="zap">{ORG_NONE_TICKED}</SettingsNote> : null}
      <SettingsNote icon="info">{NO_TALKERS_NOTE}</SettingsNote>
      <SettingsNote icon="globe">{EGRESS_NOTE}</SettingsNote>
      {after}
    </>
  );
}

export function AddSecretDialog({ level = PROJECT_LEVEL, taken, recipes = [], busy, problem, onClose, onSubmit }: {
  level?: SecretLevel;
  /** Names already held at this level. */
  taken: readonly string[];
  recipes?: readonly RecipeEnvNames[];
  busy: boolean;
  problem: string | null;
  onClose: () => void;
  onSubmit: (name: string, value: string, recipients: SecretRecipient[]) => void;
}) {
  const org = level.kind === "organization" ? level.orgName : null;
  const [name, setName] = useState("");
  const [value, setValue] = useState("");
  const [recipients, setRecipients] = useState<SecretRecipient[]>(org ? [] : [...DEFAULT_SECRET_RECIPIENTS]);
  const nameProblem = name ? secretNameProblem(name, { secrets: taken, recipes })?.message : undefined;
  const valueProblem = valueError(value);
  const canSubmit = !busy && name !== "" && !nameProblem && value !== "" && !valueProblem && (org !== null || recipients.length > 0);
  const submit = () => onSubmit(name, value, recipients);
  return (
    <FormDialog open onOpenChange={(open) => !open && onClose()} title={org ? `Add a secret to ${org}` : "Add a secret"}
      description={org
        ? `Give this value to the agents that need it in every ${org} project, as an environment variable. Automations pick it on their own Secrets.`
        : "Give this value to the previews and agents that need it as an environment variable."}
      submitLabel="Add secret" submitTestId="secret-save" canSubmit={canSubmit} onSubmit={submit} problem={problem}
      onKeyDown={submitOnModEnter(canSubmit, submit)}>
      <Input label="Name" mono autoFocus value={name} placeholder={org ? "GH_TOKEN" : "SNOWFLAKE_PASSWORD"} autoComplete="off" autoCapitalize="characters" spellCheck={false}
        onChange={(e) => setName(e.target.value.trim())} error={nameProblem} hint={SECRET_NAME_HELP} data-testid="secret-name" />
      <SecretField label="Value" value={value} onChange={setValue} hint={valueHint(value)} error={valueProblem} placeholder="Paste the value" data-testid="secret-value" />
      <Recipients level={level} value={recipients} onChange={setRecipients} disabled={busy} />
    </FormDialog>
  );
}

export function RecipientsDialog({ level = PROJECT_LEVEL, secret, overriddenIn = [], busy, problem, onClose, onSubmit }: {
  level?: SecretLevel;
  secret: ProjectSecret;
  /** Projects holding a secret of this name: an organisation secret's ticks do not reach their agents. */
  overriddenIn?: readonly string[];
  busy: boolean;
  problem: string | null;
  onClose: () => void;
  onSubmit: (recipients: SecretRecipient[]) => void;
}) {
  const org = level.kind === "organization";
  const [recipients, setRecipients] = useState<SecretRecipient[]>([...secret.recipients]);
  const canSubmit = !busy && (org || recipients.length > 0);
  return (
    <FormDialog open onOpenChange={(open) => !open && onClose()} title={<>Who gets <span className="ds-mono">{secret.name}</span></>}
      description="Changing who gets it leaves its value as it is."
      submitLabel="Save" submitTestId="secret-recipients-save" canSubmit={canSubmit} onSubmit={() => onSubmit(recipients)} problem={problem}>
      {org ? <Input label="Name" mono readOnly tabIndex={-1} value={secret.name} hint={`Ends in ${secret.hint}.`} data-testid="secret-now" /> : null}
      <Recipients level={level} value={recipients} onChange={setRecipients} disabled={busy} after={overriddenIn.length ? (
        <SettingsNote icon="layers">
          <span data-testid="overridden-in">
            {overriddenIn.join(", ")} {overriddenIn.length === 1 ? "has" : "have"} its own <span className="ds-mono">{secret.name}</span>: agents there get that one, whatever is ticked here.
          </span>
        </SettingsNote>
      ) : null} />
    </FormDialog>
  );
}

export function ChangeValueDialog({ secret, busy, problem, onClose, onSubmit, description }: {
  secret: ProjectSecret;
  busy: boolean;
  problem: string | null;
  onClose: () => void;
  onSubmit: (value: string) => void;
  description?: string;
}) {
  const [value, setValue] = useState("");
  const valueProblem = valueError(value);
  const canSubmit = !busy && value !== "" && !valueProblem;
  const submit = () => onSubmit(value);
  return (
    <FormDialog open onOpenChange={(open) => !open && onClose()} title={<>Change <span className="ds-mono">{secret.name}</span></>}
      description={description ?? "Changing the value leaves who gets it as it is. Running previews get the new value when they next wake; agent sessions when they next start or resume."}
      submitLabel="Change value" submitTestId="secret-replace" canSubmit={canSubmit} onSubmit={submit} problem={problem}
      onKeyDown={submitOnModEnter(canSubmit, submit)}>
      <Input label="Name" mono readOnly tabIndex={-1} value={secret.name} hint={`Now ends in ${secret.hint}.`} data-testid="secret-now" />
      <SecretField label="New value" autoFocus value={value} onChange={setValue} hint={valueHint(value)} error={valueProblem} placeholder="Paste the value" data-testid="secret-value" />
    </FormDialog>
  );
}

export function RemoveSecretDialog({ editing, busy, problem, onClose, onRemove, description }: {
  editing: Editing | null;
  busy: boolean;
  problem: string | null;
  onClose: () => void;
  onRemove: (name: string) => void;
  description: string;
}) {
  return (
    <Dialog
      open={editing?.kind === "remove"}
      onOpenChange={(open) => !open && onClose()}
      tone="danger"
      size="sm"
      title={<>Remove <span className="ds-mono">{editing?.kind === "remove" ? editing.secret.name : ""}</span>?</>}
      description={description}
      footer={
        <>
          <Button variant="quiet" onClick={onClose}>Cancel</Button>
          <Button variant="danger" solid disabled={busy} data-testid="secret-remove" onClick={() => {
            if (editing?.kind === "remove") onRemove(editing.secret.name);
          }}>
            Remove
          </Button>
        </>
      }
    >
      {/* In the dialog, where the person who asked is looking: the row behind it is covered. */}
      {problem && editing?.kind === "remove" ? <Callout tone="danger">{problem}</Callout> : null}
    </Dialog>
  );
}
