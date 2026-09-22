/**
 * Pull request routes — Bootstrap 4 of the plan's self-hosting order (§81).
 *
 * The factory's work only becomes part of a repository through a pull request,
 * so this is what turns "the agent made a commit" into something a person can
 * review and merge.
 *
 * Two audiences:
 *
 *   - the runner asks for a **push credential**, scoped to one Run, because
 *     the workspace lives on its filesystem and only it can push. It never
 *     holds a long-lived forge credential, so a node that loses its lease
 *     loses its ability to write (plan §61);
 *   - a human (and later the workflow) asks to **open a PR** and to read its
 *     state.
 *
 * The agent sees neither. It writes commits into a workspace; the runner
 * pushes them.
 */

import { z } from "zod";
import { EventTypes, newId } from "@dude/domain";
import { withOrg } from "../../db/client.ts";
import { appendInScope } from "../../events/ledger.ts";
import { eventBus } from "../../events/bus.ts";
import { badRequest, conflict, json, notFound, parseBody } from "../http.ts";
import { ForgeError, forgeFor, slugFromUrl } from "../../forge/github.ts";
import type { RequestContext, Router } from "../router.ts";

const PR_SELECT = `
  id, organization_id AS "organizationId", project_id AS "projectId",
  work_item_id AS "workItemId", run_id AS "runId", repository_id AS "repositoryId",
  number, node_id AS "nodeId", url, head_branch AS "headBranch",
  base_branch AS "baseBranch", head_sha AS "headSha", title, body,
  state, checks, review,
  created_at AS "createdAt", updated_at AS "updatedAt",
  merged_at AS "mergedAt", closed_at AS "closedAt"`;

/**
 * The branch a Run publishes to.
 *
 * Derived rather than stored so it is the same on both sides of a restart,
 * and namespaced under `dude/` so a human scanning branches can see at a
 * glance which were machine-authored.
 */
export function branchForRun(workItemId: string, attempt: number): string {
  return `dude/${workItemId}/attempt-${attempt}`;
}

interface RunRow {
  id: string;
  organization_id: string;
  project_id: string;
  work_item_id: string;
  attempt: number;
  status: string;
}

// ---------------------------------------------------------------------------
// Push credentials (runner only)
// ---------------------------------------------------------------------------

/**
 * A credential for one push, plus the branch to push to.
 *
 * Deliberately tied to a live Run: a worker whose lease expired cannot ask
 * for one, so losing a node does not leave a push credential loose on it.
 */
async function getPushCredential(ctx: RequestContext): Promise<Response> {
  const runId = ctx.params.id!;
  const { organizationId } = ctx.principal;

  const run = await withOrg(organizationId, async (scope) => {
    const rows = (await scope.sql`
      SELECT id, organization_id, project_id, work_item_id, attempt, status
      FROM runs
      WHERE id = ${runId} AND worker_id IS NOT NULL AND lease_expires_at > now()`) as RunRow[];
    return rows[0] ?? null;
  });

  if (!run) throw notFound(`run ${runId} is not leased`);

  const branch = branchForRun(run.work_item_id, run.attempt);

  /*
   * No credential is not an error. A local-path or ssh remote needs none —
   * the local provisioner and the test suite both use them — and refusing
   * here stranded every phase after the first: the implementer's commit
   * never reached the remote, so the next phase could not check it out.
   *
   * If the remote does need one, the push fails with the forge's own auth
   * error and that lands in the ledger as a failed push, which is a more
   * honest report than refusing before anything was tried.
   */
  const forge = await forgeFor(organizationId);
  const token = forge ? await forge.pushToken() : null;

  return json({
    // A git credential helper wants both halves; `x-access-token` is what
    // GitHub expects as the username when the password is a token.
    username: "x-access-token",
    token,
    branch,
  });
}

// ---------------------------------------------------------------------------
// Pull requests
// ---------------------------------------------------------------------------

