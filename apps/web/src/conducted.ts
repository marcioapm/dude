/**
 * What a task's Chat shows beside its conductor's own conversation, when
 * the conductor takes the task's decisions: each Run it started, where it
 * started; each decision the delivery waited on it for; and what happened
 * on the pull requests that wakes nobody — an approval, checks passing,
 * ready to merge — as dude's notices. From the task's Runs and its ledger,
 * which the task page already reads.
 */

import { DECISION_POINT_LABEL, EventTypes, decisionPointSchema, runLabel, type PersistedEvent, type Run } from "@dude/domain";
import { apply, emptyProjection } from "./api/conversation.ts";

export type ConductedLine =
  | { kind: "run"; id: string; at: string; run: Run; steers: ConductorSteer[] }
  | { kind: "decision" | "notice"; id: string; at: string; text: string };

/**
 * A steer the conductor sent one of its Runs, and what became of it, as a
 * person's steer's turn says it: sent, taken (with where it lands), read,
 * or not delivered and why.
 */
export interface ConductorSteer {
  directiveId: string;
  text: string;
  at: string;
  /** Where the harness said it lands, once lux took it. */
  lands: "next_step" | "next_turn" | null;
  /** When the agent read it, or (an older lux) had it handed over. */
  deliveredAt: string | null;
  read: boolean;
  failed: string | null;
}

/** The task's conducted lines, oldest first; none for a task its conductor never decided for. */
export function conductedLines(task: { decider: string; runs: readonly Run[] }, events: readonly PersistedEvent[]): ConductedLine[] {
  const started = task.runs.filter((r) => r.conductorRunId);
  if (task.decider !== "conductor" && started.length === 0) return [];
  const steers = conductorSteers(events);
  const out: ConductedLine[] = started.map((run) => ({ kind: "run", id: run.id, at: run.createdAt, run, steers: steers.get(run.id) ?? [] }));
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

/**
 * The conductor's steers by the Run they went to, in the order it sent
 * them, each settled as the Run's session settles it (conversation.ts
 * apply): delivered is final; a failure before it says why; a read after a
 * failure clears it; a person's Retry or Interrupt now with the same words
 * is the same steer, its receipts the steer's own.
 */
export function conductorSteers(events: readonly PersistedEvent[]): Map<string, ConductorSteer[]> {
  const byRun = new Map<string, PersistedEvent[]>();
  for (const e of events) {
    if (!e.runId || !STEER_EVENTS.has(e.eventType)) continue;
    byRun.set(e.runId, [...(byRun.get(e.runId) ?? []), e]);
  }
  const out = new Map<string, ConductorSteer[]>();
  for (const [runId, runEvents] of byRun) {
    const turns = new Map(apply(emptyProjection(), runEvents).turns.map((t) => [t.id, t]));
    const steers: ConductorSteer[] = [];
    for (const e of runEvents) {
      const p = (e.payload ?? {}) as Record<string, unknown>;
      const turn = turns.get(e.eventId);
      if (e.eventType !== EventTypes.RunSteered || p.by !== "conductor" || typeof p.directiveId !== "string" || turn?.kind !== "human") continue;
      steers.push({ directiveId: p.directiveId, text: turn.text, at: turn.at, lands: turn.lands, deliveredAt: turn.deliveredAt,
        read: turn.read, failed: turn.failed });
    }
    if (steers.length > 0) out.set(runId, steers);
  }
  return out;
}

const STEER_EVENTS = new Set<string>([EventTypes.RunSteered, EventTypes.DirectiveAccepted, EventTypes.DirectiveDelivered,
  EventTypes.DirectiveFailed]);

/** What a Run the conductor started is, after its role: its review category, its phase. */
export function runWhat(run: Run): string {
  return run.category || runLabel(run).toLowerCase();
}
