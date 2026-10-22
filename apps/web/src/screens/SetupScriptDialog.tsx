/**
 * A repository's setup script, in a dialog of its own: editors change it
 * (a blank script saves as none), readers read it. The repository's own
 * dialog keeps its four fields.
 */

import { useMemo, useState } from "react";
import { CodeEditor, SizeMeter, textLines } from "@dude/design-system/components";
import { Button, Dialog } from "@dude/design-system/primitives";
import { formatBytes, Icon, plural } from "@dude/design-system";
import type { ApiClient, Repository } from "../api/client.ts";
import { FormDialog, useSave } from "../hooks/useSave.tsx";
import { checkSetupScript, lintSetupScript, SETUP_SCRIPT_MAX_BYTES, setupScriptBytes } from "../setupScript.ts";

// The runtime's bound on one script (SetupTimeout, orchestrator/internal/phases/setup.go).
const TIME_LIMIT = "15 minutes";

export const SETUP_SCRIPT_PLACEHOLDER = `# Runs once in each new checkout, before the agent starts. For example:
# install the repository's own pre-commit hook; dude's commit hook runs it
ln -sf ../../scripts/pre-commit "$DUDE_REPO_HOOKS_DIR/pre-commit"
npm ci --prefer-offline`;

export interface SetupScriptDialogProps {
  client: ApiClient;
  repository: Repository;
  /** May change it; otherwise it is read with Close. */
  canEdit: boolean;
  orgName: string;
  onClose: () => void;
  onSaved: () => void;
}

/** Mounted only while open, so each opening starts from the saved script. */
export function SetupScriptDialog({ client, repository, canEdit, orgName, onClose, onSaved }: SetupScriptDialogProps) {
  const saved = (repository.setupScript ?? "").replace(/\r\n?/g, "\n");
  const [text, setText] = useState(saved);
  const { busy, problem, save, clear } = useSave();
  const diagnostics = useMemo(() => lintSetupScript(text), [text]);
  const bypassLines = new Set(diagnostics.map((d) => d.line)).size;
  const bytes = setupScriptBytes(text);
  const check = checkSetupScript(text);
  const dirty = text !== saved;
  const lines = textLines(text);

  const editor = (
    <CodeEditor
      data-testid="setup-script-editor"
      aria-label={`Setup script for ${repository.name}`}
      language="shell"
      value={text}
      readOnly={!canEdit}
      onChange={(v) => {
        setText(v);
        clear();
      }}
      placeholder={canEdit ? SETUP_SCRIPT_PLACEHOLDER : undefined}
      diagnostics={diagnostics}
      minLines={10}
      maxLines={18}
      header={
        <>
          <span className="ds-mono">bash</span>
          <span>· runs once per new checkout</span>
          {canEdit ? null : <span>· read-only</span>}
        </>
      }
      footer={
        <>
          <span data-testid="setup-script-lines">{plural(lines, "line")}</span>
          {bypassLines ? (
            <span className="setupScriptWarnCount" data-testid="setup-script-bypass-count">
              <Icon name="warning" size={12} />
              {bypassLines === 1 ? "1 line bypasses hooks" : `${bypassLines} lines bypass hooks`}
            </span>
          ) : null}
          <span className="setupScriptSpacer" />
          <SizeMeter bytes={bytes} limit={SETUP_SCRIPT_MAX_BYTES} />
        </>
      }
    />
  );

  const notes = (
    <>
      {!check.ok ? (
        <p className="setupScriptOver" role="alert" data-testid="setup-script-invalid">
          {check.problem === "nul"
            ? "A setup script can’t contain NUL characters."
             : `${(bytes - SETUP_SCRIPT_MAX_BYTES).toLocaleString("en-US")} ${bytes - SETUP_SCRIPT_MAX_BYTES === 1 ? "byte" : "bytes"} over the ${formatBytes(SETUP_SCRIPT_MAX_BYTES)} limit. Keep long files in the repository and have the script use them.`}
        </p>
      ) : null}
      {diagnostics.length ? (
        <ul className="setupScriptWarnings" data-testid="setup-script-warnings">
          {diagnostics.map((d) => (
            <li key={`${d.line}:${d.from}`}>
              <Icon name="warning" size={12} />
              <span>Line {d.line}: {d.message}</span>
            </li>
          ))}
        </ul>
      ) : null}
      <p className="setupScriptHelp">
        Runs in <span className="ds-mono">{repository.name}/</span> as the agent, with the Run’s environment, before the agent starts.
        A resumed Run that keeps its checkout does not run it again. Time limit: {TIME_LIMIT}. Saved changes reach Runs started after.
      </p>
      <p className="setupScriptHelp" data-testid="setup-script-hooks-help">
        Install git hooks into <span className="ds-mono">$DUDE_REPO_HOOKS_DIR</span>, the checkout’s own hooks directory: dude’s commit hook runs them.
        <span className="ds-mono"> git rev-parse --git-path hooks</span> names dude’s directory, which the script cannot write.
      </p>
      <p className="setupScriptHelp setupScriptFails">
        <Icon name="clock" size={12} />
        If it fails or runs out of time, the Run fails and shows the script’s exit code and output.
      </p>
    </>
  );

  const title = `Setup script for ${repository.name}`;
  const description = (
    <>For {repository.name} · <span className="ds-mono">{repository.url}</span></>
  );

  if (!canEdit) {
    return (
      <Dialog
        open
        onOpenChange={(open) => !open && onClose()}
        size="lg"
        title={title}
        description={description}
        footer={<Button variant="secondary" onClick={onClose} data-testid="setup-script-close">Close</Button>}
      >
        <div className="setupScriptBody" data-testid="setup-script-dialog">
          {editor}
          {notes}
           <p className="setupScriptHelp">Only {orgName}’s admins and this project’s admins can change it.</p>
        </div>
      </Dialog>
    );
  }

  return (
    <FormDialog
      open
      onOpenChange={(open) => !open && onClose()}
      size="lg"
      title={title}
      description={description}
      submitLabel={check.ok && check.value === null && saved !== "" ? "Remove script" : "Save"}
      submitTestId="setup-script-save"
      canSubmit={!busy && dirty && check.ok}
      problem={problem}
      unsavedWords={dirty ? Math.max(1, text.split(/\s+/).filter(Boolean).length) : 0}
      discardTitle="Discard changes to the setup script?"
      discardDescription="Your changes to the script haven’t been saved."
      onSubmit={() => {
        if (!check.ok) return;
        const value = check.value;
        void save(
          () => client.updateRepository(repository.id, { setupScript: value }),
          () => {
            onClose();
            onSaved();
          },
          value === null ? `${repository.name}’s setup script removed` : `${repository.name}’s setup script saved`,
        );
      }}
    >
      <div className="setupScriptBody" data-testid="setup-script-dialog">
        {editor}
        {notes}
      </div>
    </FormDialog>
  );
}
