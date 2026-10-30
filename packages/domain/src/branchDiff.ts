/**
 * A task attempt's whole branch, one repository at a time, as a pinned
 * snapshot (task_branch_snapshots, migration 230): the committed branch
 * head against its target, `git diff <target>...<head>`, read from the
 * repository's object graph (GitHub's compare), never by putting Run
 * diffs together. The merge base is recomputed at every capture, so a
 * merge of the target into the branch moves it forward and the diff stays
 * the branch's own changes. Uncommitted work of live sessions is never in
 * it. `GET /v1/tasks/:id/branch-diff`.
 */

import type { RelocatedAnchor } from "./feedbackAnchor.ts";
import type { RunDiffFile } from "./live.ts";

/**
 * How a snapshot's head relates to the one before it: `initial` (none
 * before), `fast-forward` (descends, merge base unchanged), `merged-target`
 * (descends, merge base moved: the target was merged in), `rewritten` (does
 * not descend: a rebase or a force push).
 */
export const BRANCH_RELATIONS = ["initial", "fast-forward", "merged-target", "rewritten"] as const;
export type BranchRelation = (typeof BRANCH_RELATIONS)[number];

/** A snapshot's totals: GitHub's whole counts, whatever lines were kept. */
export interface TaskBranchStats {
  files: number;
  additions: number;
  deletions: number;
  /** Files whose lines were left out (too large to show): each file says so too. */
  truncatedFiles: number;
  /** Binary files: changed, with no lines to show. */
  binaryFiles: number;
  /** GitHub's compare lists at most 300 files: at it, more may have changed than listed. */
  fileCapReached: boolean;
}

/** One snapshot, as captured. */
export interface TaskBranchSnapshot {
  id: string;
  taskId: string;
  attempt: number;
  repositoryId: string;
  /** The repository's name, as runs' heads and code anchors name it. */
  repository: string;
  branchRef: string;
  targetRef: string;
  /** What the target resolved to at capture. */
  targetSha: string;
  /** merge-base(target, head), computed at capture: the diff's old side. */
  mergeBaseSha: string;
  headSha: string;
  /** Identifies this diff: a feedback anchor written on it carries it. */
  checksum: string;
  source: "github-compare";
  capturedAt: string;
  /** The files, as a Run's diff has them; each says when it is truncated or binary. */
  files: RunDiffFile[];
  stats: TaskBranchStats;
  /** Some file's lines were left out, or the file list was cut at GitHub's cap. */
  truncated: boolean;
  /** `partial`: the file list may be incomplete; `reason` says why. */
  status: "complete" | "partial";
  reason: string | null;
  /** The published pull request's head when it was captured, if one was open. */
  prHeadSha: string | null;
  previousSnapshotId: string | null;
  relation: BranchRelation;
}

/**
 * Whether there is a snapshot to show: `ok`; `preparing` (a capture is
 * asked for and none is recorded yet); `unavailable` (GitHub could not be
 * read: why — never an empty diff); `none` (nothing to show: no branch
 * pushed yet, no repository, or an earlier attempt that recorded none).
 */
export type TaskBranchAvailability =
  | { state: "ok" }
  | { state: "preparing" }
  | { state: "unavailable"; reason: string }
  | { state: "none"; reason: string };

/** A newer snapshot than the one returned, for "New changes available · …". */
export interface TaskBranchNewer {
  id: string;
  /**
   * From the returned snapshot to this one, over every capture between:
   * `rewritten` if any step was, else `merged-target` if the merge base
   * moved, else `fast-forward`.
   */
  relation: Exclude<BranchRelation, "initial">;
  /** The returned snapshot's merge base and head ("a" in "base moved a → b"). */
  fromMergeBaseSha: string;
  fromHeadSha: string;
  mergeBaseSha: string;
  headSha: string;
  capturedAt: string;
  /** Snapshots captured after the returned one. */
  count: number;
}

/** One repository of the attempt, for the repository picker. */
export interface TaskBranchRepository {
  id: string;
  name: string;
  access: "write" | "read";
  /** Snapshots recorded for it on this attempt. */
  snapshots: number;
}

/** The pull request published from this branch, when there is one. */
export interface TaskBranchPullRequest {
  number: number;
  url: string;
  state: "draft" | "open" | "merged" | "closed";
  /** Its head as dude last recorded it; may differ from the snapshot's head. */
  headSha: string;
  /** When dude last read it from GitHub (or recorded its own push), null if never. */
  syncedAt: string | null;
}

/** A capture asked for or refused after the returned snapshot (its head may be newer). */
export interface TaskBranchCapture {
  state: "pending" | "unavailable" | "absent";
  reason: string | null;
  requestedAt: string;
}

/** `GET /v1/tasks/:id/branch-diff?attempt=&repository=&snapshot=`. */
export interface TaskBranchDiff {
  taskId: string;
  attempt: number;
  currentAttempt: number;
  /** An earlier attempt: its recorded snapshots, read-only (no new drafts on them). */
  readOnly: boolean;
  /** The repository shown, by name; null when the task has none. */
  repository: string | null;
  repositories: TaskBranchRepository[];
  availability: TaskBranchAvailability;
  /** The latest (or the named) snapshot; null unless availability is ok. */
  snapshot: TaskBranchSnapshot | null;
  /** Set when the returned snapshot is not the newest of its attempt and repository. */
  newer: TaskBranchNewer | null;
  pullRequest: TaskBranchPullRequest | null;
  capture: TaskBranchCapture | null;
  /**
   * Where each branch-anchored item of this attempt and repository the
   * reader may see (submitted ones, and their own drafts) sits on the
   * returned snapshot: `relocateCodeAnchor`, run here. `lines` is on its
   * lines (or on its own snapshot), `outdated` under its file's header with
   * its original excerpt, `unchanged` outside every hunk, `absent` when its
   * file is not in this snapshot. Empty unless availability is ok.
   */
  placements: TaskBranchPlacement[];
}

export interface TaskBranchPlacement {
  itemId: string;
  /** The snapshot the item was written on. */
  snapshotId: string;
  at: RelocatedAnchor;
}

/**
 * The relation over a span of captures, from one snapshot to a later one:
 * `rewritten` if any step was (or a step started a new lineage), else
 * `merged-target` if the merge base moved, else `fast-forward`.
 */
export function spanRelation(steps: ReadonlyArray<BranchRelation>, fromMergeBase: string, toMergeBase: string):
    Exclude<BranchRelation, "initial"> {
  if (steps.some((r) => r === "rewritten" || r === "initial")) return "rewritten";
  return fromMergeBase === toMergeBase ? "fast-forward" : "merged-target";
}
