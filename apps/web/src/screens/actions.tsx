/**
 * The actions on things in the tree and on the board: what a row's "…"
 * menu offers for a project, an epic or a task, and the small dialogs
 * those actions open.
 *
 * The design system draws the menus; which actions exist, and what they
 * do, is the app's to say — here, in one place, so the sidebar and the
 * board offer the same things.
 */

import { useEffect, useRef, useState } from "react";
import { MachineChip, MachineRaises, MachineTip, SessionFacts, SessionRailBlock } from "@dude/design-system/components";
import { machineSpec, type AgentMachine, type Machine } from "@dude/domain";
import { raisedProvenance, raisedRows, roleLabel, taskRaisedMachines } from "../machines.ts";
import type { NavEpic, NavProject, NavRef } from "@dude/design-system";
import type { RowMenuItem } from "@dude/design-system/primitives";
import { Dialog, Button, Callout, Input, Textarea } from "@dude/design-system/primitives";
import type { ApiClient, Epic, TaskDetail } from "../api/client.ts";
import { errorText, FormDialog, useSave } from "../hooks/useSave.tsx";

export function ResetMachineConfirm({ label, from, to, by, session, onConfirm, onClose }: {
  label: string; from: string; to: string; by: string; session?: boolean;
  onConfirm: (note: string) => Promise<unknown>; onClose: () => void;
}) {
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);
  const posting = useRef(false);
  const submit = async () => {
    if (posting.current || note.length > 2000) return;
    posting.current = true;
    setBusy(true);
    setProblem(null);
    try {
      await onConfirm(note);
      onClose();
    } catch (err) {
      setProblem(errorText(err));
    } finally {
      posting.current = false;
      setBusy(false);
    }
  };
  const lead = `Moved back to ${to} by ${by}`;
  return (
    <Dialog open size="sm" onOpenChange={(open) => !open && !posting.current && onClose()}
      title={`Move the ${label} back to ${to} now?`}
      description={`It keeps its conversation and checkout; running processes stop.${session ? "" : ` Later ${label} Runs in this task run on ${to} too.`}`}
      footer={<>
        <Button variant="quiet" disabled={busy} onClick={onClose}>Keep {from}</Button>
        <Button variant="primary" disabled={busy || note.length > 2000} onClick={() => void submit()} data-testid="confirm-reset">Move it to {to}</Button>
      </>}>
      <Textarea label={`Note to the ${label} (optional)`} rows={2} maxRows={6} maxLength={2000} autoFocus
        value={note} disabled={busy} onChange={(e) => setNote(e.target.value)} data-testid="reset-note"
        hint={note.trim() ? `It is told “${lead}:” and then your note.` : `It is told “${lead}.”`} />
      {problem ? <Callout tone="danger" role="alert" data-testid="reset-error">{problem}</Callout> : null}
    </Dialog>
  );
}

/**
 * A session's raised machine in its rail: the chip with who asked, the
 * pool, the settings' size a Reset returns to, and Reset, which every
 * member may use. All of it is the session detail's `agentMachines`.
 */
export function SessionMachineBlock({ client, id, machine, by, onChanged }: {
  client: ApiClient; id: string; machine: AgentMachine; by: string; onChanged: () => void;
}) {
  const [confirm, setConfirm] = useState(false);
  const { size, settings } = machine;
  if (!size) return null;
  return <SessionRailBlock label="Machine" data-testid="session-machine">
    <MachineChip name={size.name} spec={machineSpec(size)} raised="for this session"
      tooltip={<MachineTip name={size.name}>{raisedProvenance(machine.raise, machine.askedBy.role ?? machine.raise.role)}.
        {settings ? ` Its settings name ${settings.name}.` : ""}</MachineTip>} />
    <SessionFacts facts={[{ label: "Pool", value: size.pool ?? "Default pool" }, { label: "Settings' size", value: settings?.name ?? "None" }]} />
    {settings ? <Button size="sm" variant="quiet" onClick={() => setConfirm(true)} data-testid="session-machine-reset">Reset</Button> : null}
    {confirm && settings ? <ResetMachineConfirm label="brainstorm" from={size.name} to={settings.name} by={by} session
      onClose={() => setConfirm(false)} onConfirm={async (note) => { await client.resetSessionMachine(id, note); onChanged(); }} /> : null}
  </SessionRailBlock>;
}

