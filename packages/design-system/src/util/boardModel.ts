/**
 * View model for the board — pure functions, no React.
 *
 * The board is the overview for a project or an epic: the same `NavProject`
 * / `NavEpic` / `NavTask` shapes the sidebar consumes, laid out by
 * lifecycle stage instead of by hierarchy. The app hands over the nav model
 * it already built; nothing here fetches.
 *
 * Eleven task statuses would be eleven columns, which is a spreadsheet.
 * The five columns below are the stages the operator actually watches: is
 * work waiting to be shaped, waiting for a worker, being worked, waiting to
 * land, or finished. "Needs you" is deliberately not a column — it is a
 * condition that can strike at any stage, so it is a card treatment and a
 * sort order, exactly as it is a row treatment in the tree.
 *
 * Keyed on the domain union, so a new status is a compile error here before
 * it can be a blank column.
 */

import type { AgentRole, TaskStatus } from "@dude/domain";
import { EMPTY_TRIAGE_COUNTS, TRIAGE_SPECS, addTriage, sumTriage, type TriageCounts, type TriageKind } from "../tokens/triage.ts";
import { askingSession, currentRun, taskTriage, type NavEpic, type NavProject, type NavRef, type NavSession, type NavTask } from "./navModel.ts";

export const BOARD_COLUMN_KINDS = ["intake", "queued", "running", "review", "closed"] as const;
export type BoardColumnKind = (typeof BOARD_COLUMN_KINDS)[number];

export interface BoardColumnSpec {
  readonly label: string;
  readonly description: string;
}

export const BOARD_COLUMN_SPECS: Record<BoardColumnKind, BoardColumnSpec> = {
  intake: { label: "Intake", description: "Received, being analysed, or waiting for its plan to be confirmed." },
  queued: { label: "Queued", description: "Plan approved; waiting for a worker." },
  running: { label: "In progress", description: "A run is active, or blocked on a person mid-run." },
  review: { label: "Review", description: "A PR is open: under review, or ready to merge." },
  closed: { label: "Closed", description: "Merged, failed or aborted. Failed items sort first." },
};

/** Which lane each domain status sits in. */
export const BOARD_COLUMN_FOR_STATUS: Record<TaskStatus, BoardColumnKind> = {
  received: "intake",
  intake: "intake",
  awaiting_confirmation: "intake",
  queued: "queued",
  running: "running",
  awaiting_input: "running",
  review: "review",
  ready_to_merge: "review",
  done: "closed",
  failed: "closed",
  aborted: "closed",
};

export function boardColumnOf(status: TaskStatus): BoardColumnKind {
  return BOARD_COLUMN_FOR_STATUS[status];
}

export interface BoardCard {
  readonly task: NavTask;
  /** Null for a task outside any epic. */
  readonly epic: NavEpic | null;
  readonly column: BoardColumnKind;
  readonly triage: TriageKind;
  /** The session asking, when the block is at session level. */
  readonly asking: NavSession | null;
}

export interface BoardColumn {
  readonly kind: BoardColumnKind;
  readonly spec: BoardColumnSpec;
  /** Most urgent bucket first, then the caller's order. */
  readonly cards: ReadonlyArray<BoardCard>;
  readonly counts: TriageCounts;
  readonly costUsd: number;
}

function toCard(task: NavTask, epic: NavEpic | null): BoardCard {
  const run = currentRun(task);
  return {
    task,
    epic,
    column: boardColumnOf(task.status),
    triage: taskTriage(task),
    asking: run ? askingSession(run.sessions) : null,
  };
}

/**
 * Every card in scope. With an epic, only its tasks; otherwise the
 * whole project — each epic in order, then the loose tasks.
 */
export function boardCards(project: NavProject, epic?: NavEpic | null): BoardCard[] {
  if (epic) return epic.tasks.map((wi) => toCard(wi, epic));
  const out: BoardCard[] = [];
  for (const e of project.epics ?? []) for (const wi of e.tasks) out.push(toCard(wi, e));
  for (const wi of project.tasks ?? []) out.push(toCard(wi, null));
  return out;
}

/**
 * The five columns, always all five and always in order, so the board's
 * shape never changes with its contents. Within a column, cards sort by
 * triage rank — needs-you at the top, then active, ready, failed — and are
 * otherwise left in the caller's order, which is where recency belongs.
 */
