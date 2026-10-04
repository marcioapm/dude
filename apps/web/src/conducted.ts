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
 * The conductor's steers by the Run they went to, in order, each settled
 * by its receipts on the precedence a person's steer follows: delivered is
 * final; a failure before it says why; a read after a failure clears it.
 */
export function conductorSteers(events: readonly PersistedEvent[]): Map<string, ConductorSteer[]> {
  const out = new Map<string, ConductorSteer[]>();
  const byId = new Map<string, ConductorSteer>();
  for (const e of events) {
    const p = (e.payload ?? {}) as Record<string, unknown>;
    const id = typeof p.directiveId === "string" ? p.directiveId : "";
    if (e.eventType === EventTypes.RunSteered) {
      if (p.by !== "conductor" || !e.runId || !id) continue;
      const steer: ConductorSteer = { directiveId: id, text: String(p.text ?? ""), at: e.occurredAt, lands: null,
        deliveredAt: null, read: false, failed: null };
      byId.set(id, steer);
      out.set(e.runId, [...(out.get(e.runId) ?? []), steer]);
      continue;
    }
    const steer = byId.get(id);
    if (!steer) continue;
    switch (e.eventType) {
      case EventTypes.DirectiveAccepted:
        if (p.lands === "next_step" || p.lands === "next_turn") steer.lands = p.lands;
        break;
      case EventTypes.DirectiveDelivered:
        if (steer.deliveredAt !== null) break;
        steer.deliveredAt = e.occurredAt;
        steer.read = p.read === true;
        steer.failed = null;
        break;
      case EventTypes.DirectiveFailed:
        if (steer.deliveredAt === null) steer.failed = String(p.error ?? "") || "lux could not deliver it";
        break;
    }
  }
  return out;
}

/** What a Run the conductor started is, after its role: its review category, its phase. */
export function runWhat(run: Run): string {
  return run.category || runLabel(run).toLowerCase();
}