/** The Overview's Machines panel: each raised role, the roles following it, and Reset back to the settings' size. */
export function TaskMachineRaises({ client, task, by, onChanged }: {
  client: ApiClient; task: Pick<TaskDetail, "id" | "agentMachines">; by: string; onChanged: () => void;
}) {
  const machines = taskRaisedMachines(task);
  const [reset, setReset] = useState<{ role: string; from: Machine; to: Machine } | null>(null);
  if (machines.length === 0) return null;
  const rows = raisedRows(machines, (m, from, to) => setReset({ role: m.role, from, to }));
  return <>
    <MachineRaises where="raised in this task" raises={rows} />
    {reset ? <ResetMachineConfirm label={roleLabel(reset.role).toLowerCase()} from={reset.from.name} to={reset.to.name} by={by}
      onClose={() => setReset(null)} onConfirm={async (note) => {
        await client.resetTaskMachine(task.id, reset.role, note);
        onChanged();
      }} /> : null}
  </>;
}

/** What an action asks the app to open; the app owns navigation and dialogs. */
export type Intent =
  | { kind: "newTask"; projectId: string; epicId: string | null }
  | { kind: "editTask"; taskId: string }
  | { kind: "newEpic"; projectId: string }
  | { kind: "editEpic"; epic: EpicRef }
  | { kind: "deleteEpic"; epic: EpicRef }
  | { kind: "projectSettings"; projectId: string; page?: string }
  | { kind: "moveEpic"; epicId: string; position: number }
  | { kind: "moveTask"; taskId: string; epicId: string | null };

export interface EpicRef {
  id: string;
  projectId: string;
  title: string;
  taskCount: number;
}

/** An epic, as the epic dialogs take it. */
export function epicRef(projectId: string, epic: NavEpic): EpicRef {
  return { id: epic.id, projectId, title: epic.title, taskCount: epic.tasks.length };
}

/** The actions on one thing in the tree, or none (sessions and runs have their own screens). */
export function rowActions(project: NavProject, ref: NavRef, act: (intent: Intent) => void): RowMenuItem[] | null {
  const epics = project.epics ?? [];
  switch (ref.kind) {
    case "project":
      return [
        { id: "new-task", label: "New task", icon: "plus", onSelect: () => act({ kind: "newTask", projectId: project.id, epicId: null }) },
        { id: "new-epic", label: "New epic", icon: "layers", onSelect: () => act({ kind: "newEpic", projectId: project.id }) },
        { kind: "separator" },
        { id: "settings", label: "Project settings", icon: "settings", onSelect: () => act({ kind: "projectSettings", projectId: project.id }) },
        { id: "agents", label: "Agents & prompts", icon: "agent", onSelect: () => act({ kind: "projectSettings", projectId: project.id, page: "implementer" }) },
      ];
    case "epic": {
      const index = epics.findIndex((e) => e.id === ref.id);
      const epic = epics[index]!;
      const target = epicRef(project.id, epic);
      return [
        { id: "new-task", label: "New task", icon: "plus", onSelect: () => act({ kind: "newTask", projectId: project.id, epicId: epic.id }) },
        { id: "edit", label: "Edit epic", icon: "edit", onSelect: () => act({ kind: "editEpic", epic: target }) },
        { kind: "separator" },
        {
          id: "up", label: "Move up", icon: "arrow-up", disabled: index === 0, disabledReason: "Already first",
          onSelect: () => act({ kind: "moveEpic", epicId: epic.id, position: index - 1 }),
        },
        {
          id: "down", label: "Move down", icon: "arrow-down", disabled: index === epics.length - 1, disabledReason: "Already last",
          onSelect: () => act({ kind: "moveEpic", epicId: epic.id, position: index + 1 }),
        },
        { kind: "separator" },
        { id: "delete", label: "Delete epic", tone: "danger", onSelect: () => act({ kind: "deleteEpic", epic: target }) },
      ];
    }
    case "task": {
      const item = ref;
      const current = epics.find((e) => e.tasks.some((w) => w.id === item.id))?.id ?? null;
      const destinations: RowMenuItem[] = [
        ...epics.map((e: NavEpic) => ({
          id: `to-${e.id}`,
          label: e.title,
          disabled: e.id === current,
          disabledReason: "It is already here",
          onSelect: () => act({ kind: "moveTask", taskId: item.id, epicId: e.id }),
        })),
        ...(epics.length ? [{ kind: "separator" as const }] : []),
        {
          id: "to-none",
          label: "No epic",
          disabled: current === null,
          disabledReason: "It is not in an epic",
          onSelect: () => act({ kind: "moveTask", taskId: item.id, epicId: null }),
        },
      ];
      return [
        { id: "edit", label: "Edit", icon: "edit", onSelect: () => act({ kind: "editTask", taskId: item.id }) },
        { kind: "submenu", id: "move", label: "Move to epic", icon: "layers", items: destinations },
      ];
    }
    default:
      return null;
  }
}

