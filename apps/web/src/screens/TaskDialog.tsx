/**
 * Create or edit a task: what it asks for, and where it sits.
 *
 * One dialog for both, because they are the same fields. It is a document
 * being written: the title as its heading, the goal and the acceptance
 * criteria as Markdown, and where it sits (epic, repositories) beside them
 * with help for writing a good one. Criteria are edited as one Markdown list
 * and saved as the list of strings the API keeps (`criteria.ts`).
 *
 * Once a delivery has started, what the task asks for (title, goal,
 * criteria, repository) is fixed — agents are working to it — and only
 * where it sits (its epic) can change; the fields say so rather than failing
 * on save.
 *
 * Saving needs a title and a goal of at least TASK_GOAL_MIN characters,
 * trimmed, as the API does; a task saved before that rule opens as it is,
 * and saves once its goal is long enough.
 *
 * Read shows the whole task as one document, as it stands (saved or not),
 * in place of the fields; Back to writing, Escape or Ctrl/⌘+Shift+R return
 * to them as they were.
 *
 * Mounted by its opener only while open, so each opening starts from the
 * task (or empty).
 */

import { useEffect, useMemo, useState } from "react";
import { AttachDropZone, AttachmentChip, Badge, Callout, Breadcrumb, Button, Checkbox, Fieldset, FormStack, HelpList, Input, KeyHint, Markdown, MarkdownCheatsheet, MarkdownEditor, Select, Skeleton, Tooltip, attachmentWarning } from "@dude/design-system";
import { TASK_CRITERIA_MAX, TASK_GOAL_MAX, taskGoalShortBy } from "@dude/domain";
import type { ApiClient, Epic, Repository, TaskDetail, TaskFields, TaskRepository } from "../api/client.ts";
import { unsavedWords } from "../hooks/discard.ts";
import { errorText, FormDialog, useSave } from "../hooks/useSave.tsx";
import { criteriaFromMarkdown, criteriaToMarkdown } from "./criteria.ts";
import { limitsHint, useAttachmentLimits, useImageTray, type ImageTray } from "../hooks/useImages.tsx";

const NO_EPIC = "__none__";
// Read. Reload (Ctrl/⌘+R's family) is not one of the keys a browser keeps from a page, so the dialog takes it.
const READ_KEYS = ["mod", "Shift", "R"];
const isReadKey = (e: { key: string; ctrlKey: boolean; metaKey: boolean; shiftKey: boolean; altKey: boolean }) =>
  (e.ctrlKey || e.metaKey) && e.shiftKey && !e.altKey && e.key.toLowerCase() === "r";

/** Past the editor's limit (text that arrived longer than it, not typed): what to do about it. */
function overBy(value: string, max: number): string | undefined {
  const over = value.length - max;
  return over > 0 ? `${over.toLocaleString("en-US")} ${over === 1 ? "character" : "characters"} over the limit; shorten it to save.` : undefined;
}

/** Under the shortest goal a task is saved with: how much more to write. */
function shortGoalText(goal: string): string | undefined {
  const short = taskGoalShortBy(goal);
  return short > 0 ? `${short} more ${short === 1 ? "character" : "characters"} to save: why it matters and what should change.` : undefined;
}

export type ExistingTask = { id: string; delivering: boolean } & TaskFields;

/** A task as the dialog edits it; `delivering` fixes what it asks for. */
export function existingTask(item: TaskDetail, delivering: boolean): ExistingTask {
  const { id, title, goal, acceptanceCriteria, epicId, repositories } = item;
  return { id, delivering, title, goal, acceptanceCriteria, epicId, repositories };
}

export interface TaskDialogProps {
  client: ApiClient;
  projectId: string;
  onClose: () => void;
  /** Editing this task; omitted to create one. */
  existing?: ExistingTask | undefined;
  /** Prefilled epic when creating from an epic's board. */
  epicId?: string | null | undefined;
  onSaved: (id: string, deliver: boolean) => void;
}

