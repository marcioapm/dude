/**
 * Keep a pull request's recorded state in step with the forge, and tell the
 * delivery workflow when something happened that it has to act on.
 *
 * Polled rather than pushed, for now: webhooks need a public URL, and a
 * factory that cannot see its own PR because it runs behind a laptop's NAT is
 * worse than one that asks every so often. The webhook path, when it lands,
 * calls the same `syncPullRequest` — only the trigger changes.
 *
 * Every poll records what changed as ledger events, but the workflow is only
 * signalled for what `classify` says is worth waking an agent for (plan
 * §13.3). An approval, a green check or a bot comment updates the record and
 * wakes nobody.
 */

import { EventTypes } from "@dude/domain";
import { withOrg, withSystemScope } from "../db/client.ts";
import { appendInScope } from "../events/ledger.ts";
import { eventBus } from "../events/bus.ts";
import { PR_SELECT } from "../api/routes/pullRequests.ts";
import { Signals } from "../workflow/delivery.workflow.ts";
import type { PostgresWorkflowRuntime } from "../workflow/runtime.ts";
import type { SweepResult } from "../workflow/sweepers.ts";
import { classify } from "./classify.ts";
import { forgeFor, slugFromUrl } from "./github.ts";

/** How often one PR is asked about. The poller itself ticks faster. */
const POLL_EVERY_SECONDS = Number(process.env.DUDE_PR_POLL_SECONDS ?? 30);
const BATCH = 20;

interface LoadedPr {
  id: string;
  project_id: string;
  work_item_id: string;
  run_id: string | null;
  number: number;
  state: string;
  checks: string;
  review: string;
  feedback_cursor: string | null;
  repo_url: string;
  repo_name: string;
}

/**
 * Read one PR from the forge, record what changed, and signal the workflow
 * if a change is actionable.
 *
 * Returns the updated row, or null if the PR does not exist for this
 * organization.
 */
export async function syncPullRequest(
  organizationId: string,
  pullRequestId: string,
  workflow?: PostgresWorkflowRuntime,
): Promise<Record<string, unknown> | null> {
  const loaded = await withOrg(organizationId, async (scope) => {
    const rows = (await scope.sql`
      SELECT pr.id, pr.project_id, pr.work_item_id, pr.run_id, pr.number,
             pr.state, pr.checks, pr.review, pr.feedback_cursor,
             r.url AS repo_url, r.name AS repo_name
      FROM pull_requests pr
      JOIN repositories r ON r.id = pr.repository_id
      WHERE pr.id = ${pullRequestId}`) as LoadedPr[];
    return rows[0] ?? null;
  });
  if (!loaded) return null;

  const slug = slugFromUrl(loaded.repo_url);
  const forge = await forgeFor(organizationId);
  if (!slug || !forge) {
    // Nothing to ask. Record the attempt so the poller moves on rather than
    // retrying this PR every tick.
    await markPolled(organizationId, pullRequestId);
    return null;
  }

  const status = await forge.getPullRequest(slug, loaded.number);
  const cursor = loaded.feedback_cursor
    ? new Date(loaded.feedback_cursor).toISOString().replace(/\.\d{3}Z$/, "Z")
    : null;
  const listed = await forge.listFeedback(slug, loaded.number, cursor);

  /*
   * The cursor narrows the query; the ledger decides what is new. Feedback
   * at the cursor's own second comes back every poll (the listing is
   * inclusive, because timestamps are whole seconds), so anything already
   * recorded as a `pull_request.commented` event is dropped by id.
   */
  const seen = await withOrg(organizationId, async (scope) => {
    if (listed.length === 0) return new Set<string>();
    const rows = (await scope.sql`
      SELECT payload->>'feedbackId' AS id FROM events
      WHERE work_item_id = ${loaded.work_item_id}
        AND event_type = ${EventTypes.PullRequestCommented}
        AND payload->>'feedbackId' IN ${scope.sql(listed.map((f) => f.id))}`) as Array<{ id: string }>;
    return new Set(rows.map((r) => r.id));
  });
  const feedback = listed.filter((f) => !seen.has(f.id));
  const newCursor = listed.length > 0 ? listed[listed.length - 1]!.createdAt : cursor;

  const result = await withOrg(organizationId, async (scope) => {
    const rows = (await scope.sql`
      UPDATE pull_requests SET
        state           = ${status.state}::pull_request_state,
        checks          = ${status.checks}::check_state,
        review          = ${status.review}::review_state,
        head_sha        = ${status.headSha},
        last_polled_at  = now(),
        feedback_cursor = ${newCursor},
        updated_at      = now(),
        merged_at = CASE WHEN ${status.state} = 'merged' THEN COALESCE(merged_at, now()) ELSE merged_at END,
        closed_at = CASE WHEN ${status.state} = 'closed' THEN COALESCE(closed_at, now()) ELSE closed_at END
      WHERE id = ${pullRequestId}
      RETURNING ${scope.sql.unsafe(PR_SELECT)}`) as Array<Record<string, unknown>>;

    /*
     * One event per thing that actually changed, rather than a "refreshed"
     * event per poll: the ledger records what happened, and "nothing
     * happened" is not an occurrence.
     */
    const changes: Array<{ type: string; payload: Record<string, unknown> }> = [];
    if (status.checks !== loaded.checks) {
      changes.push({
        type: EventTypes.PullRequestChecksChanged,
        payload: { from: loaded.checks, to: status.checks },
      });
    }
    if (status.review !== loaded.review) {
      changes.push({
        type: EventTypes.PullRequestReviewed,
        payload: { from: loaded.review, to: status.review },
      });
    }
    if (status.state !== loaded.state) {
      changes.push({
        type:
          status.state === "merged"
            ? EventTypes.PullRequestMerged
            : status.state === "closed"
              ? EventTypes.PullRequestClosed
              : EventTypes.PullRequestUpdated,
        payload: { from: loaded.state, to: status.state, url: status.url },
      });
    }
    for (const f of feedback) {
      changes.push({
        type: EventTypes.PullRequestCommented,
        payload: {
          feedbackId: f.id,
          author: f.author,
          body: f.body,
          path: f.path ?? null,
          kind: f.kind,
        },
      });
    }

    const events = [];
    for (const change of changes) {
      events.push(
        await appendInScope(scope, {
          eventType: change.type,
          organizationId,
          projectId: loaded.project_id,
          workItemId: loaded.work_item_id,
          runId: loaded.run_id,
          actor: { type: "system", id: "forge" },
          source: "control-plane",
          correlationId: loaded.work_item_id,
          payload: { ...change.payload, number: loaded.number, repo: loaded.repo_name },
        }),
      );
    }

    const workflows = (await scope.sql`
      SELECT id FROM workflow_runs
      WHERE work_item_id = ${loaded.work_item_id} AND status IN ('running', 'waiting')
      LIMIT 1`) as Array<{ id: string }>;

    return { pullRequest: rows[0]!, events, workflowRunId: workflows[0]?.id ?? null };
  });

  for (const event of result.events) eventBus.publish(event);

  const signal = classify(loaded, status, feedback, factoryLogins());
  if (signal && workflow && result.workflowRunId) {
    await workflow.signal(
      organizationId,
      result.workflowRunId,
      Signals.PrFeedback,
      // The classifier's own result, unchanged: the workflow switches on
      // `kind` rather than re-deriving what this function already decided.
      { ...signal },
      // One signal per distinct change, however many times it is polled.
      `pr:${pullRequestId}:${signal.kind}:${feedback.map((f) => f.id).join(",")}:${status.checks}:${status.state}`,
    );
  }

  return result.pullRequest;
}

