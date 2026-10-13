/**
 * Task relations in the app (docs/design/task-relations.md): a task's
 * Relations section, the refused start in its header, Start anyway and
 * Start when unblocked, and a Backlog card's start on the board. The
 * components are the design system's; this wires them to the API and the
 * navigation tree.
 */

import { useCallback, useEffect, useMemo, useRef, useState, useSyncExternalStore, type ReactNode } from "react";
import {
  AutoStartDialog, AutoStartMark, DependencyStrip, RelationList, RelationPicker, StartAnywayDialog, StartGate, StartMenu,
  type DialogBlocker, type PickableTask, type RelatedTask, type RelationGroupData, type RelationGroupKind, type RelationSide, type StripTask,
} from "@dude/design-system/components";
import { Callout } from "@dude/design-system/primitives";
import { dependencySteps, firstName, formatDuration, neighbourKeys, scopeTasks, type NavEpic, type NavProject, type NavTask } from "@dude/design-system";
import { blockedRefusal, blockerList, neverMergesRefusal, type AutoStartHow, type TaskRelations, type TaskRelationView } from "@dude/domain";
import { ApiError, refusedBlockers, type ApiClient } from "../api/client.ts";
import { errorText } from "../hooks/useSave.tsx";

const NONE: TaskRelations = { blockedBy: [], blocks: [], related: [], autoStart: null, owner: null };

/** A task's relations, read again whenever `version` moves. Null until the first read. */
export function useRelations(client: ApiClient, taskId: string, version: number): { relations: TaskRelations | null; reload: () => void } {
  const [relations, setRelations] = useState<TaskRelations | null>(null);
  const [tick, setTick] = useState(0);
  useEffect(() => {
    let live = true;
    client.relations(taskId).then((r) => live && setRelations(r), () => live && setRelations((now) => now ?? NONE));
    return () => {
      live = false;
    };
  }, [client, taskId, version, tick]);
  return { relations, reload: useCallback(() => setTick((t) => t + 1), []) };
}

const related = (v: TaskRelationView): RelatedTask => ({ key: v.task.key, title: v.task.title, status: v.task.status,
  ...(v.task.project ? { project: v.task.project } : {}), ...(v.task.unmerged ? { unmerged: true } : {}) });

/** The blockers still open: not merged (a blocker done with nothing merged stays). */
export function openBlockers(r: TaskRelations | null): TaskRelationView[] {
  return (r?.blockedBy ?? []).filter((v) => !v.task.merged);
}

const dialogBlocker = (v: TaskRelationView): DialogBlocker => ({ ...related(v), owner: v.task.owner });

/** Why start when unblocked would never fire: a blocker marked done with nothing merged. */
export function neverMerges(blockers: ReadonlyArray<{ key: string; unmerged?: boolean | undefined }>): string | null {
  const never = blockers.filter((b) => b.unmerged).map((b) => b.key);
  return never.length === 0 ? null : neverMergesRefusal(never);
}

const ago = (at: string | number | Date) => formatDuration(Math.max(0, Date.now() - new Date(at).getTime()), { style: "age" });

/**
 * Where Deliver and Talk it through sit on a blocked task: refused for the
 * reason, then Start when unblocked (its owner) and Start anyway (a person),
 * each opening its dialog. Set to start when unblocked, the mark says so in
 * place of Start when unblocked.
 */
export function TaskStartGate({ client, taskId, taskKey, relations, you, onStarted, onChanged }: {
  client: ApiClient;
  taskId: string;
  taskKey: string;
  relations: TaskRelations;
  you: string | null;
  onStarted: (how: AutoStartHow) => void;
  onChanged: () => void;
}) {
  const [dialog, setDialog] = useState<"anyway" | "auto" | null>(null);
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);
  const blockers = openBlockers(relations);
  const owner = !relations.owner || relations.owner.id === you;
  const auto = relations.autoStart;
  const close = (open: boolean) => {
    if (!open) {
      setDialog(null);
      setProblem(null);
    }
  };
  const anyway = async (how: AutoStartHow, note: string) => {
    setBusy(true);
    setProblem(null);
    try {
      const start = { override: true as const, ...(note ? { note } : {}) };
      if (how === "deliver") await client.deliver(taskId, start);
      else await client.talk(taskId, start);
      setDialog(null);
      onStarted(how);
    } catch (err) {
      setProblem(errorText(err));
    } finally {
      setBusy(false);
    }
  };
  const setAuto = async (how: AutoStartHow) => {
    setBusy(true);
    setProblem(null);
    try {
      await client.setAutoStart(taskId, how);
      setDialog(null);
      onChanged();
    } catch (err) {
      setProblem(errorText(err));
    } finally {
      setBusy(false);
    }
  };
  return (
    <>
      <StartGate blockers={blockers.map(related)} onStartAnyway={() => setDialog("anyway")}
        onStartWhenUnblocked={owner && !auto ? () => setDialog("auto") : undefined}
        startWhenUnblockedRefused={neverMerges(blockers.map(related))}
        starts={[{ id: "talk", label: "Talk it through", icon: "message" }, { id: "deliver", label: "Deliver", icon: "zap" }]} />
      <StartAnywayDialog open={dialog === "anyway"} onOpenChange={close} taskKey={taskKey} blockers={blockers.map(dialogBlocker)}
        onConfirm={anyway} problem={problem} busy={busy} />
      <AutoStartDialog open={dialog === "auto"} onOpenChange={close} taskKey={taskKey} blockers={blockers.map(dialogBlocker)}
        onConfirm={setAuto} problem={problem} busy={busy} />
    </>
  );
}

