/**
 * A task's history in one line, for the head of its Chat: how it went,
 * what ran, and what it came to — from what the task page already reads
 * (its Runs, findings and pull requests, and its cost), nothing new.
 */

import { TERMINAL_RUN_STATUSES, isConductor, type Finding, type PullRequest, type Run, type TaskStatus } from "@dude/domain";

export interface TaskHistoryLine {
  lead: string;
  steps: string[];
  facts: string[];
}

/** Each phase as a person names who did it. */
const DOER: Record<string, string> = {
  investigate: "investigator",
  implement: "implementer",
  review: "reviewer",
  fix: "fixer",
  simplify: "simplifier",
  test: "tester",
};

/** A finding is settled once it is not open. */
const SETTLED: ReadonlySet<Finding["status"]> = new Set(["resolved", "accepted", "superseded"]);

/**
 * The line: the latest attempt's phases in order, a fan-out of one phase
 * (reviewers in parallel) folded into "reviewers ×3", each pull request,
 * then the findings and how they stand, and the cost when known.
 */
export function taskHistory(task: { status: TaskStatus; runs: readonly Run[] }, findings: readonly Finding[],
  pullRequests: readonly PullRequest[], costUsd: number | null, format: (usd: number) => string): TaskHistoryLine {
  const phases = task.runs.filter((r) => r.phase && !isConductor(r));
  const attempt = Math.max(0, ...phases.map((r) => r.attempt));
  const ran = phases.filter((r) => r.attempt === attempt).sort((a, b) => a.createdAt.localeCompare(b.createdAt));

  const steps: string[] = [];
  let last: { phase: string; n: number } | null = null;
  for (const run of ran) {
    const phase = run.phase!;
    if (last && last.phase === phase) {
      last.n++;
      steps[steps.length - 1] = `${DOER[phase] ?? phase}s ×${last.n}`;
      continue;
    }
    last = { phase, n: 1 };
    steps.push(DOER[phase] ?? phase);
  }
  const prs = [...pullRequests].sort((a, b) => a.createdAt.localeCompare(b.createdAt));
  for (const pr of prs) steps.push(prs.length > 1 ? `${pr.repositoryName}#${pr.number}` : `PR #${pr.number}`);

  const facts: string[] = [];
  if (findings.length > 0) {
    const open = findings.filter((f) => !SETTLED.has(f.status)).length;
    const n = `${findings.length} finding${findings.length === 1 ? "" : "s"}`;
    facts.push(open === 0 ? `${n}, all settled` : `${n}, ${open} open`);
  }
  if (costUsd !== null && costUsd > 0) facts.push(format(costUsd));

  return { lead: leadFor(task.status, ran), steps, facts };
}

function leadFor(status: TaskStatus, ran: readonly Run[]): string {
  if (ran.length === 0) return status === "done" ? "Done" : status === "aborted" ? "Stopped" : "Not started";
  if (status === "done") return "Delivered automatically, merged";
  if (status === "aborted") return "Delivered automatically, then closed";
  if (status === "failed") return "Delivery failed";
  if (status === "awaiting_input") return "Delivering automatically, waiting on you";
  if (ran.some((r) => !TERMINAL_RUN_STATUSES.includes(r.status))) return "Delivering automatically";
  return "Delivered automatically";
}
