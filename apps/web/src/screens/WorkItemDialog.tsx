/**
 * Create or edit a work item: what it asks for, and where it sits.
 *
 * One dialog for both, because they are the same fields. Once a delivery
 * has started, what the work item asks for (title, goal, criteria,
 * repository) is fixed — agents are working to it — and only where it sits
 * (its epic) can change; the fields say so rather than failing on save.
 */

import { useEffect, useId, useRef, useState } from "react";
import { Button, Dialog, IconButton, Input, Select } from "@dude/design-system/primitives";
import type { ApiClient, Epic, Repository, WorkItemFields } from "../api/client.ts";
import { ApiError } from "../api/client.ts";

const NO_EPIC = "__none__";

export interface WorkItemDialogProps {
  client: ApiClient;
  projectId: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** Editing this work item; omitted to create one. */
  existing?: ({ id: string; delivering: boolean } & WorkItemFields) | undefined;
  /** Prefilled epic when creating from an epic's board. */
  epicId?: string | null | undefined;
  onSaved: (id: string, deliver: boolean) => void;
}

export function WorkItemDialog({ client, projectId, open, onOpenChange, existing, epicId, onSaved }: WorkItemDialogProps) {
  const [title, setTitle] = useState("");
  const [goal, setGoal] = useState("");
  const [criteria, setCriteria] = useState<string[]>([""]);
  const [epic, setEpic] = useState<string>(NO_EPIC);
  const [repository, setRepository] = useState<string>("");
  const [epics, setEpics] = useState<Epic[]>([]);
  const [repositories, setRepositories] = useState<Repository[]>([]);
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);
  // The choices arrive after the dialog opens; until they have, a save could
  // miss a repository the project needs named.
  const [loaded, setLoaded] = useState(false);
  // Created, but its delivery did not start: a retry delivers it rather
  // than creating a second.
  const [created, setCreated] = useState<string | null>(null);
  const formId = useId();

  // Fresh fields and choices each time it opens — only then: the parent
  // re-renders as events arrive, and a reset on every render would wipe
  // what the person is typing.
  const initial = useRef({ existing, epicId });
  initial.current = { existing, epicId };
  useEffect(() => {
    if (!open) return;
    const { existing: e, epicId: preset } = initial.current;
    setTitle(e?.title ?? "");
    setGoal(e?.goal ?? "");
    setCriteria(e?.acceptanceCriteria.length ? [...e.acceptanceCriteria] : [""]);
    setEpic(e?.epicId ?? preset ?? NO_EPIC);
    setRepository(e?.repositoryId ?? "");
    setProblem(null);
    setCreated(null);
    setLoaded(false);
    setEpics([]);
    setRepositories([]);
    let current = true;
    void Promise.all([client.listEpics(projectId), client.getProject(projectId)]).then(
      ([{ epics: found }, project]) => {
        if (!current) return;
        setEpics(found);
        setRepositories(project.repositories);
        setLoaded(true);
      },
      (err: unknown) => current && setProblem(err instanceof Error ? err.message : String(err)),
    );
    return () => {
      current = false;
    };
  }, [open, client, projectId]);

  const locked = existing?.delivering ?? false;
  const needsRepository = repositories.length > 1;

  async function save(deliver: boolean) {
    if (!title.trim()) return;
    setBusy(true);
    setProblem(null);
    const fields: Partial<WorkItemFields> = { epicId: epic === NO_EPIC ? null : epic };
    if (!locked) {
      Object.assign(fields, {
        title: title.trim(),
        goal: goal.trim(),
        acceptanceCriteria: criteria.map((c) => c.trim()).filter(Boolean),
        repositoryId: repository || null,
      });
    }
    try {
      const id = existing
        ? (await client.updateWorkItem(existing.id, fields)).id
        : (created ?? (await client.createWorkItem({ projectId, title: title.trim(), ...fields })).id);
      if (!existing) setCreated(id);
      if (deliver) await client.deliver(id);
      onOpenChange(false);
      onSaved(id, deliver);
    } catch (err) {
      setProblem(err instanceof ApiError ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }

  const canSave = loaded && Boolean(title.trim()) && !busy && (!needsRepository || repository !== "" || locked);

  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      size="md"
      title={existing ? "Edit work item" : "New work item"}
      description={
        locked ? "Delivery has started, so what it asks for is fixed. You can still move it to another epic." : undefined
      }
      footer={
        <>
          <Button variant="ghost" onClick={() => onOpenChange(false)}>
            Cancel
          </Button>
          <Button type="submit" form={formId} variant={existing ? "primary" : "secondary"} disabled={!canSave}
            data-testid="work-item-save">
            {existing ? "Save" : "Create"}
          </Button>
          {existing ? null : (
            <Button variant="primary" disabled={!canSave} onClick={() => void save(true)} data-testid="work-item-create-deliver">
              Create and deliver
            </Button>
          )}
        </>
      }
    >
      <form
        id={formId}
        className="dialogForm"
        onSubmit={(e) => {
          e.preventDefault();
          if (canSave) void save(false);
        }}
      >
        <Input
          label="Title"
          autoFocus
          required
          placeholder="What should change?"
          value={title}
          disabled={locked}
          maxLength={500}
          onChange={(e) => setTitle(e.target.value)}
          data-testid="work-item-title"
        />
        <Input
          label="Goal"
          hint="Why, and any detail an agent needs."
          value={goal}
          disabled={locked}
          maxLength={10_000}
          onChange={(e) => setGoal(e.target.value)}
          data-testid="work-item-goal"
        />
        <div className="fieldRow">
          <Select
            label="Epic"
            value={epic}
            onValueChange={setEpic}
            options={[{ value: NO_EPIC, label: "No epic" }, ...epics.map((e) => ({ value: e.id, label: e.title }))]}
          />
          {/* One repository needs no choosing: it is the only place the work can go. */}
          {repositories.length > 1 ? (
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
                <IconButton
                  icon="close"
                  label={`Remove criterion ${i + 1}`}
                  size="sm"
                  onClick={() => setCriteria((all) => all.filter((_, j) => j !== i))}
                />
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
        {problem ? <p className="problem" role="alert">{problem}</p> : null}
      </form>
    </Dialog>
  );
}
