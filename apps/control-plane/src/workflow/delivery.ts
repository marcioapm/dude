/**
 * The side-effecting half of the delivery workflow.
 *
 * Separated from `delivery.workflow.ts` so the state machine there reads as
 * transitions rather than SQL. Each function here is one durable action the
 * workflow takes — create a phase Run, read what it produced, record findings
 * — and the runtime persists the transition before acting on it, so a crash
 * between two of these replays rather than skips.
 */

import { EventTypes, newId } from "@dude/domain";
import { withOrg } from "../db/client.ts";
import { appendInScope } from "../events/ledger.ts";
import { eventBus } from "../events/bus.ts";
import { PHASE_PUBLISHES, ROLE_FOR_PHASE, type FindingSeverity } from "./policy.ts";
import type { ActionableFeedback } from "../forge/classify.ts";

export interface PhaseRunInput {
  workItemId: string;
  phase: "investigate" | "implement" | "review" | "fix" | "simplify" | "test";
  /** The commit this phase's workspace starts from. Null means default branch. */
  baseRef: string | null;
  parentRunId: string | null;
  /** Review phase only: which reviewer flavour this Run is. */
  category?: string;
  /** Fix phase only: the findings this Run must address. */
  findingIds?: string[];
  /** Fix phase only: PR feedback this Run must address. */
  prFeedback?: ActionableFeedback[];
}

/**
 * Create a Run for one phase, ready for a worker to claim.
 *
 * Phase Runs of one attempt share its number: a review that runs beside an
 * implement is the same attempt at the work item, not a new one. The attempt
 * only increments when a human retries.
 */
export async function createPhaseRun(
  organizationId: string,
  input: PhaseRunInput,
): Promise<string> {
  const result = await withOrg(organizationId, async (scope) => {
    const workItems = (await scope.sql`
      SELECT project_id FROM work_items WHERE id = ${input.workItemId}`) as Array<{
      project_id: string;
    }>;
    const projectId = workItems[0]?.project_id;
    if (!projectId) throw new Error(`work item ${input.workItemId} not found`);

    const attemptRows = (await scope.sql`
      SELECT COALESCE(MAX(attempt), 1) AS attempt FROM runs
      WHERE work_item_id = ${input.workItemId}`) as Array<{ attempt: number }>;
    const attempt = Number(attemptRows[0]?.attempt ?? 1);

    const runId = newId("run");
    await scope.sql`
      INSERT INTO runs (
        id, organization_id, project_id, work_item_id, attempt, status,
        phase, role, parent_run_id, base_ref, category, pr_feedback)
      VALUES (
        ${runId}, ${organizationId}, ${projectId}, ${input.workItemId}, ${attempt}, 'pending',
        ${input.phase}::run_phase, ${ROLE_FOR_PHASE[input.phase]!}::agent_role,
        ${input.parentRunId}, ${input.baseRef}, ${input.category ?? null},
        ${input.prFeedback ?? []}::jsonb)`;

    const event = await appendInScope(scope, {
      eventType: EventTypes.RunCreated,
      organizationId,
      projectId,
      workItemId: input.workItemId,
      runId,
      actor: { type: "system", id: "workflow" },
      source: "control-plane",
      correlationId: input.workItemId,
      payload: {
        attempt,
        phase: input.phase,
        role: ROLE_FOR_PHASE[input.phase],
        publishes: PHASE_PUBLISHES[input.phase],
        baseRef: input.baseRef,
        ...(input.category ? { category: input.category } : {}),
        ...(input.findingIds?.length ? { findingIds: input.findingIds } : {}),
        ...(input.prFeedback?.length ? { prFeedbackCount: input.prFeedback.length } : {}),
      },
    });

    return { runId, event };
  });

  eventBus.publish(result.event);
  return result.runId;
}

export interface PhaseOutcome {
  succeeded: boolean;
  error: string | null;
  headSha: string | null;
  branch: string | null;
  /** Paths the Run touched, which is what selects the conditional reviewers. */
  changedPaths: string[];
}

/**
 * What a finished phase Run produced.
 *
 * Read from the ledger rather than the Run row because the detail lives in
 * events: `git.commit_created` carries the diffstat, and the diffstat is the
 * only record of which paths changed.
 */