export function boardColumns(project: NavProject, epic?: NavEpic | null): BoardColumn[] {
  return columnsOf(boardCards(project, epic));
}

function columnsOf(cards: ReadonlyArray<BoardCard>): BoardColumn[] {
  const byKind: Record<BoardColumnKind, BoardCard[]> = { intake: [], queued: [], running: [], review: [], closed: [] };
  for (const c of cards) byKind[c.column].push(c);
  return BOARD_COLUMN_KINDS.map((kind) => {
    const sorted = byKind[kind].sort((a, b) => TRIAGE_SPECS[a.triage].rank - TRIAGE_SPECS[b.triage].rank);
    let counts = EMPTY_TRIAGE_COUNTS;
    let costUsd = 0;
    for (const c of sorted) {
      counts = addTriage(counts, c.triage);
      costUsd += c.task.costUsd ?? 0;
    }
    return { kind, spec: BOARD_COLUMN_SPECS[kind], cards: sorted, counts, costUsd };
  });
}

/** Key of the "No epic" swimlane. */
export const NO_EPIC_LANE = "none";

export interface BoardSwimlane {
  /** `epic:<id>` or `NO_EPIC_LANE`. Stable across renders, so collapse state can key on it. */
  readonly key: string;
  /** Null for the loose tasks. */
  readonly epic: NavEpic | null;
  readonly title: string;
  readonly columns: ReadonlyArray<BoardColumn>;
  readonly count: number;
  readonly counts: TriageCounts;
  readonly costUsd: number;
}

/**
 * The project board read by epic: one swimlane per epic in the order the
 * project lists them (that order is the operator's), each holding the same
 * five columns, then "No epic" for the loose tasks when there are
 * any. An epic with nothing in it still gets its row — the order set in
 * the tree must be visible here — but the row is empty, not five rails.
 */
export function boardSwimlanes(project: NavProject): BoardSwimlane[] {
  const out: BoardSwimlane[] = [];
  for (const e of project.epics ?? []) out.push(swimlane(`epic:${e.id}`, e, e.title, e.tasks.map((wi) => toCard(wi, e))));
  const loose = (project.tasks ?? []).map((wi) => toCard(wi, null));
  if (loose.length > 0) out.push(swimlane(NO_EPIC_LANE, null, "No epic", loose));
  return out;
}

function swimlane(key: string, epic: NavEpic | null, title: string, cards: ReadonlyArray<BoardCard>): BoardSwimlane {
  const columns = columnsOf(cards);
  return { key, epic, title, columns, count: cards.length, counts: sumTriage(columns.map((c) => c.counts)), costUsd: boardCost(columns) };
}

export function boardCardCount(columns: ReadonlyArray<BoardColumn>): number {
  return columns.reduce((n, c) => n + c.cards.length, 0);
}

export function boardCost(columns: ReadonlyArray<BoardColumn>): number {
  return columns.reduce((n, c) => n + c.costUsd, 0);
}

export interface LiveActivity {
  readonly role: AgentRole;
  readonly activity: string;
}

/**
 * What a task is doing right now: the deepest running session that
 * says so. Deepest, because the orchestrator's line is usually "waiting for
 * the implementer" and the implementer's is the one that changes.
 */
export function liveActivity(wi: NavTask): LiveActivity | null {
  const walk = (list: ReadonlyArray<NavSession>): LiveActivity | null => {
    for (const s of list) {
      const deeper = walk(s.children ?? []);
      if (deeper) return deeper;
      if (s.status === "running" && s.activity) return { role: s.role, activity: s.activity };
    }
    return null;
  };
  const run = currentRun(wi);
  return run ? walk(run.sessions) : null;
}

export interface BoardScope {
  readonly project: NavProject;
  readonly epic: NavEpic | null;
}

/**
 * The board a sidebar selection opens: a project ref is the project board,
 * an epic ref is that epic's board. Anything else (a task, a session)
 * opens the transcript instead and resolves to null.
 */
export function boardScope(projects: ReadonlyArray<NavProject>, ref: NavRef | null | undefined): BoardScope | null {
  if (!ref) return null;
  if (ref.kind === "project") {
    const project = projects.find((p) => p.id === ref.id);
    return project ? { project, epic: null } : null;
  }
  if (ref.kind === "epic") {
    for (const project of projects) {
      const epic = (project.epics ?? []).find((e) => e.id === ref.id);
      if (epic) return { project, epic };
    }
  }
  return null;
}
