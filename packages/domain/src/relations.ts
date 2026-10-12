/**
 * Task relations (docs/design/task-relations.md): the words every place
 * that names a blocker uses, the same as the orchestrator's
 * (delivery.BlockedError). A `blocks` edge A → B holds B's start until A
 * is merged: done, with a merged pull request.
 */

import type { TaskStatus } from "./hierarchy.ts";

/** The kinds stored. "Blocked by" is `blocks` read from the other side. */
export type TaskRelationKind = "blocks" | "related";

/** The kinds a person or agent names, from the task they are on. */
export type RelationSide = "blocked_by" | "blocks" | "related";

/** A not-started status: where Deliver and Talk it through are offered. */
export const NOT_STARTED_STATUSES: readonly TaskStatus[] = ["received", "intake", "awaiting_confirmation", "queued"];

export function notStarted(status: TaskStatus): boolean {
  return NOT_STARTED_STATUSES.includes(status);
}

/** An open blocker, as task_blocked_by returns it. */
export interface BlockerRef {
  key: string;
  status: TaskStatus;
}

const STATE_WORD: Partial<Record<TaskStatus, string>> = {
  review: "in review", running: "in progress", awaiting_input: "needs a person", ready_to_merge: "ready to merge",
  failed: "failed", aborted: "aborted", received: "not started", queued: "not started", intake: "being shaped",
  awaiting_confirmation: "plan to confirm",
};

/**
 * A blocker's state in words. An open blocker that is `done` was marked done
 * with nothing merged: it never releases anything, and is named so.
 */
export function blockerStateWord(status: TaskStatus, unmerged?: boolean): string {
  if (unmerged || status === "done") return "done, nothing merged";
  return STATE_WORD[status] ?? status.replace(/_/g, " ");
}

/** "CP-41 and CP-43", "CP-40, CP-41 and CP-43". */
export function keyList(keys: ReadonlyArray<string>): string {
  return keys.length > 1 ? `${keys.slice(0, -1).join(", ")} and ${keys.at(-1)}` : (keys[0] ?? "");
}

/** "it can start once that's merged" / "once they're merged". */
export function onceMerged(n: number): string {
  return `it can start once ${n === 1 ? "that's" : "they're"} merged`;
}

/** "CP-41 (in review) and CP-43 (in progress)". */
export function blockerList(blockers: ReadonlyArray<BlockerRef & { unmerged?: boolean | undefined }>): string {
  return keyList(blockers.map((b) => `${b.key} (${blockerStateWord(b.status, b.unmerged)})`));
}

/** The refusal every start path gives: "CP-44 is blocked by CP-41 (in review): it can start once that's merged." */
export function blockedRefusal(taskKey: string, blockers: ReadonlyArray<BlockerRef>): string {
  return `${taskKey} is blocked by ${blockerList(blockers)}: ${onceMerged(blockers.length)}.`;
}

/** "Starts when CP-41 merges", "Starts when CP-41 and CP-43 merge". */
export function startsWhen(blockers: ReadonlyArray<{ key: string }>): string {
  return `Starts when ${keyList(blockers.map((b) => b.key))} ${blockers.length === 1 ? "merges" : "merge"}`;
}

/** How a start when unblocked starts the task. */
export type AutoStartHow = "deliver" | "talk";

export const AUTO_START_HOW_LABEL: Record<AutoStartHow, string> = { deliver: "Deliver", talk: "Talk it through" };

/** Why a start when unblocked ended without firing (task.auto_start_cancelled's `why`). */
export type AutoStartEnd = "owner" | "started" | "owner_changed" | "unblocked_by_removal";

/** One relation as `GET /v1/tasks/:id/relations` lists it. */
export interface TaskRelationView {
  task: {
    id: string;
    key: string;
    title: string;
    status: TaskStatus;
    /** Its project's name, when it is not this task's. */
    project: string | null;
    /** Done with nothing merged. */
    unmerged: boolean;
    /** Merged: a blocker that holds nothing up any more. */
    merged: boolean;
    owner: { id: string; name: string } | null;
  };
  /** Who added it: a person's name, or "<owner>'s <role>" for an agent. */
  createdBy: { kind: "person" | "agent"; name: string; role?: string | null };
  createdAt: string;
  why: string | null;
  /** The reader may remove it. */
  canRemove: boolean;
  /** Whose it is to remove, by name, when the reader may not. */
  removableBy: string | null;
}

export interface TaskRelations {
  blockedBy: TaskRelationView[];
  blocks: TaskRelationView[];
  related: TaskRelationView[];
  /** The task's start when unblocked, if set. */
  autoStart: { how: AutoStartHow; by: { id: string; name: string }; at: string } | null;
  /** The reader owns the task (sets and cancels start when unblocked). */
  owner: { id: string; name: string } | null;
}