/**
 * Logins whose comments are the factory's own.
 *
 * A PAT comments as the person who made it, so their own comments cannot be
 * filtered out that way without also filtering out their review. Only
 * explicitly configured bot accounts are skipped.
 */
function factoryLogins(): string[] {
  return (process.env.DUDE_FACTORY_LOGINS ?? "")
    .split(",")
    .map((s) => s.trim())
    .filter(Boolean);
}

/**
 * Record that a PR was looked at, without touching its feedback cursor.
 *
 * The cursor only ever moves forward with feedback actually handed on; a
 * failed or skipped poll resetting it would resend every old comment as new.
 */
async function markPolled(organizationId: string, id: string) {
  await withOrg(organizationId, async (scope) => {
    await scope.sql`UPDATE pull_requests SET last_polled_at = now() WHERE id = ${id}`;
  });
}

/**
 * Sync every open PR that is due a look.
 *
 * Cross-tenant, because a sweeper serves every organization; each PR is then
 * synced inside its own organization's scope.
 */
export async function pollPullRequests(workflow: PostgresWorkflowRuntime): Promise<SweepResult> {
  const due = await withSystemScope<Array<{ id: string; organization_id: string }>>(
    "pr-poller",
    async ({ sql }) =>
      (await sql`
        SELECT id, organization_id FROM pull_requests
        WHERE state IN ('draft', 'open')
          AND (last_polled_at IS NULL
               OR last_polled_at < now() - make_interval(secs => ${POLL_EVERY_SECONDS}))
        ORDER BY last_polled_at NULLS FIRST
        LIMIT ${BATCH}`) as Array<{ id: string; organization_id: string }>,
  );

  /*
   * Concurrently, not one after another. PRs belong to different
   * organizations on different forges, and one slow forge must not delay
   * every other organization's feedback by its timeout. The batch bounds how
   * many requests are in flight.
   */
  const results = await Promise.allSettled(
    due.map(async (pr) => {
      try {
        await syncPullRequest(pr.organization_id, pr.id, workflow);
      } catch (err) {
        // Record the attempt so the next sweep moves on instead of hammering
        // a forge that is refusing or not answering.
        console.error("pull request sync failed", { id: pr.id, error: String(err) });
        await markPolled(pr.organization_id, pr.id).catch(() => {});
        throw err;
      }
    }),
  );
  return { handled: results.filter((r) => r.status === "fulfilled").length };
}