const openPullRequestInput = z.object({
  repositoryId: z.string().min(1),
  title: z.string().min(1).max(200),
  body: z.string().max(60_000).default(""),
  /** Defaults to the Run's branch; override only for an unusual workflow. */
  headBranch: z.string().min(1).optional(),
  /** Defaults to the repository's own default branch. */
  baseBranch: z.string().min(1).optional(),
  draft: z.boolean().default(false),
});

export interface OpenPullRequestForRun {
  runId: string;
  repositoryId: string;
  title: string;
  body: string;
  draft?: boolean;
  headBranch?: string;
  baseBranch?: string;
  actor: { type: "human" | "system"; id: string };
}

/**
 * Open a pull request for a Run's branch, and record it.
 *
 * Callable rather than HTTP-only because the delivery workflow opens PRs
 * too, and a workflow step calling its own API over the network would turn a
 * transaction into a round trip that can fail on its own.
 *
 * Idempotent on the forge's terms as well as ours: a PR already recorded for
 * this Run is returned as-is, and one GitHub rejects as a duplicate surfaces
 * as a conflict rather than a crash — a retried Run must not be blocked by
 * its own earlier success.
 */
export async function openPullRequestForRun(
  organizationId: string,
  input: OpenPullRequestForRun,
): Promise<string> {
  const context = await withOrg(organizationId, async (scope) => {
    const runs = (await scope.sql`
      SELECT id, organization_id, project_id, work_item_id, attempt, status
      FROM runs WHERE id = ${input.runId}`) as RunRow[];
    const run = runs[0];
    if (!run) return { missing: true as const };

    const repos = (await scope.sql`
      SELECT id, name, url, default_branch AS "defaultBranch"
      FROM repositories WHERE id = ${input.repositoryId} AND project_id = ${run.project_id}`) as Array<{
      id: string;
      name: string;
      url: string;
      defaultBranch: string;
    }>;
    const repository = repos[0];
    if (!repository) return { missingRepo: true as const };

    const existing = (await scope.sql`
      SELECT id FROM pull_requests
      WHERE run_id = ${input.runId} AND repository_id = ${input.repositoryId}
      LIMIT 1`) as Array<{ id: string }>;

    return { run, repository, existingId: existing[0]?.id ?? null };
  });

  if ("missing" in context) throw notFound(`run ${input.runId} not found`);
  if ("missingRepo" in context) {
    throw notFound(`repository ${input.repositoryId} is not part of this run's project`);
  }
  if (context.existingId) return context.existingId;

  const { run, repository } = context;
  const slug = slugFromUrl(repository.url);
  if (!slug) throw badRequest(`cannot derive owner/repo from ${repository.url}`);

  const forge = await forgeFor(organizationId);
  if (!forge) throw badRequest("no git forge credential is configured for this organization");

  const headBranch = input.headBranch ?? branchForRun(run.work_item_id, run.attempt);
  const baseBranch = input.baseBranch ?? repository.defaultBranch;

  let ref;
  try {
    ref = await forge.openPullRequest({
      slug,
      title: input.title,
      body: input.body,
      headBranch,
      baseBranch,
      draft: input.draft ?? false,
    });
  } catch (err) {
    if (err instanceof ForgeError && err.isAlreadyExists) {
      throw conflict(`a pull request already exists for ${headBranch}: ${err.message}`);
    }
    throw err;
  }

  const result = await withOrg(organizationId, async (scope) => {
    const pullRequestId = newId("pullRequest");
    await scope.sql`
      INSERT INTO pull_requests (
        id, organization_id, project_id, work_item_id, run_id, repository_id,
        number, node_id, url, head_branch, base_branch, head_sha, title, body, state)
      VALUES (
        ${pullRequestId}, ${organizationId}, ${run.project_id}, ${run.work_item_id},
        ${input.runId}, ${repository.id}, ${ref.number}, ${ref.nodeId}, ${ref.url},
        ${headBranch}, ${baseBranch}, ${ref.headSha}, ${input.title}, ${input.body},
        ${ref.state}::pull_request_state)`;

    const event = await appendInScope(scope, {
      eventType: EventTypes.PullRequestOpened,
      organizationId,
      projectId: run.project_id,
      workItemId: run.work_item_id,
      runId: input.runId,
      actor: input.actor,
      source: "control-plane",
      correlationId: run.work_item_id,
      payload: {
        number: ref.number,
        url: ref.url,
        repo: repository.name,
        headBranch,
        baseBranch,
        draft: input.draft ?? false,
      },
    });

    return { pullRequestId, event };
  });

  eventBus.publish(result.event);
  return result.pullRequestId;
}

