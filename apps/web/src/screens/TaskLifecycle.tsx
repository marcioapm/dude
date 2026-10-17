/**
 * A task's lifecycle on its page (docs/design/tracking-work.md, Epic 1):
 * the header's `…` menu with Close… / Reopen and Archive… / Unarchive, each
 * disabled with its reason; the closed and archived notices, whose one
 * action is the page's button too; and the dialogs that close and archive.
 */

import { useCallback, useMemo, useState } from "react";
import { ArchiveDialog, ArchivedMark, CloseDialog, ResolutionMark, hasEnded, isClosed, lifecycleMenuItems,
  type CloseDecision, type LifecycleTaskRef } from "@dude/design-system/components";
import { Button, Callout, RowMenu } from "@dude/design-system/primitives";
import { firstName, scopeTasks, type NavProject } from "@dude/design-system";
import { RESOLUTION_LABEL, notStarted } from "@dude/domain";
import type { ApiClient, TaskDetail } from "../api/client.ts";
import { errorText } from "../hooks/useSave.tsx";
import type { People } from "../people.tsx";

/** Who may decide, in the menu's words when the reader may not. */
export function notYours(task: Pick<TaskDetail, "owner">, people: Pick<People, "you" | "me">): string | undefined {
  if (people.me?.role === "admin") return undefined;
  if (!task.owner) return "Only an admin can do this: the task has no owner";
  if (task.owner.id === people.you) return undefined;
  return `Only ${firstName(task.owner.name)}, the owner, or an admin can do this`;
}

/** The tasks the tree knows, as the Close dialog's picker and lists name them. */
function treeTasks(tree: ReadonlyArray<NavProject>): LifecycleTaskRef[] {
  return tree.flatMap((p) => scopeTasks(p).filter((t) => t.key).map((t) => ({ key: t.key!, title: t.title, status: t.status,
    ...(t.duplicateOf ? { duplicateOf: t.duplicateOf } : {}) })));
}

export interface LifecycleControl {
  /** The menu items: Close… / Reopen, Archive… / Unarchive. */
  readonly items: ReturnType<typeof lifecycleMenuItems>;
  /** The marks beside the header's status. */
  readonly marks: React.ReactNode;
  /** The closed or archived notice, or null. */
  readonly notice: React.ReactNode;
  /** The dialogs, rendered once. */
  readonly dialogs: React.ReactNode;
  readonly problem: string | null;
}

