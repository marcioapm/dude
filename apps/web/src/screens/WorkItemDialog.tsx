/**
 * Create or edit a work item: what it asks for, and where it sits.
 *
 * One dialog for both, because they are the same fields. Once a delivery
 * has started, what the work item asks for (title, goal, criteria,
 * repository) is fixed — agents are working to it — and only where it sits
 * (its epic) can change; the fields say so rather than failing on save.
 *
 * Mounted by its opener only while open, so each opening starts from the
 * work item (or empty).
 */

import { useEffect, useState } from "react";
import { Button, IconButton, Input, Select } from "@dude/design-system/primitives";
import type { ApiClient, Epic, Repository, WorkItemFields } from "../api/client.ts";
import { errorText, FormDialog, useSave } from "../hooks/useSave.tsx";

const NO_EPIC = "__none__";

export interface WorkItemDialogProps {
  client: ApiClient;
  projectId: string;
  onClose: () => void;
  /** Editing this work item; omitted to create one. */
  existing?: ({ id: string; delivering: boolean } & WorkItemFields) | undefined;
  /** Prefilled epic when creating from an epic's board. */
  epicId?: string | null | undefined;
  onSaved: (id: string, deliver: boolean) => void;
}

export function WorkItemDialog({ client, projectId, onClose, existing, epicId, onSaved }: WorkItemDialogProps) {
  const [title, setTitle] = useState(existing?.title ?? "");
  const [goal, setGoal] = useState(existing?.goal ?? "");
  const [criteria, setCriteria] = useState<string[]>(existing?.acceptanceCriteria.length ? [...existing.acceptanceCriteria] : [""]);
  const [epic, setEpic] = useState<string>(existing?.epicId ?? epicId ?? NO_EPIC);
  const [repository, setRepository] = useState<string>(existing?.repositoryId ?? "");
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
  const needsRepository = repositories.length > 1;
  const canSave = choices !== null && Boolean(title.trim()) && !busy && (!needsRepository || repository !== "" || locked);

  function submit(deliver: boolean) {
    const fields: Partial<WorkItemFields> = { epicId: epic === NO_EPIC ? null : epic };
    if (!locked) {
      Object.assign(fields, {
        title: title.trim(),
        goal: goal.trim(),
        acceptanceCriteria: criteria.map((c) => c.trim()).filter(Boolean),
        repositoryId: repository || null,
      });
    }
    let id = existing?.id ?? created ?? "";
    void save(
      async () => {
        if (existing) await client.updateWorkItem(existing.id, fields);
        else if (!created) {
          id = (await client.createWorkItem({ projectId, title: title.trim(), ...fields })).id;
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
      title={existing ? "Edit work item" : "New work item"}
      description={locked ? "Delivery has started, so what it asks for is fixed. You can still move it to another epic." : undefined}
      submitLabel={existing ? "Save" : "Create"}
      submitTestId="work-item-save"
      canSubmit={canSave}
      onSubmit={() => submit(false)}
      problem={problem ?? loadProblem}
      extraActions={
        existing ? undefined : (
          <Button variant="primary" disabled={!canSave} onClick={() => submit(true)} data-testid="work-item-create-deliver">
            Create and deliver
          </Button>
        )
      }
    >
      <Input label="Title" autoFocus required placeholder="What should change?" value={title} disabled={locked}
        maxLength={500} onChange={(e) => setTitle(e.target.value)} data-testid="work-item-title" />
      <Input label="Goal" hint="Why, and any detail an agent needs." value={goal} disabled={locked}
        maxLength={10_000} onChange={(e) => setGoal(e.target.value)} data-testid="work-item-goal" />
      <div className="fieldRow">
        <Select
          label="Epic"
          value={epic}
          onValueChange={setEpic}
          options={[{ value: NO_EPIC, label: "No epic" }, ...(choices?.epics ?? []).map((e) => ({ value: e.id, label: e.title }))]}
        />
        {/* One repository needs no choosing: it is the only place the work can go. */}
        {needsRepository ? (
          <Select
            label="Repository"
            value={repository}
            placeholder="Choose a repository"
            disabled={locked}
            onValueChange={setRepository}
            options={repositories.map((r) => ({ value: r.id, label: `${r.name} · ${r.defaultBranch}` }))}
          />
        ) : null}
      </div>
      <fieldset className="criteria" disabled={locked}>
        <legend>Acceptance criteria</legend>
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
      </fieldset>
      {needsRepository && !repository && !locked ? (
        <p className="muted">This project has several repositories; choose the one this changes.</p>
      ) : null}
    </FormDialog>
  );
}
