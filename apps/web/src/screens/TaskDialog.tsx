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
 * Mounted by its opener only while open, so each opening starts from the
 * task (or empty).
 */

import { useEffect, useMemo, useState } from "react";
import { Badge, Breadcrumb, Button, Checkbox, Fieldset, FormStack, HelpList, Input, KeyHint, MarkdownCheatsheet, MarkdownEditor, Select } from "@dude/design-system";
import type { ApiClient, Epic, Repository, TaskDetail, TaskFields, TaskRepository } from "../api/client.ts";
import { unsavedWords } from "../hooks/discard.ts";
import { errorText, FormDialog, useSave } from "../hooks/useSave.tsx";
import { criteriaFromMarkdown, criteriaToMarkdown, criterionTooLong } from "./criteria.ts";

const NO_EPIC = "__none__";
const GOAL_MAX = 10_000;
// The server caps each criterion (2,000) and not the list; this bounds the
// source so a paste cannot make a request the browser struggles to send.
const CRITERIA_MAX = 20_000;

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
  const [opened] = useState(() => [existing?.title ?? "", existing?.goal ?? "", criteriaToMarkdown(existing?.acceptanceCriteria ?? [])]);
  const [title, setTitle] = useState(opened[0]!);
  const [goal, setGoal] = useState(opened[1]!);
  const [criteriaSource, setCriteriaSource] = useState(opened[2]!);
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

  const locked = existing?.delivering ?? false;
  const repositories = choices?.repositories ?? [];
  // One repository needs no choosing: it is where the work goes unless the
  // task says otherwise.
  const choosing = repositories.length > 1;
  const criteria = useMemo(() => criteriaFromMarkdown(criteriaSource), [criteriaSource]);
  const criteriaError = locked ? null : criterionTooLong(criteria.items);
  const canSave = choices !== null && Boolean(title.trim()) && !criteriaError && !busy;
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
        if (deliver) await client.deliver(id);
      },
      () => {
        onClose();
        onSaved(id, deliver);
      },
    );
  }

  const epicTitle = choices?.epics.find((e) => e.id === epic)?.title;

  return (
    <FormDialog
      open
      onOpenChange={(open) => !open && onClose()}
      size="document"
      context={choices ? (
        <Breadcrumb size="sm" current={false} items={[
          { id: "project", label: choices.projectName },
          ...(epicTitle ? [{ id: "epic", label: epicTitle, icon: "layers" as const }] : []),
        ]} />
      ) : undefined}
      title={existing ? "Edit task" : "New task"}
      description={locked ? "Delivery has started, so what it asks for is fixed. You can still move it to another epic." : undefined}
      submitLabel={existing ? "Save" : "Create"}
      submitTestId="task-save"
      canSubmit={canSave}
      onSubmit={() => submit(false)}
      problem={problem ?? loadProblem}
      unsavedWords={unsaved}
      discardTitle={existing ? "Discard your changes to this task?" : "Discard this task?"}
      discardDescription={existing ? "Your changes to this task haven't been saved." : undefined}
      footerStart={
        <>
          <KeyHint keys={["mod", "Enter"]}>{existing ? "save" : "create"}</KeyHint>
          {locked ? null : <KeyHint keys={["mod", "Shift", "P"]}>toggle preview</KeyHint>}
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
          <HelpList title="What makes a good task">
            <li><strong>Goal:</strong> why it matters, what exists today, and the constraints an agent can't guess.</li>
            <li><strong>Criteria:</strong> one checkable statement per list item — reviewers check every one.</li>
            <li>Link issues, designs and logs; paste error output in a <code>```</code> block.</li>
          </HelpList>
          <MarkdownCheatsheet extra={[["- [ ] item", "a criterion"]]} />
        </FormStack>
      }
    >
      <Input size="title" label="Title" labelNote={locked ? undefined : "required"} autoFocus required placeholder="What should change?"
        value={title} disabled={locked} maxLength={500} onChange={(e) => setTitle(e.target.value)} data-testid="task-title" />
      <MarkdownEditor
        label="Goal"
        hint="Why it matters, what exists today, and anything an agent can't guess."
        placeholder="Why does this matter? What exists today? What must an agent not break?"
        value={goal}
        onChange={setGoal}
        minRows={12}
        maxLength={GOAL_MAX}
        locked={locked}
        data-testid="task-goal"
      />
      <MarkdownEditor
        label="Acceptance criteria"
        hint="One list item per criterion. Reviewers check each one."
        placeholder="- [ ] A thing that must be true when it's done"
        value={criteriaSource}
        onChange={setCriteriaSource}
        minRows={7}
        maxLength={CRITERIA_MAX}
        locked={locked}
        error={criteriaError ?? undefined}
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
