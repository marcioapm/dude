/**
 * A task's attempts: Start over gives a task a new one (a new branch, the
 * whole pipeline again), and the task page shows one at a time. What
 * belongs to which attempt is read from the Runs: a Run carries its
 * attempt, and a finding, a file or a pull request belongs to the attempt
 * of the Run that made it.
 */

import { firstName } from "@dude/design-system";
import { runLabel, type PersistedEvent } from "@dude/domain";
import type { PullRequest, Run, TaskDetail } from "./api/client.ts";
import { actorName, humanActor } from "./api/conversation.ts";
import { howRunStopped } from "./screens/Recovery.tsx";
import type { People } from "./people.tsx";

/** Every attempt the task has had, newest first: distinct `attempt` over its agent Runs. */
export function attemptsOf(runs: readonly Run[]): number[] {
  return [...new Set(runs.filter((r) => r.kind !== "preview").map((r) => r.attempt))].sort((a, b) => b - a);
}

/** The attempt a Run's session is listed under: a branch preview serves the task now, so the current one. */
export function attemptOfRun(run: Run, current: number): number {
  return run.kind === "preview" ? current : run.attempt;
}

/** A task's Runs by id, built once per read of them for the lookups below. */
export type RunsById = ReadonlyMap<string, Run>;
export const runsById = (runs: readonly Run[]): RunsById => new Map(runs.map((r) => [r.id, r]));

/**
 * The attempt of what a Run made (a finding, a file): that Run's. One whose
 * Run is not known — deleted, or never recorded — is the current attempt's,
 * where it stays in sight.
 */
export function attemptOfWork(runId: string | null, runs: RunsById, current: number): number {
  const run = runId ? runs.get(runId) : undefined;
  return run ? attemptOfRun(run, current) : current;
}

/**
 * The attempt a pull request belongs to: that of the Run that opened it;
 * with that Run unknown, the one its branch names (`dude/<task>/attempt-N`),
 * else the current one, as for a finding or a file.
 */
export function attemptOfPr(pr: PullRequest, runs: RunsById, current: number): number {
  const run = pr.runId ? runs.get(pr.runId) : undefined;
  if (run) return run.attempt;
  const named = /\/attempt-([1-9]\d*)$/.exec(pr.headBranch);
  return named ? Number(named[1]) : current;
}

/** The pull request a `pull_request.*` event is about: by its number, and its repository when several share the number. */
export function prOfEvent(e: PersistedEvent, prs: readonly PullRequest[]): PullRequest | undefined {
  const same = prs.filter((p) => p.number === e.payload.number);
  return same.find((p) => p.repositoryName === e.payload.repo) ?? same[0];
}

/**
 * When an earlier attempt's pull request was closed by its start over, or
 * null when it was not. The sync records every close on GitHub alike (no
 * person), so the closer is read from when: recover closes the attempt's
 * open pull requests right after the restart, and a close before the
 * restart was someone's on GitHub.
 */
export function closedAtStartOver(pr: PullRequest, events: readonly PersistedEvent[], restartAt: string | null): string | null {
  if (!restartAt) return null;
  const closed = events.findLast((e) => e.eventType === "pull_request.closed" && e.payload.number === pr.number &&
    (e.payload.repo === undefined || e.payload.repo === pr.repositoryName));
  return closed && !humanActor(closed) && closed.occurredAt >= restartAt ? closed.occurredAt : null;
}

/** When each attempt began: its first agent Run's creation. */
export function attemptStarts(runs: readonly Run[]): Map<number, string> {
  const starts = new Map<number, string>();
  for (const r of runs) {
    if (r.kind === "preview") continue;
    const at = starts.get(r.attempt);
    if (at === undefined || r.createdAt < at) starts.set(r.attempt, r.createdAt);
  }
  return starts;
}

/**
 * The attempt an event happened in: the one a start over began, its Run's,
 * its pull request's, else the attempt under way when it happened. Built
 * once per pass over the ledger; asked only of the events that make a line.
 */
export function eventAttempts(runs: readonly Run[], prs: readonly PullRequest[], current: number): (e: PersistedEvent) => number {
  const byId = runsById(runs);
  // Latest first, for an event on no known Run.
  const begun = [...attemptStarts(runs)].sort((a, b) => b[0] - a[0]);
  return (e) => {
    if (e.eventType === "task.recovered" && e.payload.action === "restart" && typeof e.payload.attempt === "number") return e.payload.attempt;
    const run = e.runId ? byId.get(e.runId) : undefined;
    if (run) return attemptOfRun(run, current);
    if (e.eventType.startsWith("pull_request.")) {
      const pr = prOfEvent(e, prs);
      if (pr) return attemptOfPr(pr, byId, current);
    }
    return begun.find(([, at]) => at <= e.occurredAt)?.[0] ?? 1;
  };
}

/** An earlier attempt's last aborted or failed Run: where it stopped. */
function stoppedRunOf(runs: readonly Run[], attempt: number): Run | undefined {
  return runs.filter((r) => r.attempt === attempt && r.kind !== "preview" && (r.status === "aborted" || r.status === "failed"))
    .sort((a, b) => (a.endedAt ?? a.createdAt).localeCompare(b.endedAt ?? b.createdAt)).at(-1);
}

/** How an earlier attempt ended, for its status mark: the status of the Run it stopped at. */
function attemptEnd(runs: readonly Run[], attempt: number): "aborted" | "failed" {
  return stoppedRunOf(runs, attempt)?.status === "failed" ? "failed" : "aborted";
}

/** An attempt's status mark: the task's for the current one, how it ended for an earlier one. */
export const attemptStatus = (task: TaskDetail, attempt: number, current: number) =>
  attempt === current ? task.status : attemptEnd(task.runs, attempt);

export interface SetAside {
  /** Who started over, by name; null when the ledger does not say. */
  by: string | null;
  at: string | null;
  note: string | null;
  /** "stopped at Fix, aborted by Ana", or null when it stopped on no Run. */
  how: string | null;
}

/**
 * Why an earlier attempt is set aside: the `task.recovered` that started
 * the next one over (who, when, their note), and where it had stopped.
 */
export function setAsideOf(attempt: number, runs: readonly Run[], events: readonly PersistedEvent[], people: People): SetAside {
  const restart = events.find((e) => e.eventType === "task.recovered" && e.payload.action === "restart" && e.payload.attempt === attempt + 1);
  const stopped = stoppedRunOf(runs, attempt);
  const by = stopped ? howRunStopped(stopped, events, people, 120).by : null;
  const how = stopped
    ? `stopped at ${runLabel(stopped)}${stopped.status === "failed" ? ", failed" : by ? `, aborted by ${firstName(by)}` : ", aborted"}`
    : null;
  return {
    by: restart ? actorName(humanActor(restart), people.names) : null,
    at: restart?.occurredAt ?? null,
    note: typeof restart?.payload.note === "string" && restart.payload.note ? restart.payload.note : null,
    how,
  };
}
