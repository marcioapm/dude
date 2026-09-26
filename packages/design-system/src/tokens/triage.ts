/**
 * Triage vocabulary — the operator's four questions, as buckets.
 *
 * `status.ts` says how one state *looks*. This table says which of the
 * operator's questions a state *answers*: what needs me, what is active,
 * what is ready, what failed. Every domain status lands in exactly one
 * bucket, and the buckets are what a collapsed parent rolls up — a project
 * header can say "2 need you · 5 active · 1 ready" without listing anything.
 *
 * Keyed on the imported domain unions, so a new status is a compile error
 * here until it is placed. That is the point: a status that silently fell
 * into no bucket would be invisible in every roll-up.
 *
 * Rules:
 * - `needs_you` is the only bucket that may be loud. It reuses the
 *   `awaiting_input` treatment (diamond, ring) wherever it is drawn.
 * - `waiting` and `done` are counted nowhere. Queued work and finished
 *   work are the calm majority; rolling them up would be noise.
 * - `paused` is `waiting`, not `needs_you`. The operator paused it; it is
 *   not asking for anything.
 * - `aborted` is `done`. Stopping a run is a decision, not a failure.
 */

import type { ToneName } from "./palette.ts";
import type { Status } from "./status.ts";

export const TRIAGE_KINDS = ["needs_you", "active", "ready", "failed", "waiting", "done"] as const;
export type TriageKind = (typeof TRIAGE_KINDS)[number];

export interface TriageSpec {
  readonly label: string;
  /** Short form for a count ("2 need you"). */
  readonly countLabel: (n: number) => string;
  readonly tone: ToneName;
  /**
   * The status whose `StatusBadge` treatment stands for this bucket. The
   * roll-up draws this, so a bucket never invents a second mark.
   */
  readonly status: Status;
  /** Whether a collapsed parent counts this bucket. */
  readonly counted: boolean;
  /** Display order in a roll-up, most urgent first. */
  readonly rank: number;
  readonly description: string;
}

export const TRIAGE_SPECS: Record<TriageKind, TriageSpec> = {
  needs_you: {
    label: "Needs you",
    countLabel: (n) => (n === 1 ? "1 needs you" : `${n} need you`),
    tone: "attention",
    status: "awaiting_input",
    counted: true,
    rank: 0,
    description: "Blocked on a person. Nothing proceeds until someone acts.",
  },
  active: {
    label: "Active",
    countLabel: (n) => `${n} active`,
    tone: "info",
    status: "running",
    counted: true,
    rank: 1,
    description: "An agent is working on it right now.",
  },
  ready: {
    label: "Ready",
    countLabel: (n) => `${n} ready`,
    tone: "success",
    status: "ready_to_merge",
    counted: true,
    rank: 2,
    description: "Finished and waiting to be merged.",
  },
  failed: {
    label: "Failed",
    countLabel: (n) => `${n} failed`,
    tone: "danger",
    status: "failed",
    counted: true,
    rank: 3,
    description: "Ended with an error. Retry or abandon.",
  },
  waiting: {
    label: "Waiting",
    countLabel: (n) => `${n} waiting`,
    tone: "neutral",
    status: "queued",
    counted: false,
    rank: 4,
    description: "Queued, scheduled, paused or in review. Nothing to do yet.",
  },
  done: {
    label: "Done",
    countLabel: (n) => `${n} done`,
    tone: "neutral",
    status: "done",
    counted: false,
    rank: 5,
    description: "Merged, completed or deliberately stopped.",
  },
};

/** Which bucket each domain status answers. */
export const TRIAGE_FOR_STATUS: Record<Status, TriageKind> = {
  // shared / run / session
  pending: "waiting",
  scheduled: "waiting",
  starting: "active",
  running: "active",
  paused: "waiting",
  completed: "done",
  failed: "failed",
  aborted: "done",
  awaiting_input: "needs_you",
  // task
  received: "waiting",
  intake: "active",
  awaiting_confirmation: "needs_you",
  queued: "waiting",
  review: "waiting",
  ready_to_merge: "ready",
  done: "done",
};

export function triageOf(status: Status): TriageKind {
  return TRIAGE_FOR_STATUS[status];
}

export function triageSpec(kind: TriageKind): TriageSpec {
  return TRIAGE_SPECS[kind];
}

/** Buckets a roll-up shows, most urgent first. */
export const COUNTED_TRIAGE_KINDS: readonly TriageKind[] = TRIAGE_KINDS.filter((k) => TRIAGE_SPECS[k].counted).sort(
  (a, b) => TRIAGE_SPECS[a].rank - TRIAGE_SPECS[b].rank,
);

export type TriageCounts = Readonly<Record<TriageKind, number>>;

export const EMPTY_TRIAGE_COUNTS: TriageCounts = {
  needs_you: 0,
  active: 0,
  ready: 0,
  failed: 0,
  waiting: 0,
  done: 0,
};

export function addTriage(counts: TriageCounts, kind: TriageKind, n = 1): TriageCounts {
  return { ...counts, [kind]: counts[kind] + n };
}

export function sumTriage(list: ReadonlyArray<TriageCounts>): TriageCounts {
  const out: Record<TriageKind, number> = { ...EMPTY_TRIAGE_COUNTS };
  for (const c of list) for (const k of TRIAGE_KINDS) out[k] += c[k];
  return out;
}

export function hasCountedTriage(counts: TriageCounts): boolean {
  return COUNTED_TRIAGE_KINDS.some((k) => counts[k] > 0);
}