/** Create or edit an epic. Mounted only while open, so it starts from the epic (or empty). */
export function EpicDialog(props: {
  client: ApiClient;
  projectId: string;
  epic: EpicRef | null;
  onClose: () => void;
  onSaved: (epic: Epic) => void;
}) {
  const { epic, client, projectId } = props;
  const [title, setTitle] = useState(epic?.title ?? "");
  // The tree does not carry an epic's description: an edit reads it first,
  // so saving cannot blank one it never showed.
  const [description, setDescription] = useState<string | null>(epic ? null : "");
  const { busy, problem, save } = useSave();
  const [loadProblem, setLoadProblem] = useState<string | null>(null);
  useEffect(() => {
    if (!epic) return;
    let current = true;
    void client.listEpics(projectId).then(
      ({ epics }) => current && setDescription(epics.find((e) => e.id === epic.id)?.description ?? ""),
      (err: unknown) => current && setLoadProblem(errorText(err)),
    );
    return () => {
      current = false;
    };
  }, [client, projectId, epic]);
  return (
    <FormDialog
      open
      onOpenChange={(open) => !open && props.onClose()}
      title={epic ? "Edit epic" : "New epic"}
      description="An epic groups related tasks; its place in the list is its priority."
      submitLabel={epic ? "Save" : "Create epic"}
      submitTestId="epic-save"
      canSubmit={!busy && description !== null && Boolean(title.trim())}
      problem={problem ?? loadProblem}
      onSubmit={() => {
        let saved: Epic | null = null;
        void save(
          async () => {
            const fields = { title: title.trim(), description: (description ?? "").trim() };
            saved = epic ? await props.client.updateEpic(epic.id, fields) : await props.client.createEpic(props.projectId, fields);
          },
          () => {
            props.onClose();
            props.onSaved(saved!);
          },
          epic ? "Epic saved" : "Epic created",
        );
      }}
    >
      <Input label="Title" autoFocus value={title} maxLength={200} onChange={(e) => setTitle(e.target.value)} data-testid="epic-title" />
      <Textarea label="Description" hint="What this group of work is for. Markdown." value={description ?? ""}
        disabled={description === null} placeholder={description === null ? "Loading…" : undefined}
        maxLength={10_000} onChange={(e) => setDescription(e.target.value)} />
    </FormDialog>
  );
}

/** Confirm deleting an epic, saying what happens to its work. */
export function DeleteEpicDialog(props: { client: ApiClient; epic: EpicRef; onClose: () => void; onDeleted: () => void }) {
  const { busy, problem, save } = useSave();
  const { epic } = props;
  const count = epic.taskCount;
  return (
    <Dialog
      open
      onOpenChange={(open) => !open && props.onClose()}
      tone="danger"
      size="sm"
      title={`Delete ${epic.title}?`}
      description={
        count === 0
          ? "It has no tasks."
          : `Its ${count} task${count === 1 ? "" : "s"} will stay in the project, with no epic.`
      }
      footer={
        <>
          <Button variant="quiet" onClick={props.onClose}>
            Cancel
          </Button>
          <Button
            variant="danger" solid
            disabled={busy}
            data-testid="epic-delete"
            onClick={() => void save(() => props.client.deleteEpic(epic.id), () => {
              props.onClose();
              props.onDeleted();
            }, `${epic.title} deleted`)}
          >
            Delete epic
          </Button>
        </>
      }
    >
      {problem ? <Callout tone="danger">{problem}</Callout> : null}
    </Dialog>
  );
}
