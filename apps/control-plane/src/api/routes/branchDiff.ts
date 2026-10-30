/**
 * A task attempt's whole branch, one repository at a time: the latest (or
 * a named) snapshot the orchestrator captured (task_branch_snapshots,
 * migration 230), with what is newer, the published pull request's head
 * and whether there is anything to show.
 *
 * Nothing here calls GitHub. With no snapshot yet for the current attempt,
 * the read asks for a capture (want_branch_capture), which the
 * orchestrator's branch-snapshots loop takes, and answers `preparing`.
 * An earlier attempt is read-only: its recorded snapshots, never a new
 * capture.
 */

import { relocateCodeAnchor, spanRelation, type BranchRelation, type FeedbackBranchAnchor, type TaskBranchPlacement, type TaskBranchAvailability, type TaskBranchCapture, type TaskBranchDiff,
  type TaskBranchNewer, type TaskBranchPullRequest, type TaskBranchRepository, type TaskBranchSnapshot } from "@dude/domain";
import { withOrg, type OrgScope } from "../../db/client.ts";
import { badRequest, intParam, json, notFound } from "../http.ts";
import type { RequestContext, Router } from "../router.ts";
import { taskIdOf } from "./work.ts";

const SNAPSHOT_COLUMNS = `s.id, s.task_id AS "taskId", s.attempt, s.repository_id AS "repositoryId", s.repository,
  s.branch_ref AS "branchRef", s.target_ref AS "targetRef", s.target_sha AS "targetSha", s.merge_base_sha AS "mergeBaseSha",
  s.head_sha AS "headSha", s.checksum, s.source, s.captured_at AS "capturedAt", s.stats, s.truncated, s.status,
  s.reason, s.pr_head_sha AS "prHeadSha", s.previous_snapshot_id AS "previousSnapshotId", s.relation`;

/** A snapshot's lineage entry: enough to walk back from the newest. */
interface Link { id: string; previousSnapshotId: string | null; relation: BranchRelation; mergeBaseSha: string;
  headSha: string; capturedAt: Date }

function snapshotOut(row: Record<string, unknown>): TaskBranchSnapshot {
  return { ...(row as unknown as TaskBranchSnapshot), capturedAt: (row.capturedAt as Date).toISOString() };
}

/**
 * The snapshot after `from` that is newest, and the relation over the span:
 * walked back along previous_snapshot_id from the newest; `from` not on that
 * lineage (the branch was reset past it) reads as rewritten.
 */
export function newerThan(fromId: string, latestId: string, links: ReadonlyArray<Link>): TaskBranchNewer | null {
  if (fromId === latestId) return null;
  const byId = new Map(links.map((l) => [l.id, l]));
  const from = byId.get(fromId);
  const latest = byId.get(latestId);
  if (!from || !latest) return null;
  const steps: BranchRelation[] = [];
  let at: Link | undefined = latest;
  let reached = false;
  for (let guard = 0; at && guard <= links.length; guard++) {
    if (at.id === fromId) {
      reached = true;
      break;
    }
    steps.push(at.relation);
    at = at.previousSnapshotId ? byId.get(at.previousSnapshotId) : undefined;
  }
  const count = steps.length;
  if (!reached) steps.push("rewritten");
  return {
    id: latest.id, relation: spanRelation(steps, from.mergeBaseSha, latest.mergeBaseSha),
    fromMergeBaseSha: from.mergeBaseSha, fromHeadSha: from.headSha,
    mergeBaseSha: latest.mergeBaseSha, headSha: latest.headSha, capturedAt: latest.capturedAt.toISOString(),
    count,
  };
}

interface RepoRow { id: string; name: string; access: "write" | "read"; snapshots: number }

/** A (task, attempt, repository)'s capture request, as the read needs it. */
interface CaptureRequest { status: "pending" | "done" | "unavailable" | "absent"; reason: string | null; requestedAt: Date; snapshotId: string | null }

