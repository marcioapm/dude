/**
 * Create or edit a task: what it asks for, and where it sits.
 *
 * One dialog for both, because they are the same fields. Once a delivery
 * has started, what the task asks for (title, goal, criteria,
 * repository) is fixed — agents are working to it — and only where it sits
 * (its epic) can change; the fields say so rather than failing on save.
 *
 * Mounted by its opener only while open, so each opening starts from the
 * task (or empty).
 */

import { useEffect, useState } from "react";
import { Button, Checkbox, Fieldset, FormRow, IconButton, Input, Select } from "@dude/design-system/primitives";
import type { ApiClient, Epic, Repository, TaskDetail, TaskFields, TaskRepository } from "../api/client.ts";
import { errorText, FormDialog, useSave } from "../hooks/useSave.tsx";

const NO_EPIC = "__none__";

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
  const [title, setTitle] = useState(existing?.title ?? "");
  const [goal, setGoal] = useState(existing?.goal ?? "");
  const [criteria, setCriteria] = useState<string[]>(existing?.acceptanceCriteria.length ? [...existing.acceptanceCriteria] : [""]);
  const [epic, setEpic] = useState<string>(existing?.epicId ?? epicId ?? NO_EPIC);
  const [chosen, setChosen] = useState<TaskRepository[]>(existing?.repositories ?? []);
  // The choices arrive after it opens; until they have, a save could miss
  // a repository the project needs named.
  const [choices, setChoices] = useState<{ epics: Epic[]; repositories: Repository[] } | null>(null);
  const [loadProblem, setLoadProblem] = useState<string | null>(null);
  // Created, but its delivery did not start: a retry delivers it rather
  // than creating a second.
  const [created, setCreated] = useState<string | null>(null);
  const { busy, problem, save } = useSave();

  useEffect(() => {
    let current = true;
    void Promise.all([client.listEpics(projectId), client.getProject(projectId)]).then(
      ([{ epics }, project]) => current && setChoices({ epics, repositories: project.repositories }),
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
  const canSave = choices !== null && Boolean(title.trim()) && !busy;

  function submit(deliver: boolean) {
    const fields: Partial<TaskFields> = { epicId: epic === NO_EPIC ? null : epic };
    if (!locked) {
      Object.assign(fields, {
        title: title.trim(),
        goal: goal.trim(),
        acceptanceCriteria: criteria.map((c) => c.trim()).filter(Boolean),
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

  return (
    <FormDialog
      open
      onOpenChange={(open) => !open && onClose()}
      size="md"
      title={existing ? "Edit task" : "New task"}
      description={locked ? "Delivery has started, so what it asks for is fixed. You can still move it to another epic." : undefined}
      submitLabel={existing ? "Save" : "Create"}
      submitTestId="task-save"
      canSubmit={canSave}
      onSubmit={() => submit(false)}
      problem={problem ?? loadProblem}
      extraActions={
        existing ? undefined : (
          <Button variant="primary" disabled={!canSave} onClick={() => submit(true)} data-testid="task-create-deliver">
            Create and deliver
          </Button>
        )
      }
    >
      <Input label="Title" autoFocus required placeholder="What should change?" value={title} disabled={locked}
        maxLength={500} onChange={(e) => setTitle(e.target.value)} data-testid="task-title" />
      <Input label="Goal" hint="Why, and any detail an agent needs." value={goal} disabled={locked}
        maxLength={10_000} onChange={(e) => setGoal(e.target.value)} data-testid="task-goal" />
      <FormRow>
        <Select
          label="Epic"
          value={epic}
          onValueChange={setEpic}
          options={[{ value: NO_EPIC, label: "No epic" }, ...(choices?.epics ?? []).map((e) => ({ value: e.id, label: e.title }))]}
        />
      </FormRow>
      {choosing ? (
        <RepositoryChooser repositories={repositories} chosen={chosen} onChange={setChosen} disabled={locked} />
      ) : null}
      <Fieldset legend="Acceptance criteria" className="criteria" disabled={locked}>
        {criteria.map((c, i) => (
          <div className="criterion" key={i}>
            <Input
              size="sm"
              aria-label={`Criterion ${i + 1}`}
              placeholder="A thing that must be true when it is done"
              value={c}
              maxLength={2000}
              onChange={(e) => setCriteria((all) => all.map((x, j) => (j === i ? e.target.value : x)))}
              onKeyDown={(e) => {
                if (e.key === "Enter" && c.trim()) {
                  e.preventDefault();
                  setCriteria((all) => [...all.slice(0, i + 1), "", ...all.slice(i + 1)]);
                }
              }}
            />
            {criteria.length > 1 ? (
              <IconButton icon="close" label={`Remove criterion ${i + 1}`} size="sm"
                onClick={() => setCriteria((all) => all.filter((_, j) => j !== i))} />
            ) : null}
          </div>
        ))}
        <Button size="sm" variant="ghost" leadingIcon="plus" onClick={() => setCriteria((all) => [...all, ""])}>
          Add criterion
        </Button>
      </Fieldset>
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