export async function phaseOutcome(organizationId: string, runId: string): Promise<PhaseOutcome> {
  return withOrg(organizationId, async (scope) => {
    const runs = (await scope.sql`
      SELECT status, error, head_sha, branch FROM runs WHERE id = ${runId}`) as Array<{
      status: string;
      error: string | null;
      head_sha: string | null;
      branch: string | null;
    }>;
    const run = runs[0];
    if (!run) return { succeeded: false, error: "run not found", headSha: null, branch: null, changedPaths: [] };

    const commits = (await scope.sql`
      SELECT payload FROM events
      WHERE run_id = ${runId} AND event_type = ${EventTypes.GitCommitCreated}
      ORDER BY cursor DESC LIMIT 1`) as Array<{ payload: Record<string, unknown> }>;

    const payload = commits[0]?.payload ?? {};
    const pushes = (await scope.sql`
      SELECT payload FROM events
      WHERE run_id = ${runId} AND event_type = ${EventTypes.GitPushCompleted}
      ORDER BY cursor DESC LIMIT 1`) as Array<{ payload: Record<string, unknown> }>;

    return {
      succeeded: run.status === "completed",
      error: run.error,
      headSha: (payload.headSha as string) ?? run.head_sha,
      branch: (pushes[0]?.payload.branch as string) ?? run.branch,
      changedPaths: pathsFromDiffstat(String(payload.diffstat ?? "")),
    };
  });
}

/**
 * Filenames out of `git diff --stat` output.
 *
 * Each line is `path/to/file | 12 +++---`, and the summary line has no pipe.
 * A rename reads `old => new`; the new path is what a reviewer cares about.
 */
export function pathsFromDiffstat(diffstat: string): string[] {
  const paths: string[] = [];
  for (const line of diffstat.split("\n")) {
    const pipe = line.indexOf("|");
    if (pipe === -1) continue;
    const path = line.slice(0, pipe).trim();
    if (!path) continue;
    const renamed = path.split("=>");
    paths.push((renamed[renamed.length - 1] ?? path).trim().replace(/[{}]/g, ""));
  }
  return paths;
}

export interface Finding {
  id: string;
  severity: FindingSeverity;
  status: string;
  fixAttempts: number;
  category: string;
  title: string;
}

export async function findingsFor(organizationId: string, workItemId: string): Promise<Finding[]> {
  return withOrg(organizationId, async (scope) => {
    return (await scope.sql`
      SELECT id, severity, status, fix_attempts AS "fixAttempts", category, title
      FROM review_findings
      WHERE work_item_id = ${workItemId}
      ORDER BY created_at`) as Finding[];
  });
}

/**
 * Count one fix attempt against each finding the fixer was given.
 *
 * Counted before the attempt rather than after, so a fixer that crashes still
 * consumes one — otherwise a crash loop would retry forever without ever
 * reaching the bound that is supposed to stop it.
 */
export async function markFindingAttempted(
  organizationId: string,
  findingIds: readonly string[],
): Promise<void> {
  if (findingIds.length === 0) return;
  await withOrg(organizationId, async (scope) => {
    await scope.sql`
      UPDATE review_findings
      SET fix_attempts = fix_attempts + 1, updated_at = now()
      WHERE id IN ${scope.sql(findingIds as string[])}`;
  });
}

/**
 * Retire findings whose file the latest change rewrote.
 *
 * A fixer rewriting a function can make a finding about it moot without
 * "resolving" it in any way the fixer reported. Leaving those open would
 * block the work item on a problem that no longer exists.
 *
 * The rule is deliberately narrow: a finding is superseded only when the
 * *fix Run that just ran* touched the file the finding points at, and the
 * re-review that follows did not raise it again. Anything broader would
 * retire real defects — a file being edited is not evidence the problem in
 * it was fixed, which is why the re-review, not this function, is what
 * actually clears a finding.
 */