/** The attempt's repositories: the task's, and any other its snapshots name (one since removed from the task). */
async function repositoriesOf(scope: OrgScope, taskId: string, attempt: number): Promise<RepoRow[]> {
  return (await scope.sql`
    SELECT r.id, r.name, COALESCE(tr.access::text, 'write') AS access,
      (SELECT count(*)::int FROM task_branch_snapshots s WHERE s.task_id = ${taskId} AND s.attempt = ${attempt}
        AND s.repository_id = r.id) AS snapshots
    FROM repositories r LEFT JOIN task_repositories tr ON tr.repository_id = r.id AND tr.task_id = ${taskId}
    WHERE tr.task_id IS NOT NULL OR EXISTS (SELECT 1 FROM task_branch_snapshots s
      WHERE s.task_id = ${taskId} AND s.attempt = ${attempt} AND s.repository_id = r.id)
    ORDER BY (COALESCE(tr.access::text, 'write') = 'write') DESC, r.name`) as RepoRow[];
}

async function branchDiff(ctx: RequestContext): Promise<Response> {
  const ref = ctx.params.id!;
  const askedAttempt = intParam(ctx.url, "attempt", { min: 1, max: 2_147_483_647 }) ?? null;
  const askedRepo = ctx.url.searchParams.get("repository") || null;
  const askedSnapshot = ctx.url.searchParams.get("snapshot") || null;
  const includeFiles = ctx.url.searchParams.get("files") !== "0";
  const snapshotColumns = SNAPSHOT_COLUMNS + (includeFiles ? ", s.files" : "");
  if (askedRepo && askedRepo.length > 255) throw badRequest("repository is at most 255 characters");
  if (askedSnapshot && askedSnapshot.length > 100) throw badRequest("snapshot is at most 100 characters");

  const out = await withOrg(ctx.principal.organizationId, async (scope): Promise<TaskBranchDiff | null> => {
    const taskId = await taskIdOf(scope, ref);
    if (!taskId) return null;
    const [{ current }] = (await scope.sql`SELECT task_attempt(${taskId}) AS current`) as [{ current: number }];

    let named: Record<string, unknown> | null = null;
    if (askedSnapshot) {
      const [row] = (await scope.sql`SELECT ${scope.sql.unsafe(snapshotColumns)} FROM task_branch_snapshots s
        WHERE s.id = ${askedSnapshot} AND s.task_id = ${taskId}`) as Array<Record<string, unknown>>;
      if (!row) throw notFound(`snapshot ${askedSnapshot} is not one of this task's`);
      if (askedAttempt !== null && askedAttempt !== row.attempt) throw badRequest(`snapshot ${askedSnapshot} is of attempt ${row.attempt}`);
      named = row;
    }
    const attempt = (named?.attempt as number | undefined) ?? askedAttempt ?? current;
    if (attempt > current) throw notFound(`attempt ${attempt} of this task does not exist`);

    const repos = await repositoriesOf(scope, taskId, attempt);
    let repo: RepoRow | undefined;
    if (named) repo = repos.find((r) => r.id === named!.repositoryId);
    else if (askedRepo) {
      repo = repos.find((r) => r.name === askedRepo || r.id === askedRepo);
      if (!repo) throw badRequest(`${askedRepo} is not a repository of this task`);
    } else repo = repos.find((r) => r.access === "write" && r.snapshots > 0) ?? repos.find((r) => r.access === "write") ?? repos[0];

    const base = {
      taskId, attempt, currentAttempt: current, readOnly: attempt < current,
      repositories: repos.map((r): TaskBranchRepository => ({ id: r.id, name: r.name, access: r.access, snapshots: r.snapshots })),
    };
    if (!repo) {
      return { ...base, repository: null, availability: { state: "none", reason: "This task changes no repository." },
        snapshot: null, newer: null, pullRequest: null, capture: null, placements: [] };
    }

    const links = (await scope.sql`SELECT s.id, s.previous_snapshot_id AS "previousSnapshotId", s.relation,
        s.merge_base_sha AS "mergeBaseSha", s.head_sha AS "headSha", s.captured_at AS "capturedAt"
      FROM task_branch_snapshots s WHERE s.task_id = ${taskId} AND s.attempt = ${attempt} AND s.repository_id = ${repo.id}
      ORDER BY s.captured_at, s.id`) as Link[];
    const readRequest = async () => ((await scope.sql`SELECT status, reason, requested_at AS "requestedAt", snapshot_id AS "snapshotId"
      FROM task_branch_capture_requests WHERE task_id = ${taskId} AND attempt = ${attempt} AND repository_id = ${repo.id}`) as CaptureRequest[])[0];
    const request = await readRequest();
    // The branch is at the snapshot it was last read at; else the newest captured.
    const latestId = (request?.snapshotId && links.some((l) => l.id === request.snapshotId) ? request.snapshotId : links.at(-1)?.id) ?? null;

    let shown = named;
    if (!shown && latestId) {
      const [latest] = (await scope.sql`SELECT ${scope.sql.unsafe(snapshotColumns)} FROM task_branch_snapshots s
        WHERE s.id = ${latestId}`) as Array<Record<string, unknown>>;
      shown = latest ?? null;
    }

    let availability: TaskBranchAvailability;
    let requested = request;
    if (shown) availability = { state: "ok" };
    else if (request?.status === "unavailable") availability = { state: "unavailable", reason: request.reason ?? "GitHub could not be read." };
    else if (request?.status === "absent") availability = { state: "none", reason: "The branch has not been pushed yet." };
    else if (request?.status === "pending") availability = { state: "preparing" };
    else if (attempt < current) availability = { state: "none", reason: `Attempt ${attempt} recorded no snapshot of ${repo.name}.` };
    else {
      // Nothing yet for the current attempt: ask the orchestrator for one.
      await scope.sql`SELECT want_branch_capture(${scope.organizationId}, ${taskId}, ${attempt}, ${repo.id}, '')`;
      requested = await readRequest();
      availability = { state: "preparing" };
    }

    // The published head is an open (or draft) pull request's on this branch, as the capture reads it: a closed one publishes nothing.
    const branchRef = (shown?.branchRef as string | undefined) ?? null;
    const [pr] = (await scope.sql`SELECT pr.number, pr.url, pr.state::text AS state, pr.head_sha AS "headSha",
        GREATEST(pr.last_polled_at, pr.head_seen_at) AS "syncedAt"
      FROM pull_requests pr WHERE pr.task_id = ${taskId} AND pr.repository_id = ${repo.id} AND pr.state IN ('open', 'draft')
        AND (${branchRef}::text IS NULL OR pr.head_branch = ${branchRef}::text)
      ORDER BY pr.created_at DESC LIMIT 1`) as Array<Omit<TaskBranchPullRequest, "syncedAt"> & { syncedAt: Date | null }>;

    // The attempt's branch items on this repository the reader may see: submitted ones, and their own drafts.
    let placements: TaskBranchPlacement[] = [];
    if (shown && includeFiles) {
      const items = (await scope.sql`SELECT f.id, f.anchor FROM feedback_items f
        WHERE f.task_id = ${taskId} AND f.attempt = ${attempt} AND f.anchor_kind = 'code' AND f.anchor->>'scope' = 'branch'
          AND f.anchor->>'repo' = ${repo.name} AND (f.status <> 'draft' OR f.author_person_id = ${ctx.principal.personId ?? null})
        ORDER BY f.created_at, f.id LIMIT 500`) as Array<{ id: string; anchor: FeedbackBranchAnchor }>;
      const viewed = { snapshotId: shown.id as string, attempt, repository: repo.name, checksum: shown.checksum as string,
        files: shown.files as TaskBranchSnapshot["files"] };
      placements = items.map((i) => ({ itemId: i.id, snapshotId: i.anchor.snapshotId, at: relocateCodeAnchor(i.anchor, viewed) }));
    }

    const capture: TaskBranchCapture | null = requested && requested.status !== "done"
      ? { state: requested.status, reason: requested.reason, requestedAt: requested.requestedAt.toISOString() } : null;
    return {
      ...base, repository: repo.name, availability,
      snapshot: shown ? snapshotOut(shown) : null,
      newer: shown && latestId ? newerThan(shown.id as string, latestId, links) : null,
      pullRequest: pr ? { ...pr, syncedAt: pr.syncedAt ? pr.syncedAt.toISOString() : null } : null,
      capture, placements,
    };
  });
  if (!out) throw notFound(`task ${ref} not found`);
  if (!includeFiles) {
    const metadata = { ...out };
    delete (metadata as Partial<TaskBranchDiff>).placements;
    return json(metadata);
  }
  return json(out);
}

export function registerBranchDiffRoutes(router: Router): void {
  router.get("/v1/tasks/:id/branch-diff", branchDiff);
}