/** What the task page needs of its lifecycle. `onChanged` re-reads the page. Called before the task is read: then nothing. */
export function useTaskLifecycle({ client, task: maybe, people, tree, waiting, onChanged }: {
  client: ApiClient;
  task: TaskDetail | null;
  people: People;
  tree: ReadonlyArray<NavProject>;
  /** The not-started tasks it blocks: what waits on it. */
  waiting: ReadonlyArray<LifecycleTaskRef>;
  onChanged: () => void;
}): LifecycleControl {
  const [dialog, setDialog] = useState<"close" | "closeArchive" | "archive" | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const all = useMemo(() => treeTasks(tree), [tree]);
  const find = useCallback(async (q: string) => {
    const s = q.trim().toLowerCase();
    return all.filter((t) => !s || `${t.key} ${t.title}`.toLowerCase().includes(s)).slice(0, 30);
  }, [all]);
  if (!maybe) return { items: [], marks: null, notice: null, dialogs: null, problem: null };
  const task = maybe;
  const refused = notYours(task, people);
  const archived = Boolean(task.archivedAt);
  const life = { status: task.status, resolution: task.resolution, archived };
  const ended = hasEnded(life);
  const act = async (fn: () => Promise<unknown>, keep = false) => {
    setBusy(true);
    setProblem(null);
    try {
      await fn();
      setDialog(null);
      onChanged();
    } catch (err) {
      setProblem(errorText(err));
      if (!keep) setDialog(null);
    } finally {
      setBusy(false);
    }
  };
  const reopen = () => void act(() => client.reopenTask(task.id));
  const unarchive = () => void act(() => client.unarchiveTask(task.id));
  const items = lifecycleMenuItems(life, { notYours: refused, onClose: () => setDialog("close"), onReopen: reopen,
    onArchive: () => setDialog(ended ? "archive" : "closeArchive"), onUnarchive: unarchive });
  const ref: LifecycleTaskRef = { key: task.key ?? task.id, title: task.title, status: task.status };
  const marks = (
    <>
      {task.resolution && task.resolution !== "nothing_to_merge" ? <ResolutionMark resolution={task.resolution} of={task.duplicateOf ?? undefined} /> : null}
      {archived ? <ArchivedMark /> : null}
    </>
  );
  const closed = isClosed(life);
  const by = (p: TaskDetail["closedBy"]) => (p ? ` by ${firstName(p.name)}` : "");
  const notice = archived ? (
    <Callout tone="neutral" data-testid="archived-notice">
      <b>{ref.key} is archived{by(task.archivedBy)}.</b>{" "}
      It is out of the board, the sidebar and every count. {closed ? "Unarchive it first to reopen it." : "Unarchive puts it back as it was."}
      <div className="escalationActions">
        <Button size="sm" variant="secondary" leadingIcon="archive" onClick={unarchive} disabled={Boolean(refused) || busy}
          title={refused} data-testid="unarchive">Unarchive</Button>
      </div>
    </Callout>
  ) : closed ? (
    <Callout tone="neutral" data-testid="closed-notice">
      <b>{ref.key} was closed as {task.resolution === "duplicate" && task.duplicateOf ? `a duplicate of ${task.duplicateOf}` : RESOLUTION_LABEL[task.resolution!]}{by(task.closedBy)}.</b>{" "}
      Nobody works on it, and nothing starts it. Reopen puts it back in Backlog, keeping what ran.
      <div className="escalationActions">
        <Button size="sm" variant="primary" leadingIcon="retry" onClick={reopen} disabled={Boolean(refused) || busy}
          title={refused} data-testid="reopen">Reopen</Button>
        <Button size="sm" variant="secondary" leadingIcon="archive" onClick={() => setDialog("archive")} disabled={Boolean(refused) || busy}
          title={refused} data-testid="archive">Archive…</Button>
      </div>
      {refused ? <p className="escalationWaiting">{refused}.</p> : null}
    </Callout>
  ) : null;
  const close = (archive: boolean) => (d: CloseDecision) =>
    void act(() => (archive ? client.archiveTask(task.id, d) : client.closeTask(task.id, d)), true);
  const dialogs = (
    <>
      {dialog === "close" || dialog === "closeArchive" ? (
        <CloseDialog open onOpenChange={(o) => !o && setDialog(null)} task={ref} waiting={waiting} andArchive={dialog === "closeArchive"}
          find={find} onConfirm={close(dialog === "closeArchive")} refused={refused} problem={problem} busy={busy} />
      ) : null}
      {dialog === "archive" ? (
        <ArchiveDialog open onOpenChange={(o) => !o && setDialog(null)} task={ref} onConfirm={() => void act(() => client.archiveTask(task.id), true)}
          refused={refused} problem={problem} busy={busy} />
      ) : null}
    </>
  );
  return { items, marks, notice, dialogs, problem: dialog ? null : problem };
}

/** The header's `…`: the task's lifecycle. */
export function TaskMenu({ items }: { readonly items: LifecycleControl["items"] }) {
  return <RowMenu label="Task actions" items={items} />;
}

/** The not-started tasks a task blocks, from its relations. */
export function waitingOn(blocks: ReadonlyArray<{ task: LifecycleTaskRef }>): LifecycleTaskRef[] {
  return blocks.filter((b) => notStarted(b.task.status)).map((b) => b.task);
}
