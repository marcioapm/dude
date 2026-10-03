/**
 * What a task's Chat shows beside its conductor's own conversation, when
 * the conductor takes the task's decisions: each Run it started, where it
 * started; each decision the delivery waited on it for; and what happened
 * on the pull requests that wakes nobody — an approval, checks passing,
 * ready to merge — as dude's notices. From the task's Runs and its ledger,
 * which the task page already reads.
 */

import { DECISION_POINT_LABEL, EventTypes, decisionPointSchema, runLabel, type PersistedEvent, type Run } from "@dude/domain";

export type ConductedLine =
  | { kind: "run"; id: string; at: string; run: Run }
  | { kind: "decision" | "notice"; id: string; at: string; text: string };

/** The task's conducted lines, oldest first; none for a task its conductor never decided for. */
export function conductedLines(task: { decider: string; runs: readonly Run[] }, events: readonly PersistedEvent[]): ConductedLine[] {
  const started = task.runs.filter((r) => r.conductorRunId);
  if (task.decider !== "conductor" && started.length === 0) return [];
  const out: ConductedLine[] = started.map((run) => ({ kind: "run", id: run.id, at: run.createdAt, run }));
  for (const e of events) {
    const p = (e.payload ?? {}) as Record<string, unknown>;
    switch (e.eventType) {
      case EventTypes.ConductorDecisionAwaited: {
        const point = decisionPointSchema.safeParse(p.point);
        if (point.success) out.push({ kind: "decision", id: e.eventId, at: e.occurredAt, text: `Waiting on the conductor: ${DECISION_POINT_LABEL[point.data]}.` });
        break;
      }
      case EventTypes.PullRequestReviewed:
        if (p.to === "approved") out.push({ kind: "notice", id: e.eventId, at: e.occurredAt, text: `${pr(p)} approved.` });
        break;
      case EventTypes.PullRequestChecksChanged:
        if (p.to === "passing") out.push({ kind: "notice", id: e.eventId, at: e.occurredAt, text: `${pr(p)}: checks passing.` });
        break;
      case "task.ready_to_merge":
        out.push({ kind: "notice", id: e.eventId, at: e.occurredAt, text: "Ready to merge: approved, checks passing. Merging is yours." });
        break;
    }
  }
  return out.sort((a, b) => a.at.localeCompare(b.at));
}

function pr(p: Record<string, unknown>): string {
  return typeof p.number === "number" ? `Pull request #${p.number}` : "The pull request";
}

/** What a Run the conductor started is, after its role: its review category, its phase. */
export function runWhat(run: Run): string {
  return run.category ? run.category : runLabel(run).toLowerCase();
}