/**
 * The notice where an escalation would sit: what it waits on, in words,
 * and that nothing starts it on its own — or, set, when it will. Neutral:
 * no one is needed.
 */
export function BlockedNotice({ client, taskId, relations, you, onChanged }: {
  client: ApiClient;
  taskId: string;
  relations: TaskRelations;
  you: string | null;
  onChanged: () => void;
}) {
  const blockers = openBlockers(relations);
  const [problem, setProblem] = useState<string | null>(null);
  if (blockers.length === 0) return null;
  const words = blockers.map(related);
  const auto = relations.autoStart;
  const owner = !relations.owner || relations.owner.id === you;
  const failed = words.filter((b) => b.status === "failed" || b.status === "aborted");
  const never = neverMerges(words);
  const cancel = () => {
    setProblem(null);
    client.cancelAutoStart(taskId).then(onChanged, (err: unknown) => setProblem(errorText(err)));
  };
  return (
    <Callout tone="neutral" data-testid="blocked-notice">
      <b>Blocked by {blockerList(words)}.</b>{" "}
      {auto ? null : <>It can start once {words.length === 1 ? "that's" : "they're"} merged; nothing starts it on its own then.</>}
      {auto ? (
        <AutoStartMark blockers={words} auto={{ how: auto.how, by: auto.by.id === you ? "You" : firstName(auto.by.name), ago: ago(auto.at),
          onCancel: owner ? cancel : undefined }} />
      ) : null}
      {failed.length > 0 ? (
        <div>{failed.map((b) => b.key).join(", ")} {failed.length === 1 ? "has" : "have"} stopped and still {failed.length === 1 ? "blocks" : "block"} this:
          pick {failed.length === 1 ? "it" : "them"} back up, or remove the relation.</div>
      ) : null}
      {never ? <div>{never.replace(", so this would never start it.", ".")} Only merged work releases what waits on it.</div> : null}
      {problem ? <div role="alert">{problem}</div> : null}
    </Callout>
  );
}

const SIDE_OF: Record<RelationGroupKind, RelationSide> = { "blocked-by": "blocked_by", blocks: "blocks", related: "related" };

function groups(r: TaskRelations): RelationGroupData[] {
  const row = (v: TaskRelationView) => ({
    ...related(v), owner: v.task.owner, settled: v.task.merged || undefined,
    removeHint: v.canRemove ? undefined : (v.removableBy ?? undefined),
  });
  return [
    { kind: "blocked-by", rows: r.blockedBy.map(row) },
    { kind: "blocks", rows: r.blocks.map(row) },
    { kind: "related", rows: r.related.map(row) },
  ];
}

/** Every task in the tree as the picker offers it: this epic, then this project, then the others. */
export function pickableTasks(tree: ReadonlyArray<NavProject>, here: { projectId: string; epicId: string | null }): PickableTask[] {
  const out: PickableTask[] = [];
  const rank = (p: NavProject, e: NavEpic | null) => (e && e.id === here.epicId ? 0 : p.id === here.projectId ? 1 : 2);
  for (const p of tree) {
    const each = (t: NavTask, e: NavEpic | null) => {
      if (!t.key) return;
      const r = rank(p, e);
      out.push({ key: t.key, title: t.title, status: t.status, group: r === 0 ? "This epic" : r === 1 ? p.name : "Other projects",
        ...(r === 2 ? { project: p.name } : {}) });
    };
    for (const e of p.epics ?? []) for (const t of e.tasks) each(t, e);
    for (const t of p.tasks ?? []) each(t, null);
  }
  const order = (g?: string) => (g === "This epic" ? 0 : g === "Other projects" ? 2 : 1);
  return out.sort((a, b) => order(a.group) - order(b.group));
}

