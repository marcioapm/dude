/**
 * The actions on things in the tree and on the board: what a row's "…"
 * menu offers for a project, an epic or a work item, and the small dialogs
 * those actions open.
 *
 * The design system draws the menus; which actions exist, and what they
 * do, is the app's to say — here, in one place, so the sidebar and the
 * board offer the same things.
 */

import { useState } from "react";
import type { NavEpic, NavProject, NavRef } from "@dude/design-system";
import type { RowMenuItem } from "@dude/design-system/primitives";
import { Dialog, Button, Input, Textarea } from "@dude/design-system/primitives";
import type { ApiClient, Epic } from "../api/client.ts";
import { FormDialog, useSave } from "../hooks/useSave.tsx";

/** What an action asks the app to open; the app owns navigation and dialogs. */
export type Intent =
  | { kind: "newWorkItem"; projectId: string; epicId: string | null }
  | { kind: "editWorkItem"; workItemId: string }
  | { kind: "newEpic"; projectId: string }
  | { kind: "editEpic"; epic: EpicRef }
  | { kind: "deleteEpic"; epic: EpicRef }
  | { kind: "projectSettings"; projectId: string }
  | { kind: "moveEpic"; epicId: string; position: number }
  | { kind: "moveWorkItem"; workItemId: string; epicId: string | null };

export interface EpicRef {
  id: string;
  projectId: string;
  title: string;
  description?: string;
  workItemCount: number;
}

/** Where a row sits: its project, and for a work item, the epics it could move to. */
function projectOf(projects: readonly NavProject[], ref: NavRef): NavProject | undefined {
  return projects.find(
    (p) =>
      (ref.kind === "project" && p.id === ref.id) ||
      (ref.kind === "epic" && (p.epics ?? []).some((e) => e.id === ref.id)) ||
      (ref.kind === "workItem" &&
        ((p.workItems ?? []).some((w) => w.id === ref.id) || (p.epics ?? []).some((e) => e.workItems.some((w) => w.id === ref.id)))),
  );
}

/** The actions on one thing in the tree, or none (sessions and runs have their own screens). */
export function rowActions(projects: readonly NavProject[], ref: NavRef, act: (intent: Intent) => void): RowMenuItem[] | null {
  const project = projectOf(projects, ref);
  if (!project) return null;
  const epics = project.epics ?? [];
  switch (ref.kind) {
    case "project":
      return [
        { id: "new-work-item", label: "New work item", icon: "plus", onSelect: () => act({ kind: "newWorkItem", projectId: project.id, epicId: null }) },
        { id: "new-epic", label: "New epic", icon: "layers", onSelect: () => act({ kind: "newEpic", projectId: project.id }) },
        { kind: "separator" },
        { id: "settings", label: "Settings", icon: "settings", onSelect: () => act({ kind: "projectSettings", projectId: project.id }) },
      ];
    case "epic": {
      const index = epics.findIndex((e) => e.id === ref.id);
      const epic = epics[index]!;
      const target: EpicRef = { id: epic.id, projectId: project.id, title: epic.title, workItemCount: epic.workItems.length };
      return [
        { id: "new-work-item", label: "New work item", icon: "plus", onSelect: () => act({ kind: "newWorkItem", projectId: project.id, epicId: epic.id }) },
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
    case "workItem": {
      const item = ref;
      const current = epics.find((e) => e.workItems.some((w) => w.id === item.id))?.id ?? null;
      const destinations: RowMenuItem[] = [
        ...epics.map((e: NavEpic) => ({
          id: `to-${e.id}`,
          label: e.title,
          disabled: e.id === current,
          disabledReason: "It is already here",
          onSelect: () => act({ kind: "moveWorkItem", workItemId: item.id, epicId: e.id }),
        })),
        ...(epics.length ? [{ kind: "separator" as const }] : []),
        {
          id: "to-none",
          label: "No epic",
          disabled: current === null,
          disabledReason: "It is not in an epic",
          onSelect: () => act({ kind: "moveWorkItem", workItemId: item.id, epicId: null }),
        },
      ];
      return [
        { id: "edit", label: "Edit", icon: "edit", onSelect: () => act({ kind: "editWorkItem", workItemId: item.id }) },
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
  const { epic } = props;
  const [title, setTitle] = useState(epic?.title ?? "");
  const [description, setDescription] = useState(epic?.description ?? "");
  const { busy, problem, save } = useSave();
  return (
    <FormDialog
      open
      onOpenChange={(open) => !open && props.onClose()}
      title={epic ? "Edit epic" : "New epic"}
      description="An epic groups related work items; its place in the list is its priority."
      submitLabel={epic ? "Save" : "Create epic"}
      submitTestId="epic-save"
      canSubmit={!busy && Boolean(title.trim())}
      problem={problem}
      onSubmit={() => {
        let saved: Epic | null = null;
        void save(
          async () => {
            const fields = { title: title.trim(), description: description.trim() };
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
      <Textarea label="Description" hint="What this group of work is for. Markdown." value={description}
        maxLength={10_000} onChange={(e) => setDescription(e.target.value)} />
    </FormDialog>
  );
}

/** Confirm deleting an epic, saying what happens to its work. */
export function DeleteEpicDialog(props: { client: ApiClient; epic: EpicRef; onClose: () => void; onDeleted: () => void }) {
  const { busy, problem, save } = useSave();
  const { epic } = props;
  const count = epic.workItemCount;
  return (
    <Dialog
      open
      onOpenChange={(open) => !open && props.onClose()}
      tone="danger"
      size="sm"
      title={`Delete ${epic.title}?`}
      description={
        count === 0
          ? "It has no work items."
          : `Its ${count} work item${count === 1 ? "" : "s"} will stay in the project, with no epic.`
      }
      footer={
        <>
          <Button variant="ghost" onClick={props.onClose}>
            Cancel
          </Button>
          <Button
            variant="destructive"
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
      {problem ? <p className="problem" role="alert">{problem}</p> : null}
    </Dialog>
  );
}