async function openPullRequest(ctx: RequestContext): Promise<Response> {
  const input = await parseBody(ctx.request, openPullRequestInput);

  const pullRequestId = await openPullRequestForRun(ctx.principal.organizationId, {
    runId: ctx.params.id!,
    repositoryId: input.repositoryId,
    title: input.title,
    body: input.body ?? "",
    draft: input.draft ?? false,
    ...(input.headBranch ? { headBranch: input.headBranch } : {}),
    ...(input.baseBranch ? { baseBranch: input.baseBranch } : {}),
    actor: { type: "human", id: ctx.principal.apiKeyId },
  });

  const pullRequest = await withOrg(ctx.principal.organizationId, async (scope) => {
    const rows = (await scope.sql`
      SELECT ${scope.sql.unsafe(PR_SELECT)} FROM pull_requests WHERE id = ${pullRequestId}`) as Array<
      Record<string, unknown>
    >;
    return rows[0]!;
  });

  return json(pullRequest, 201);
}

/**
 * Re-read a pull request from the forge and record what changed.
 *
 * Polled rather than pushed, for now: webhooks need a public URL, and a
 * factory that cannot see its own PR because it is behind a laptop's NAT is
 * worse than one that asks every so often. The webhook path replaces the
 * body of this function without changing its contract.
 */
async function refreshPullRequest(ctx: RequestContext): Promise<Response> {
  const id = ctx.params.id!;
  const { organizationId } = ctx.principal;

  const loaded = await withOrg(organizationId, async (scope) => {
    const rows = (await scope.sql`
      SELECT pr.id, pr.project_id, pr.work_item_id, pr.run_id, pr.number,
             pr.state, pr.checks, pr.review, r.url AS repo_url, r.name AS repo_name
      FROM pull_requests pr
      JOIN repositories r ON r.id = pr.repository_id
      WHERE pr.id = ${id}`) as Array<Record<string, string | number | null>>;
    return rows[0] ?? null;
  });

  if (!loaded) throw notFound(`pull request ${id} not found`);

  const slug = slugFromUrl(String(loaded.repo_url));
  if (!slug) throw badRequest(`cannot derive owner/repo from ${loaded.repo_url}`);

  const forge = await forgeFor(organizationId);
  if (!forge) throw badRequest("no git forge credential is configured for this organization");

  const status = await forge.getPullRequest(slug, Number(loaded.number));

  const result = await withOrg(organizationId, async (scope) => {
    const rows = (await scope.sql`
      UPDATE pull_requests SET
        state      = ${status.state}::pull_request_state,
        checks     = ${status.checks}::check_state,
        review     = ${status.review}::review_state,
        head_sha   = ${status.headSha},
        updated_at = now(),
        merged_at  = CASE WHEN ${status.state} = 'merged' THEN COALESCE(merged_at, now()) ELSE merged_at END,
        closed_at  = CASE WHEN ${status.state} = 'closed' THEN COALESCE(closed_at, now()) ELSE closed_at END
      WHERE id = ${id}
      RETURNING ${scope.sql.unsafe(PR_SELECT)}`) as Array<Record<string, unknown>>;

    /*
     * One event per thing that actually changed, rather than a single
     * "refreshed" event on every poll: the ledger is a record of what
     * happened, and "nothing happened" is not an occurrence.
     */
    const changes: Array<{ type: string; payload: Record<string, unknown> }> = [];
    if (status.checks !== loaded.checks) {
      changes.push({
        type: EventTypes.PullRequestChecksChanged,
        payload: { from: loaded.checks, to: status.checks, number: loaded.number },
      });
    }
    if (status.review !== loaded.review) {
      changes.push({
        type: EventTypes.PullRequestReviewed,
        payload: { from: loaded.review, to: status.review, number: loaded.number },
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
        payload: { from: loaded.state, to: status.state, number: loaded.number, url: status.url },
      });
    }

    const events = [];
    for (const change of changes) {
      events.push(
        await appendInScope(scope, {
          eventType: change.type,
          organizationId,
          projectId: String(loaded.project_id),
          workItemId: String(loaded.work_item_id),
          runId: loaded.run_id === null ? null : String(loaded.run_id),
          actor: { type: "system", id: "forge" },
          source: "control-plane",
          correlationId: String(loaded.work_item_id),
          payload: { ...change.payload, repo: loaded.repo_name },
        }),
      );
    }

    return { pullRequest: rows[0]!, events };
  });

  for (const event of result.events) eventBus.publish(event);
  return json(result.pullRequest);
}

