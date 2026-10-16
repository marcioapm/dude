/**
 * Project settings → Servers → Secrets: environment variables given only
 * to the previews and agent roles each one names. Write-only: the API
 * shows a name, who gets it and the value's last characters, so a value
 * is typed once, here, and changed rather than edited. Who gets one and
 * its value change apart.
 */

import { useRef, useState, type KeyboardEvent } from "react";
import { formatTimestamp } from "@dude/design-system";
import { GivenTo, GivenToFieldset, SettingsNote, SettingsSection } from "@dude/design-system/components";
import { Button, Callout, Dialog, EmptyState, FormActions, Input, RowMenu, SecretField, Table, TBody, Td, Th, THead, Tooltip, Tr, useToast } from "@dude/design-system/primitives";
import {
  DEFAULT_SECRET_RECIPIENTS, SECRET_NAME_HELP, SECRET_RECIPIENT_OPTIONS, SECRET_RECIPIENTS_REQUIRED, SECRET_VALUE_HELP, SHORT_SECRET_WARNING,
  secretNameProblem, secretRecipientLabel, secretTooShortToRedact, secretValueProblem,
  type ProjectSecret, type RecipeEnvNames, type SecretRecipient,
} from "@dude/domain";
import type { ApiClient } from "../api/client.ts";
import { FormDialog, errorText } from "../hooks/useSave.tsx";

export const SECRETS_HELP = "Values the project's previews and agents need but the repository must not hold. Each one reaches only who you give it to, as an environment variable.";
export const SECRETS_NOTE = "Agent sessions and previews that start after you save get it; running ones keep what they have.";
export const GIVEN_TO_HINT = "Agent sessions and previews that start after you save get it.";
export const NO_TALKERS_NOTE = "Conductor and brainstorm never get secrets: they read text anyone can write, such as PR comments.";
export const REDACTION_NOTE = "dude replaces these values in agent output, artifacts it serves and work it pushes with [REDACTED:NAME], and refuses to push work holding one. That is defence in depth, not a guarantee: give agents dedicated, least-privilege credentials.";
export const EGRESS_NOTE = "Pair a secret with the project's network allowlist, such as *.snowflakecomputing.com, so an agent can use it only where it should.";

type Editing =
  | { kind: "add" }
  | { kind: "recipients"; secret: ProjectSecret }
  | { kind: "value"; secret: ProjectSecret }
  | { kind: "remove"; secret: ProjectSecret };

