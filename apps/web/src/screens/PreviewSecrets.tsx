/**
 * Project settings → Servers → Branch previews → Secrets: environment
 * variables every branch preview gets (never an agent). Write-only: the
 * API shows a name and the value's last characters, so a value is typed
 * once, here, and replaced rather than edited.
 */

import { useRef, useState, type KeyboardEvent } from "react";
import { formatTimestamp } from "@dude/design-system";
import { SettingRow, SettingsNote } from "@dude/design-system/components";
import { Button, Callout, Dialog, EmptyState, FormActions, Input, RowMenu, SecretField, Table, TBody, Td, Th, THead, Tooltip, Tr, useToast } from "@dude/design-system/primitives";
import { SECRET_NAME_HELP, SECRET_VALUE_HELP, secretNameProblem, secretValueProblem, type PreviewSecret, type RecipeEnvNames } from "@dude/domain";
import type { ApiClient } from "../api/client.ts";
import { FormDialog, errorText } from "../hooks/useSave.tsx";

export const SECRETS_HELP = "Environment variables every preview gets, in its servers and their setup scripts. Agents never see them.";
export const SECRETS_NOTE = "A new value reaches running previews when they next wake. A secret added or removed reaches previews started after the change; restart a preview to give it the change now.";

type Editing = { kind: "add" } | { kind: "replace"; secret: PreviewSecret } | { kind: "remove"; secret: PreviewSecret };

export function PreviewSecretsRow({ client, projectId, projectName, secrets, recipes, canEdit, onChanged }: {
  client: ApiClient;
  projectId: string;
  projectName: string;
  secrets: readonly PreviewSecret[];
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
    <SettingRow label="Secrets" help={SECRETS_HELP} block data-testid="preview-secrets">
      {secrets.length === 0 ? (
        <div data-testid="secrets-empty">
          <EmptyState compact icon="braces" title="No secrets"
            description="Add a value a preview needs but shouldn’t be in the repository: a seed script’s API key, a test payment key."
            action={addAction} />
        </div>
      ) : (
        <>
          <Table density="compact" aria-label="Secrets" data-testid="secrets">
            <THead>
              <Tr>
                <Th width="60%">Name</Th>
                <Th>Value</Th>
                {canEdit ? <Th align="right" width="48px"><span className="ds-sr-only">Actions</span></Th> : null}
              </Tr>
            </THead>
            <TBody>
              {secrets.map((s) => (
                <Tooltip key={s.name} side="bottom" content={changedWords(s)}>
                  <Tr data-secret={s.name}>
                    <Td mono>{s.name}</Td>
                    <Td mono muted><span aria-label={`ends in ${s.hint}`}>…{s.hint}</span></Td>
                    {canEdit ? (
                      <Td align="right">
                        <RowMenu size="sm" label={`Actions for ${s.name}`} items={[
                          { id: "replace", label: "Replace value", icon: "edit", onSelect: () => open({ kind: "replace", secret: s }) },
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
          <div className="previewSecretsNote" data-testid="secrets-note"><SettingsNote icon="info">{SECRETS_NOTE}</SettingsNote></div>
        </>
      )}
      {editing?.kind === "add" ? (
        <AddSecretDialog projectName={projectName} secrets={secrets} recipes={recipes} busy={busy} problem={problem} onClose={close}
          onSubmit={(name, value) => void save(() => client.addProjectSecret(projectId, name, value), `${name} added`)} />
      ) : null}
      {editing?.kind === "replace" ? (
        <ReplaceSecretDialog secret={editing.secret} busy={busy} problem={problem} onClose={close}
          onSubmit={(value) => void save(() => client.replaceProjectSecret(projectId, editing.secret.name, value), `${editing.secret.name} value replaced`)} />
      ) : null}
      <Dialog
        open={editing?.kind === "remove"}
        onOpenChange={(open) => !open && close()}
        tone="danger"
        size="sm"
        title={<>Remove <span className="ds-mono">{editing?.kind === "remove" ? editing.secret.name : ""}</span>?</>}
        description="New previews won’t get it. Previews already running keep it until they start a fresh run."
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
    </SettingRow>
  );
}

function changedWords(s: PreviewSecret): string {
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

function AddSecretDialog({ projectName, secrets, recipes, busy, problem, onClose, onSubmit }: {
  projectName: string;
  secrets: readonly PreviewSecret[];
  recipes: readonly RecipeEnvNames[];
  busy: boolean;
  problem: string | null;
  onClose: () => void;
  onSubmit: (name: string, value: string) => void;
}) {
  const [name, setName] = useState("");
  const [value, setValue] = useState("");
  const nameProblem = name ? secretNameProblem(name, { secrets: secrets.map((s) => s.name), recipes })?.message : undefined;
  const valueProblem = valueError(value);
  const canSubmit = !busy && name !== "" && !nameProblem && value !== "" && !valueProblem;
  const submit = () => onSubmit(name, value);
  return (
    <FormDialog open onOpenChange={(open) => !open && onClose()} title="Add a secret"
      description={`Every branch preview of ${projectName} gets it as an environment variable.`}
      submitLabel="Add secret" submitTestId="secret-save" canSubmit={canSubmit} onSubmit={submit} problem={problem}
      onKeyDown={submitOnModEnter(canSubmit, submit)}>
      <Input label="Name" mono autoFocus value={name} placeholder="SEED_LLM_KEY" autoComplete="off" autoCapitalize="characters" spellCheck={false}
        onChange={(e) => setName(e.target.value.trim())} error={nameProblem} hint={SECRET_NAME_HELP} data-testid="secret-name" />
      <SecretField label="Value" value={value} onChange={setValue} hint={SECRET_VALUE_HELP} error={valueProblem} placeholder="Paste the value" data-testid="secret-value" />
    </FormDialog>
  );
}

function ReplaceSecretDialog({ secret, busy, problem, onClose, onSubmit }: {
  secret: PreviewSecret;
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
    <FormDialog open onOpenChange={(open) => !open && onClose()} title={<>Replace <span className="ds-mono">{secret.name}</span></>}
      description="Running previews get the new value when they next wake."
      submitLabel="Replace" submitTestId="secret-replace" canSubmit={canSubmit} onSubmit={submit} problem={problem}
      onKeyDown={submitOnModEnter(canSubmit, submit)}>
      <Input label="Name" mono readOnly tabIndex={-1} value={secret.name} hint={`Now ends in ${secret.hint}.`} data-testid="secret-now" />
      <SecretField label="New value" autoFocus value={value} onChange={setValue} hint={SECRET_VALUE_HELP} error={valueProblem} placeholder="Paste the value" data-testid="secret-value" />
    </FormDialog>
  );
}