export function TaskDialog({ client, projectId, onClose, existing, epicId, onSaved }: TaskDialogProps) {
  const [opened] = useState(() => [existing?.title ?? "", existing?.goal ?? "", criteriaToMarkdown(existing?.acceptanceCriteria ?? [])] as const);
  const [title, setTitle] = useState(opened[0]);
  const [goal, setGoal] = useState(opened[1]);
  const [goalTyped, setGoalTyped] = useState(false);
  const [criteriaSource, setCriteriaSource] = useState(opened[2]);
  const [epic, setEpic] = useState<string>(existing?.epicId ?? epicId ?? NO_EPIC);
  const [chosen, setChosen] = useState<TaskRepository[]>(existing?.repositories ?? []);
  // The choices arrive after it opens; until they have, a save could miss
  // a repository the project needs named.
  const [choices, setChoices] = useState<{ projectName: string; epics: Epic[]; repositories: Repository[] } | null>(null);
  const [loadProblem, setLoadProblem] = useState<string | null>(null);
  // Created, but its delivery did not start: a retry delivers it rather
  // than creating a second.
  const [created, setCreated] = useState<string | null>(null);
  const { busy, problem, save } = useSave();
  const [reading, setReading] = useState(false);
  // Images for the first agent, with the prompt: made now, uploaded once the task exists.
  const limits = useAttachmentLimits(client);
  const tray = useImageTray(client, undefined, limits);
  const withImages = !existing && tray.attachments.length > 0;
  const imagesReady = tray.attachments.every((a) => a.state === "ready");

  useEffect(() => {
    let current = true;
    void Promise.all([client.listEpics(projectId), client.getProject(projectId)]).then(
      ([{ epics }, project]) => current && setChoices({ projectName: project.name, epics, repositories: project.repositories }),
      (err: unknown) => current && setLoadProblem(errorText(err)),
    );
    return () => {
      current = false;
    };
  }, [client, projectId]);

  // A file dropped on the backdrop, outside the dialog's zone, would have the browser open it and lose the draft.
  useEffect(() => {
    const keep = (e: DragEvent) => {
      if (e.defaultPrevented || !Array.from(e.dataTransfer?.types ?? []).includes("Files")) return;
      e.preventDefault();
      if (e.type === "dragover" && e.dataTransfer) e.dataTransfer.dropEffect = "none";
    };
    window.addEventListener("dragover", keep);
    window.addEventListener("drop", keep);
    return () => {
      window.removeEventListener("dragover", keep);
      window.removeEventListener("drop", keep);
    };
  }, []);

  const locked = existing?.delivering ?? false;
  const repositories = choices?.repositories ?? [];
  // One repository needs no choosing: it is where the work goes unless the
  // task says otherwise.
  const choosing = repositories.length > 1;
  const criteria = useMemo(() => criteriaFromMarkdown(criteriaSource), [criteriaSource]);
  // Too short says how much more only once the person has typed in the goal:
  // before that, an empty or old short goal only says "required".
  const goalShortText = locked ? undefined : shortGoalText(goal);
  const goalError = locked ? undefined : overBy(goal, TASK_GOAL_MAX) ?? (goalTyped ? goalShortText : undefined);
  // Reading criteria only strips markers and indentation, so bounding their
  // Markdown source also bounds the total saved by the server.
  const criteriaError = locked ? undefined : overBy(criteriaSource, TASK_CRITERIA_MAX);
  const canSave = choices !== null && Boolean(title.trim()) && !goalError && !goalShortText && !criteriaError && !busy && imagesReady;
  const unsaved = locked ? 0 : unsavedWords(opened, [title, goal, criteriaSource]);

  function submit(deliver: boolean) {
    const fields: Partial<TaskFields> = { epicId: epic === NO_EPIC ? null : epic };
    if (!locked) {
      Object.assign(fields, {
        title: title.trim(),
        goal: goal.trim(),
        acceptanceCriteria: criteria.items,
        // One repository needs no choosing, but is named: adding a second
        // later must not leave this work with no checkout.
        repositories: choosing ? chosen : repositories.map((r) => ({ id: r.id, access: "write" as const })),
      });
    }
    let id = existing?.id ?? created ?? "";
    void save(
      async () => {
        if (existing) await client.updateTask(existing.id, fields);
        else if (!created) {
          id = (await client.createTask({ projectId, title: title.trim(), ...fields })).id;
          setCreated(id);
        }
        if (deliver) await client.deliver(id, withImages ? await tray.uploadTo(id) : []);
      },
      () => {
        onClose();
        onSaved(id, deliver);
      },
    );
  }

  const epicTitle = choices?.epics.find((e) => e.id === epic)?.title;
  // Each part parsed on its own, so an unclosed fence in the goal cannot swallow the criteria.
  const readingSource = reading ? [
    ...(goal.trim() ? [goal] : []),
    ...(criteria.items.length > 0 ? ["## Acceptance criteria", criteriaToMarkdown(criteria.items)] : []),
  ] : [];

  return (
    <FormDialog
      open
      onOpenChange={(open) => !open && onClose()}
      size="document"
      // The line is there from the start, so the fields do not move down when the choices arrive.
      // A failed load leaves it empty (an empty fragment still reserves its height); the footer says why.
      context={choices ? (
        <Breadcrumb size="sm" current={false} items={[
          { id: "project", label: choices.projectName },
          ...(epicTitle ? [{ id: "epic", label: epicTitle, icon: "layers" as const }] : []),
        ]} />
      ) : loadProblem ? (
        <></>
      ) : (
        <Skeleton variant="text" width={160} />
      )}
      title={existing ? "Edit task" : "New task"}
      description={locked ? "Delivery has started, so what it asks for is fixed. You can still move it to another epic." : undefined}
      submitLabel={existing ? "Save" : "Create"}
      submitTestId="task-save"
      // Images go with the prompt, which only a delivery gives.
      canSubmit={canSave && !withImages}
      onSubmit={() => submit(false)}
      problem={problem ?? loadProblem}
      unsavedWords={unsaved}
      discardTitle={existing ? "Discard your changes to this task?" : "Discard this task?"}
      discardDescription={existing ? "Your changes to this task haven't been saved." : undefined}
      headerActions={
        <Tooltip content={reading ? "Back to writing" : "Read it as one document"} shortcut={READ_KEYS}>
          <Button variant="quiet" size="sm" leadingIcon={reading ? "edit" : "book-open"} onClick={() => setReading(!reading)} data-testid="task-read">
            {reading ? "Back to writing" : "Read"}
          </Button>
        </Tooltip>
      }
      reading={reading ? (
        <Markdown title={title} untitled="Untitled task" source={readingSource} breaks data-testid="task-reading" />
      ) : undefined}
      readingLabel="The task as it reads"
      onCloseReading={() => setReading(false)}
      onKeyDown={(e) => {
        if (!isReadKey(e)) return;
        e.preventDefault();
        // From a popup portalled out of the dialog (the Epic Select's list), which
        // Read would leave open over the document: it has to close first.
        if (!(e.target instanceof Node && e.currentTarget.contains(e.target))) return;
        setReading(!reading);
      }}
      // Over the task, the confirmation takes the key from the browser (a hard reload loses the draft) and does nothing.
      onConfirmKeyDown={(e) => isReadKey(e) && e.preventDefault()}
      // The whole dialog takes a dropped or pasted image, wherever the person is writing.
      wrapContent={existing ? undefined : (content) => (
        <AttachDropZone className="taskDrop" onFiles={tray.add} takePaste disabledReason={tray.disabledReason}
          detail="They go to the first agent with the task.">
          {content}
        </AttachDropZone>
      )}
      footerStart={
        <>
          <KeyHint keys={["mod", "Enter"]}>{existing ? "save" : "create"}</KeyHint>
          {locked || reading ? null : <KeyHint keys={["mod", "Shift", "P"]}>toggle preview</KeyHint>}
          <KeyHint keys={READ_KEYS}>{reading ? "back to writing" : "read"}</KeyHint>
        </>
      }
      extraActions={
        existing ? undefined : (
          <Button variant="primary" disabled={!canSave} onClick={() => submit(true)} data-testid="task-create-deliver">
            Create and deliver
          </Button>
        )
      }
      asideLabel="Where it sits"
      aside={
        <FormStack fill>
          <Select
            label="Epic"
            value={epic}
            onValueChange={setEpic}
            options={[{ value: NO_EPIC, label: "No epic" }, ...(choices?.epics ?? []).map((e) => ({ value: e.id, label: e.title }))]}
          />
          {choosing ? (
            <RepositoryChooser repositories={repositories} chosen={chosen} onChange={setChosen} disabled={locked} />
          ) : null}
          {existing ? null : <PromptImages tray={tray} />}
          <HelpList title="What makes a good task" items={[
            <><strong>Goal:</strong> why it matters, what exists today, and the constraints an agent can't guess.</>,
            <><strong>Criteria:</strong> one checkable statement per list item — reviewers check every one.</>,
            <>Link issues, designs and logs; paste error output in a <code>```</code> block.</>,
          ]} />
          <MarkdownCheatsheet extra={[["- [ ] item", "a criterion"]]} />
        </FormStack>
      }
    >
      <Input size="title" label="Title" labelNote={locked ? undefined : "required"} autoFocus required placeholder="What should change?"
        value={title} disabled={locked} maxLength={500} onChange={(e) => setTitle(e.target.value)} data-testid="task-title" />
      <MarkdownEditor
        label="Goal"
        labelNote={locked ? undefined : "required"}
        hint="Why it matters, what exists today, and anything an agent can't guess."
        placeholder="Why does this matter? What exists today? What must an agent not break?"
        value={goal}
        onChange={(value) => {
          setGoal(value);
          setGoalTyped(true);
        }}
        fill
        breaks
        minRows={4}
        maxLength={TASK_GOAL_MAX}
        locked={locked}
        error={goalError}
        data-testid="task-goal"
      />
      <MarkdownEditor
        label="Acceptance criteria"
        hint="One list item per criterion. Reviewers check each one."
        placeholder="- [ ] A thing that must be true when it's done"
        value={criteriaSource}
        onChange={setCriteriaSource}
        breaks
        minRows={3}
        maxLength={TASK_CRITERIA_MAX}
        locked={locked}
        error={criteriaError}
        summary={criteriaSource.trim() ? (
          <Badge tone="neutral" size="sm" data-testid="task-criteria-count">
            {criteria.items.length} {criteria.items.length === 1 ? "criterion" : "criteria"}
          </Badge>
        ) : undefined}
        notice={criteria.stray && !locked ? "Text outside a list item isn't saved as a criterion" : undefined}
        data-testid="task-criteria"
      />
    </FormDialog>
  );
}

/**
 * Images the first agent is given with the task: a design, a screenshot of
 * the bug. The composer's tray, outside a composer: the chips and the
 * attach button. Paste and drop are the whole dialog's (`wrapContent`).
 */
function PromptImages({ tray }: { tray: ImageTray }) {
  const [input, setInput] = useState<HTMLInputElement | null>(null);
  const off = tray.disabledReason !== undefined;
  return (
    <Fieldset legend="Images" data-testid="task-images"
      hint={tray.attachments.length > 0 ? "Given to the first agent with the task: use Create and deliver." : "A design or a screenshot for the first agent. Paste, drop or attach."}>
      <div className="taskImages">
        {tray.attachments.map((a) => <AttachmentChip key={a.id} attachment={a} onRemove={tray.remove} />)}
        <Tooltip content={tray.disabledReason ?? limitsHint(tray.limits, "this task")} keepOnPress={off}>
          <span>
            <Button variant="secondary" size="sm" leadingIcon="paperclip" disabled={off} onClick={() => input?.click()}
              data-testid="task-attach">
              Attach images
            </Button>
          </span>
        </Tooltip>
        <input ref={setInput} type="file" multiple hidden accept="image/png,image/jpeg,image/webp,image/gif" data-testid="task-attach-input"
          onChange={(e) => {
            const files = Array.from(e.target.files ?? []);
            e.target.value = "";
            if (files.length > 0) tray.add(files);
          }} />
      </div>
      {tray.attachments.some((a) => a.state === "error") ? (
        <Callout tone="attention">{attachmentWarning(tray.attachments)}</Callout>
      ) : null}
    </Fieldset>
  );
}

/**
 * Which of the project's repositories the work touches: each one either
 * changed (a pull request if it is) or only read, for context. None chosen
 * is work that changes no code — a design, a write-up.
 */
function RepositoryChooser(props: {
  repositories: Repository[];
  chosen: TaskRepository[];
  onChange: (chosen: TaskRepository[]) => void;
  disabled: boolean;
}) {
  const { repositories, chosen, onChange, disabled } = props;
  const accessOf = (id: string) => chosen.find((c) => c.id === id)?.access;
  const set = (id: string, access: "write" | "read" | null) => {
    const rest = chosen.filter((c) => c.id !== id);
    onChange(access ? [...rest, { id, access }] : rest);
  };
  return (
    <Fieldset
      legend="Repositories"
      hint={chosen.length === 0
        ? "None chosen: this work changes no code. What the agents write is kept with it."
        : "Each repository it changes gets its own pull request."}
      disabled={disabled}
      data-testid="task-repositories"
    >
      {repositories.map((r) => {
        const access = accessOf(r.id);
        return (
          <div className="repositoryChoice" key={r.id}>
            <Checkbox
              label={<span><span className="ds-mono">{r.name}</span> <span className="muted">{r.defaultBranch}</span></span>}
              checked={Boolean(access)}
              disabled={disabled}
              onCheckedChange={(on) => set(r.id, on === true ? "write" : null)}
            />
            {access ? (
              <Select
                size="sm"
                aria-label={`What the work does in ${r.name}`}
                disabled={disabled}
                value={access}
                onValueChange={(v) => set(r.id, v as "write" | "read")}
                options={[
                  { value: "write", label: "Changes it" },
                  { value: "read", label: "Reads it" },
                ]}
              />
            ) : null}
          </div>
        );
      })}
    </Fieldset>
  );
}