export function ProjectSecretsSection({ client, projectId, secrets, recipes, canEdit, onChanged }: {
  client: ApiClient;
  projectId: string;
  secrets: readonly ProjectSecret[];
  /** The project's servers: a name one of them sets in its env cannot be a secret. */
  recipes: readonly RecipeEnvNames[];
  canEdit: boolean;
  onChanged: () => void;
}) {
  const [editing, setEditing] = useState<Editing | null>(null);
  const { toast } = useToast();
  // Each dialog opened is a generation of its own; closing one moves it on.
  // A request belongs to the dialog that made it: once that has closed, its
  // answer may still refresh the list and toast, but it closes, focuses
  // and says nothing in a newer dialog. Busy and problem are the current
  // dialog's alone, so a newer dialog is never held by an older request.
  const generation = useRef(0);
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);
  const addButton = useRef<HTMLButtonElement>(null);
  function open(next: Editing | null): void {
    generation.current++;
    setBusy(false);
    setProblem(null);
    setEditing(next);
  }
  function close(): void {
    open(null);
  }
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
    addButton.current?.focus();
  };
  const addAction = canEdit ? (
    <Button ref={addButton} variant="secondary" leadingIcon="plus" onClick={() => open({ kind: "add" })} data-testid="add-secret">Add secret</Button>
  ) : undefined;

  return (
    <SettingsSection title="Secrets" id="secrets" data-testid="project-secrets">
      <SettingsNote icon="braces">{SECRETS_HELP}</SettingsNote>
      {canEdit ? null : <SettingsNote icon="info">Only project editors can change secrets.</SettingsNote>}
      {secrets.length === 0 ? (
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
                <Th hideWhenNarrow>Value</Th>
                {canEdit ? <Th align="right" width="48px"><span className="ds-sr-only">Actions</span></Th> : null}
              </Tr>
            </THead>
            <TBody>
              {secrets.map((s) => (
                <Tooltip key={s.name} side="bottom" content={changedWords(s)}>
                  <Tr data-secret={s.name}>
                    <Td mono>{s.name}</Td>
                    <Td><GivenTo given={s.recipients.map(secretRecipientLabel)} /></Td>
                    <Td mono muted hideWhenNarrow><span aria-label={`ends in ${s.hint}`}>…{s.hint}</span></Td>
                    {canEdit ? (
                      <Td align="right">
                        <RowMenu size="sm" label={`Actions for ${s.name}`} items={[
                          { id: "value", label: "Change value…", icon: "edit", onSelect: () => open({ kind: "value", secret: s }) },
                          { id: "recipients", label: "Change who gets it…", icon: "human", onSelect: () => open({ kind: "recipients", secret: s }) },
                          { kind: "separator" },
                          { id: "remove", label: "Remove", tone: "danger", onSelect: () => open({ kind: "remove", secret: s }) },
                        ]} />
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
      <div className="projectSecretsNote" data-testid="secrets-note"><SettingsNote icon="info">{SECRETS_NOTE}</SettingsNote></div>
      <SettingsNote icon="lock">{REDACTION_NOTE}</SettingsNote>
      {editing?.kind === "add" ? (
        <AddSecretDialog secrets={secrets} recipes={recipes} busy={busy} problem={problem} onClose={close}
          onSubmit={(name, value, recipients) => void save(() => client.addProjectSecret(projectId, name, value, recipients), `${name} added`)} />
      ) : null}
      {editing?.kind === "recipients" ? (
        <RecipientsDialog secret={editing.secret} busy={busy} problem={problem} onClose={close}
          onSubmit={(recipients) => void save(() => client.changeProjectSecretRecipients(projectId, editing.secret.name, recipients),
            `${editing.secret.name} now given to ${recipients.map(secretRecipientLabel).join(", ")}`)} />
      ) : null}
      {editing?.kind === "value" ? (
        <ChangeValueDialog secret={editing.secret} busy={busy} problem={problem} onClose={close}
          onSubmit={(value) => void save(() => client.replaceProjectSecret(projectId, editing.secret.name, value), `${editing.secret.name} value changed`)} />
      ) : null}
      <Dialog
        open={editing?.kind === "remove"}
        onOpenChange={(open) => !open && close()}
        tone="danger"
        size="sm"
        title={<>Remove <span className="ds-mono">{editing?.kind === "remove" ? editing.secret.name : ""}</span>?</>}
        description="New previews and agent sessions won't get it. Ones already running keep it until they start again."
        footer={
          <>
            <Button variant="quiet" onClick={close}>Cancel</Button>
            <Button variant="danger" solid disabled={busy} data-testid="secret-remove" onClick={() => {
              if (editing?.kind !== "remove") return;
              const name = editing.secret.name;
              void save(() => client.removeProjectSecret(projectId, name), `${name} removed`);
            }}>
              Remove
            </Button>
          </>
        }
      >
        {/* In the dialog, where the person who asked is looking: the row behind it is covered. */}
        {problem && editing?.kind === "remove" ? <Callout tone="danger">{problem}</Callout> : null}
      </Dialog>
    </SettingsSection>
  );
}

function changedWords(s: ProjectSecret): string {
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

function Recipients({ value, onChange, disabled }: { value: SecretRecipient[]; onChange: (v: SecretRecipient[]) => void; disabled: boolean }) {
  return (
    <>
      <GivenToFieldset options={SECRET_RECIPIENT_OPTIONS} value={value} onChange={(v) => onChange(v as SecretRecipient[])} disabled={disabled}
        hint={GIVEN_TO_HINT} error={value.length === 0 ? SECRET_RECIPIENTS_REQUIRED : undefined} data-testid="secret-recipients" />
      <SettingsNote icon="info">{NO_TALKERS_NOTE}</SettingsNote>
      <SettingsNote icon="globe">{EGRESS_NOTE}</SettingsNote>
    </>
  );
}

function AddSecretDialog({ secrets, recipes, busy, problem, onClose, onSubmit }: {
  secrets: readonly ProjectSecret[];
  recipes: readonly RecipeEnvNames[];
  busy: boolean;
  problem: string | null;
  onClose: () => void;
  onSubmit: (name: string, value: string, recipients: SecretRecipient[]) => void;
}) {
  const [name, setName] = useState("");
  const [value, setValue] = useState("");
  const [recipients, setRecipients] = useState<SecretRecipient[]>([...DEFAULT_SECRET_RECIPIENTS]);
  const nameProblem = name ? secretNameProblem(name, { secrets: secrets.map((s) => s.name), recipes })?.message : undefined;
  const valueProblem = valueError(value);
  const canSubmit = !busy && name !== "" && !nameProblem && value !== "" && !valueProblem && recipients.length > 0;
  const submit = () => onSubmit(name, value, recipients);
  return (
    <FormDialog open onOpenChange={(open) => !open && onClose()} title="Add a secret"
      description="Give this value to the previews and agents that need it as an environment variable."
      submitLabel="Add secret" submitTestId="secret-save" canSubmit={canSubmit} onSubmit={submit} problem={problem}
      onKeyDown={submitOnModEnter(canSubmit, submit)}>
      <Input label="Name" mono autoFocus value={name} placeholder="SNOWFLAKE_PASSWORD" autoComplete="off" autoCapitalize="characters" spellCheck={false}
        onChange={(e) => setName(e.target.value.trim())} error={nameProblem} hint={SECRET_NAME_HELP} data-testid="secret-name" />
      <SecretField label="Value" value={value} onChange={setValue} hint={valueHint(value)} error={valueProblem} placeholder="Paste the value" data-testid="secret-value" />
      <Recipients value={recipients} onChange={setRecipients} disabled={busy} />
    </FormDialog>
  );
}

function RecipientsDialog({ secret, busy, problem, onClose, onSubmit }: {
  secret: ProjectSecret;
  busy: boolean;
  problem: string | null;
  onClose: () => void;
  onSubmit: (recipients: SecretRecipient[]) => void;
}) {
  const [recipients, setRecipients] = useState<SecretRecipient[]>([...secret.recipients]);
  const canSubmit = !busy && recipients.length > 0;
  return (
    <FormDialog open onOpenChange={(open) => !open && onClose()} title={<>Who gets <span className="ds-mono">{secret.name}</span></>}
      description="Changing who gets it leaves its value as it is."
      submitLabel="Save" submitTestId="secret-recipients-save" canSubmit={canSubmit} onSubmit={() => onSubmit(recipients)} problem={problem}>
      <Recipients value={recipients} onChange={setRecipients} disabled={busy} />
    </FormDialog>
  );
}

function ChangeValueDialog({ secret, busy, problem, onClose, onSubmit }: {
  secret: ProjectSecret;
  busy: boolean;
  problem: string | null;
  onClose: () => void;
  onSubmit: (value: string) => void;
}) {
  const [value, setValue] = useState("");
  const valueProblem = valueError(value);
  const canSubmit = !busy && value !== "" && !valueProblem;
  const submit = () => onSubmit(value);
  return (
    <FormDialog open onOpenChange={(open) => !open && onClose()} title={<>Change <span className="ds-mono">{secret.name}</span></>}
      description="Changing the value leaves who gets it as it is. Running previews get the new value when they next wake; agent sessions when they next start or resume."
      submitLabel="Change value" submitTestId="secret-replace" canSubmit={canSubmit} onSubmit={submit} problem={problem}
      onKeyDown={submitOnModEnter(canSubmit, submit)}>
      <Input label="Name" mono readOnly tabIndex={-1} value={secret.name} hint={`Now ends in ${secret.hint}.`} data-testid="secret-now" />
      <SecretField label="New value" autoFocus value={value} onChange={setValue} hint={valueHint(value)} error={valueProblem} placeholder="Paste the value" data-testid="secret-value" />
    </FormDialog>
  );
}