/**
 * Relations, in Overview after the acceptance criteria: the three groups,
 * Add relation for every member, and a remove for the edge's owner only.
 */
export function TaskRelationsSection({ client, taskId, taskKey, relations, tree, here, onChanged, onOpen }: {
  client: ApiClient;
  taskId: string;
  taskKey: string;
  relations: TaskRelations;
  tree: ReadonlyArray<NavProject>;
  here: { projectId: string; epicId: string | null };
  onChanged: () => void;
  onOpen?: ((key: string) => void) | undefined;
}) {
  const [adding, setAdding] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);
  const all = useMemo(() => pickableTasks(tree, here), [tree, here.projectId, here.epicId]); // eslint-disable-line react-hooks/exhaustive-deps -- here, by its parts
  const keys = useMemo(() => new Set([...relations.blockedBy, ...relations.blocks, ...relations.related].map((v) => v.task.key)), [relations]);
  const find = useCallback(async (q: string) => {
    const s = q.trim().toLowerCase();
    return all.filter((t) => !s || `${t.key} ${t.title}`.toLowerCase().includes(s)).slice(0, 30);
  }, [all]);
  const add = async (side: RelationSide, key: string) => {
    try {
      await client.addRelation(taskId, side, key);
      onChanged();
      return undefined;
    } catch (err) {
      if (err instanceof ApiError && err.code === "cycle") {
        const loop = (err.details as { loop?: string[] } | undefined)?.loop;
        if (loop) return { loop };
      }
      return { error: errorText(err) };
    }
  };
  const remove = (group: RelationGroupKind, key: string) => {
    setProblem(null);
    client.removeRelation(taskId, SIDE_OF[group], key).then(onChanged, (err: unknown) => setProblem(errorText(err)));
  };
  return (
    <div className="taskBlock">
      <RelationList groups={groups(relations)} onRemove={remove} onAdd={() => setAdding(true)} onOpen={onOpen}
        adding={adding ? <RelationPicker here={{ key: taskKey }} related={keys} find={find} onAdd={add} onClose={() => setAdding(false)} /> : undefined} />
      {problem ? <Callout tone="danger">{problem}</Callout> : null}
    </div>
  );
}

// ---------------------------------------------------------------------------
// The board
// ---------------------------------------------------------------------------

/** The task a card's start or its dialog acts on. */
interface CardTask {
  readonly id: string;
  readonly key: string;
  readonly blockers: ReadonlyArray<RelatedTask>;
}

/**
 * The board's card starts and the one pair of dialogs they share. The
 * board draws `start(card, focus)`; its dialogs render once, in `dialogs`.
 * `start` is stable while `client` and `you` are: the board's cards are
 * memoised on it, and the callbacks are read when a card acts.
 */
export function useCardStarts(client: ApiClient, you: string | null, onStarted: (taskId: string, how: AutoStartHow) => void, onChanged: () => void): {
  start: (task: NavTask, focus: { tabIndex: 0 | -1 }) => ReactNode;
  dialogs: ReactNode;
  problem: string | null;
} {
  const [dialog, setDialog] = useState<{ kind: "anyway" | "auto"; task: CardTask } | null>(null);
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<string | null>(null);
  const [boardProblem, setBoardProblem] = useState<string | null>(null);
  const run = useCallback(async (what: () => Promise<unknown>, after: () => void, report: (s: string) => void) => {
    setBusy(true);
    try {
      await what();
      after();
    } catch (err) {
      report(errorText(err));
    } finally {
      setBusy(false);
    }
  }, []);
  const latest = useRef({ onStarted, onChanged });
  latest.current = { onStarted, onChanged };
  const start = useCallback((task: NavTask, focus: { tabIndex: 0 | -1 }): ReactNode => {
    if (!task.canStart || !task.key) return null;
    const blockers = task.blockedBy ?? [];
    const owner = task.people?.[0];
    const mine = !owner || owner.id === you;
    const ct: CardTask = { id: task.id, key: task.key, blockers };
    const auto = task.autoStart;
    const startNow = (how: AutoStartHow) => {
      setBoardProblem(null);
      void run(() => (how === "deliver" ? client.deliver(task.id) : client.talk(task.id)), () => latest.current.onStarted(task.id, how), setBoardProblem);
    };
    return (
      <StartMenu taskKey={task.key} blockers={blockers} ownerName={owner ? firstName(owner.name) : "its owner"} tabIndex={focus.tabIndex}
        onStart={startNow}
        onStartAnyway={blockers.length > 0 ? () => setDialog({ kind: "anyway", task: ct }) : undefined}
        onStartWhenUnblocked={mine ? () => setDialog({ kind: "auto", task: ct }) : undefined}
        startWhenUnblockedRefused={neverMerges(blockers)}
        auto={auto ? { how: auto.how, by: auto.by.id === you ? "You" : firstName(auto.by.name), ago: ago(auto.at),
          onCancel: mine ? () => void run(() => client.cancelAutoStart(task.id), () => latest.current.onChanged(), setBoardProblem) : undefined } : undefined} />
    );
  }, [client, you, run]);
  const close = (open: boolean) => {
    if (!open) {
      setDialog(null);
      setProblem(null);
    }
  };
  const t = dialog?.task;
  const blockers: DialogBlocker[] = [...(t?.blockers ?? [])];
  const dialogs = t ? (
    <>
      <StartAnywayDialog open={dialog.kind === "anyway"} onOpenChange={close} taskKey={t.key} blockers={blockers} busy={busy} problem={problem}
        onConfirm={(how, note) => run(() => {
          const s = { override: true as const, ...(note ? { note } : {}) };
          return how === "deliver" ? client.deliver(t.id, s) : client.talk(t.id, s);
        }, () => {
          setDialog(null);
          onStarted(t.id, how);
        }, setProblem)} />
      <AutoStartDialog open={dialog.kind === "auto"} onOpenChange={close} taskKey={t.key} blockers={blockers} busy={busy} problem={problem}
        onConfirm={(how) => run(() => client.setAutoStart(t.id, how), () => {
          setDialog(null);
          onChanged();
        }, setProblem)} />
    </>
  ) : null;
  return { start, dialogs, problem: boardProblem };
}