export async function supersedeStaleFindings(
  organizationId: string,
  workItemId: string,
  headSha: string,
): Promise<number> {
  return withOrg(organizationId, async (scope) => {
    const commits = (await scope.sql`
      SELECT payload FROM events
      WHERE work_item_id = ${workItemId}
        AND event_type = ${EventTypes.GitCommitCreated}
        AND payload->>'headSha' = ${headSha}
      ORDER BY cursor DESC LIMIT 1`) as Array<{ payload: Record<string, unknown> }>;

    const touched = pathsFromDiffstat(String(commits[0]?.payload.diffstat ?? ""));
    if (touched.length === 0) return 0;

    const rows = (await scope.sql`
      UPDATE review_findings SET
        status = 'superseded',
        resolution_note = ${`file rewritten at ${headSha}`},
        updated_at = now()
      WHERE work_item_id = ${workItemId}
        AND status = 'open'
        AND file IS NOT NULL
        AND file IN ${scope.sql(touched)}
      RETURNING id`) as Array<{ id: string }>;
    return rows.length;
  });
}

export interface OpenPrInput {
  workItemId: string;
  runId: string;
  repositoryId: string;
}

/**
 * Open the pull request for a finished work item.
 *
 * The body is rendered from what the ledger already recorded — the goal, the
 * acceptance criteria, what each phase did — rather than asking a model to
 * describe work it has already finished (plan §13.2).
 */
export async function openPullRequestFor(
  organizationId: string,
  input: OpenPrInput,
): Promise<string> {
  const { title, body } = await renderPullRequest(organizationId, input.workItemId);

  const { openPullRequestForRun } = await import("../api/routes/pullRequests.ts");
  return openPullRequestForRun(organizationId, {
    runId: input.runId,
    repositoryId: input.repositoryId,
    title,
    body,
    actor: { type: "system", id: "workflow" },
  });
}

async function renderPullRequest(
  organizationId: string,
  workItemId: string,
): Promise<{ title: string; body: string }> {
  return withOrg(organizationId, async (scope) => {
    const items = (await scope.sql`
      SELECT title, goal, acceptance_criteria AS "acceptanceCriteria"
      FROM work_items WHERE id = ${workItemId}`) as Array<{
      title: string;
      goal: string;
      acceptanceCriteria: string[];
    }>;
    const item = items[0]!;

    const findings = (await scope.sql`
      SELECT category, severity, status, title FROM review_findings
      WHERE work_item_id = ${workItemId} ORDER BY created_at`) as Array<{
      category: string;
      severity: string;
      status: string;
      title: string;
    }>;

    const sections = [item.goal.trim()];

    if (item.acceptanceCriteria.length > 0) {
      sections.push(
        ["## Acceptance criteria", ...item.acceptanceCriteria.map((c) => `- ${c}`)].join("\n"),
      );
    }

    if (findings.length > 0) {
      const resolved = findings.filter((f) => f.status !== "open").length;
      sections.push(
        [
          "## Review",
          `${findings.length} finding(s) across ${new Set(findings.map((f) => f.category)).size} reviewer(s); ${resolved} addressed.`,
          "",
          ...findings.map((f) => `- \`${f.severity}\` **${f.category}** — ${f.title} _(${f.status})_`),
        ].join("\n"),
      );
    }

    sections.push("---\n\nOpened by the dude factory.");
    return { title: item.title, body: sections.filter(Boolean).join("\n\n") };
  });
}

/**
 * Move a work item to a new macro status, and say why.
 *
 * The workflow owns this: the board, the sidebar and anyone watching the
 * ledger learn where a work item stands from here, so a status that lags the
 * workflow is a board that lies. A no-op when the status is unchanged, so a
 * step that re-runs after a crash does not add a second identical event.
 */
export async function setWorkItemStatus(
  organizationId: string,
  state: { workItemId: string; projectId: string },
  status: string,
  reason: string,
): Promise<void> {
  const event = await withOrg(organizationId, async (scope) => {
    const rows = (await scope.sql`
      UPDATE work_items SET status = ${status}::work_item_status, updated_at = now()
      WHERE id = ${state.workItemId} AND status <> ${status}::work_item_status
      RETURNING id`) as Array<{ id: string }>;
    if (rows.length === 0) return null;

    return appendInScope(scope, {
      eventType: EventTypes.WorkItemStatusChanged,
      organizationId,
      projectId: state.projectId,
      workItemId: state.workItemId,
      actor: { type: "system", id: "workflow" },
      source: "control-plane",
      correlationId: state.workItemId,
      payload: { status, reason },
    });
  });
  if (event) eventBus.publish(event);
}