async function listPullRequests(ctx: RequestContext): Promise<Response> {
  const workItemId = ctx.url.searchParams.get("workItemId");
  const runId = ctx.url.searchParams.get("runId");

  const pullRequests = await withOrg(ctx.principal.organizationId, async (scope) => {
    return (await scope.sql`
      SELECT ${scope.sql.unsafe(PR_SELECT)} FROM pull_requests
      WHERE (${workItemId}::text IS NULL OR work_item_id = ${workItemId})
        AND (${runId}::text IS NULL OR run_id = ${runId})
      ORDER BY created_at DESC
      LIMIT 200`) as Array<Record<string, unknown>>;
  });

  return json({ pullRequests });
}

// ---------------------------------------------------------------------------
// Credential configuration
// ---------------------------------------------------------------------------

const credentialInput = z.object({
  auth: z.enum(["pat", "github_app"]).default("pat"),
  secret: z.string().min(1),
  appId: z.string().nullable().default(null),
  installationId: z.string().nullable().default(null),
  apiBaseUrl: z.string().min(1).default("https://api.github.com"),
});

/**
 * Store an organization's forge credential.
 *
 * The secret is never returned by any route, including this one — the
 * response confirms what was configured, not what it was configured with.
 */
async function putCredential(ctx: RequestContext): Promise<Response> {
  const input = await parseBody(ctx.request, credentialInput);
  const { organizationId } = ctx.principal;

  if (input.auth === "github_app" && !(input.appId && input.installationId)) {
    throw badRequest("github_app authentication requires appId and installationId");
  }

  const saved = await withOrg(organizationId, async (scope) => {
    const rows = (await scope.sql`
      INSERT INTO forge_credentials (
        id, organization_id, forge, auth, secret, app_id, installation_id, api_base_url)
      VALUES (${newId("forgeCredential")}, ${organizationId}, 'github', ${input.auth}::forge_auth_kind,
              ${input.secret}, ${input.appId}, ${input.installationId}, ${input.apiBaseUrl})
      ON CONFLICT (organization_id, forge) DO UPDATE SET
        auth            = EXCLUDED.auth,
        secret          = EXCLUDED.secret,
        app_id          = EXCLUDED.app_id,
        installation_id = EXCLUDED.installation_id,
        api_base_url    = EXCLUDED.api_base_url,
        updated_at      = now()
      RETURNING id, forge, auth, app_id AS "appId", installation_id AS "installationId",
                api_base_url AS "apiBaseUrl", updated_at AS "updatedAt"`) as Array<
      Record<string, unknown>
    >;
    return rows[0]!;
  });

  return json(saved);
}

export function registerPullRequestRoutes(router: Router): void {
  router.post("/v1/forge/credential", putCredential);

  // The runner is the only thing that can push, because the workspace is on
  // its disk — and the only principal allowed to ask for a credential.
  router.get("/v1/runs/:id/push-credential", getPushCredential, { requireKind: "runner" });

  router.post("/v1/runs/:id/pull-request", openPullRequest);
  router.get("/v1/pull-requests", listPullRequests);
  router.post("/v1/pull-requests/:id/refresh", refreshPullRequest);
}