/** A start refused as blocked, in the words every path uses; null for any other failure. */
export function refusedWords(err: unknown, taskKey: string): string | null {
  const blockers = refusedBlockers(err);
  return blockers && blockers.length > 0 ? blockedRefusal(taskKey, blockers) : null;
}

/** The card the board lights (hovered or focused), by key: a store, so its changes re-render only what reads it. */
export interface LitCard {
  readonly set: (key: string | null) => void;
  readonly get: () => string | null;
  readonly subscribe: (fn: () => void) => () => void;
}

/** One LitCard for the component's life. */
export function useLitCard(): LitCard {
  return useMemo(() => {
    let key: string | null = null;
    const listeners = new Set<() => void>();
    return {
      set: (next) => {
        if (next === key) return;
        key = next;
        for (const fn of listeners) fn();
      },
      get: () => key,
      subscribe: (fn) => {
        listeners.add(fn);
        return () => listeners.delete(fn);
      },
    };
  }, []);
}

/** The epic's order, lit with the board's lit card and its relations. */
export function LitEpicOrder({ lit, project, epic, onOpen }: { lit: LitCard; project: NavProject; epic: NavEpic; onOpen?: ((key: string) => void) | undefined }) {
  const key = useSyncExternalStore(lit.subscribe, lit.get);
  const highlight = useMemo(() => {
    const task = key ? scopeTasks(project, epic).find((t) => t.key === key) : undefined;
    return task ? new Set([key!, ...neighbourKeys(task)]) : null;
  }, [key, project, epic]);
  return <EpicOrder epic={epic} onOpen={onOpen} highlight={highlight} />;
}

/** An epic's order for its board's overview: its steps and what can start; null for a single step. */
export function EpicOrder({ epic, onOpen, highlight }: { epic: NavEpic; onOpen?: ((key: string) => void) | undefined; highlight?: ReadonlySet<string> | null | undefined }) {
  const steps = dependencySteps(epic);
  if (steps.length < 2) return null;
  const strip: StripTask[][] = steps.map((s) => s.map((x) => ({
    key: x.key ?? x.id, title: x.title, status: x.status, unmerged: x.status === "done" && x.merged === false,
    startable: Boolean(x.startable), blockedBy: (x.blockedBy ?? []).map((b) => b.key),
  })));
  const all = epic.tasks;
  return (
    <DependencyStrip steps={strip} onOpen={onOpen} highlight={highlight}
      counts={{
        startable: all.filter((x) => x.startable).length,
        blocked: all.filter((x) => (x.blockedBy?.length ?? 0) > 0).length,
        working: all.filter((x) => ["running", "review", "ready_to_merge", "awaiting_input"].includes(x.status)).length,
        merged: all.filter((x) => x.status === "done" && x.merged).length,
        doneUnmerged: all.filter((x) => x.status === "done" && x.merged === false).length,
        failed: all.filter((x) => x.status === "failed").length,
      }} />
  );
}
