/**
 * A person's feedback on a task (migration 127).
 *
 * Drafts are their author's alone: written, changed and removed here, and
 * listed to nobody else. Row-level security isolates organizations, not
 * people, so every statement that can touch a draft names its author.
 * Drafting writes no ledger event: the task's stream is everyone's.
 *
 * Submitting, replying, resolving and reopening change what agents do, so
 * the orchestrator carries them out.
 */

import {
  FEEDBACK_LIMITS, feedbackDraftSchema, feedbackReopenSchema, feedbackReplySchema, feedbackResolveSchema, feedbackSubmitSchema,
  newId, type FeedbackAnchor, type FeedbackAnchorInput, type FeedbackCodeAnchor, type FeedbackExcerptLine, type RunDiffFile,
} from "@dude/domain";
import type { ZodType } from "zod";
import { type OrgScope, withOrg } from "../../db/client.ts";
import { HttpError, badRequest, conflict, forbidden, intParam, json, noContent, notFound, readCapped } from "../http.ts";
import { orchestrator } from "../../orchestrator/client.ts";
import type { RequestContext, Router } from "../router.ts";

/** A draft's body is at most 16,000 characters; a preview anchor adds 20 KB or so. */
const MAX_BODY = 128 * 1024;

// The current attempt (task_attempt, migration 127).
const CURRENT_ATTEMPT = `task_attempt(t.id)`;

const ITEM_SELECT = `
  f.id, f.task_id AS "taskId", f.attempt, f.batch_id AS "batchId",
  (SELECT json_build_object('id', p.id, 'name', p.name) FROM people p WHERE p.id = f.author_person_id) AS author,
  f.body, f.anchor, f.status, f.delivered_run_id AS "deliveredRunId", f.resolved_commit AS "resolvedCommit",
  (f.status = 'resolved' AND f.resolved_verified_at IS NULL) AS "resolutionPending",
  (f.outdated_at IS NOT NULL) AS outdated,
  (SELECT b.routed_to FROM feedback_batches b WHERE b.id = f.batch_id) AS "routedTo",
  f.submitted_at AS "submittedAt", f.created_at AS "createdAt", f.updated_at AS "updatedAt",
  COALESCE((SELECT json_agg(json_build_object('id', r.id, 'kind', r.kind, 'body', r.body, 'commit', r.commit_sha,
      'personId', r.person_id, 'runId', r.run_id, 'createdAt', r.created_at) ORDER BY r.created_at, r.id)
    FROM feedback_replies r WHERE r.item_id = f.id), '[]'::json) AS history`;

/** A JSON body, read at most MAX_BODY bytes, against `schema`. */
async function body<T>(ctx: RequestContext, schema: ZodType<T>): Promise<T> {
  const raw = new TextDecoder().decode(await readCapped(ctx.request, MAX_BODY,
    () => new HttpError(413, `a feedback request is at most ${MAX_BODY} bytes`, "too_large")));
  let payload: unknown = {};
  if (raw.trim() !== "") {
    try {
      payload = JSON.parse(raw);
    } catch {
      throw badRequest("request body must be valid JSON");
    }
  }
  const parsed = schema.safeParse(payload);
  if (!parsed.success) {
    throw badRequest(`request body failed validation: ${parsed.error.issues.map((i) => `${i.path.join(".") || "body"}: ${i.message}`).join("; ")}`,
      parsed.error.flatten());
  }
  return parsed.data;
}

/** A person, never a key with no one behind it: drafts are someone's. */
function author(ctx: RequestContext): string {
  const person = ctx.principal.personId;
  if (!person) throw forbidden("feedback is a person's: sign in as one");
  return person;
}

interface TaskRow { id: string; status: string; attempt: number }

async function taskOf(scope: OrgScope, taskId: string, lock = false): Promise<TaskRow> {
  const rows = (lock
    ? await scope.sql`SELECT t.id, t.status::text AS status, ${scope.sql.unsafe(CURRENT_ATTEMPT)} AS attempt
        FROM tasks t WHERE t.id = ${taskId} FOR NO KEY UPDATE`
    : await scope.sql`SELECT t.id, t.status::text AS status, ${scope.sql.unsafe(CURRENT_ATTEMPT)} AS attempt
        FROM tasks t WHERE t.id = ${taskId}`) as TaskRow[];
  const task = rows[0];
  if (!task) throw notFound(`task ${taskId} not found`);
  return task;
}

function refuseDone(task: TaskRow): void {
  if (task.status === "done") throw conflict("this task is done: feedback on it goes in a follow-up task");
}

/** The lines of `file` from start to end on `side`, as one hunk holds them; null if any is not there. */
export function excerptOf(file: RunDiffFile, side: "old" | "new", start: number, end: number): FeedbackExcerptLine[] | null {
  for (const hunk of file.hunks ?? []) {
    const lines = hunk.lines.filter((l) => {
      const n = side === "old" ? l.old : l.new;
      return n !== null && n >= start && n <= end;
    });
    const numbers = new Set(lines.map((l) => (side === "old" ? l.old : l.new)));
    if (numbers.size === end - start + 1) {
      const first = hunk.lines.indexOf(lines[0]!);
      const last = hunk.lines.indexOf(lines[lines.length - 1]!);
      // The range as the diff shows it, the other side's lines between included.
      return hunk.lines.slice(first, last + 1).map((l) => ({ kind: l.kind, old: l.old, new: l.new, text: l.text }));
    }
  }
  return null;
}

/**
 * The anchor kept for `input`: a code anchor is read from the Run's stored
 * diff (a Run of this task's current attempt), a preview's attachment must
 * be this person's upload on this task.
 */
async function buildAnchor(scope: OrgScope, task: TaskRow, person: string, input: FeedbackAnchorInput):
    Promise<{ kind: FeedbackAnchor["kind"]; runId: string | null; anchor: FeedbackAnchor }> {
  if (input.kind === "general") return { kind: "general", runId: null, anchor: { kind: "general" } };
  if (input.kind === "preview") {
    if (input.attachmentId) {
      const [att] = (await scope.sql`SELECT 1 FROM attachments
        WHERE id = ${input.attachmentId} AND task_id = ${task.id} AND uploaded_by = ${person}`) as unknown[];
      if (!att) throw badRequest(`attachment ${input.attachmentId} is not one you uploaded to this task`);
    }
    return { kind: "preview", runId: null, anchor: input };
  }
  const [run] = (await scope.sql`
    SELECT r.status::text AS status, r.attempt, r.base_shas AS "baseShas", r.heads, d.base, d.files, d.checksum
    FROM runs r LEFT JOIN run_diffs d ON d.run_id = r.id
    WHERE r.id = ${input.runId} AND r.task_id = ${task.id} AND r.kind = 'agent'`) as Array<{
      status: string; attempt: number; baseShas: Record<string, string>; heads: Record<string, { sha?: string }>;
      base: string | null; files: RunDiffFile[] | null; checksum: string | null }>;
  if (!run) throw badRequest(`run ${input.runId} is not a Run of this task`);
  if (run.attempt !== task.attempt) throw conflict(`run ${input.runId} is of attempt ${run.attempt}, which is read-only`);
  if (!Array.isArray(run.files) || run.checksum === null) throw badRequest(`run ${input.runId} has no diff to comment on yet`);
  if (input.diffChecksum !== undefined && input.diffChecksum !== run.checksum) {
    throw conflict(`run ${input.runId}'s diff changed since you read it: read it again`);
  }
  const file = run.files.find((f) => f.path === input.path);
  if (!file) throw badRequest(`${input.path} is not in run ${input.runId}'s diff`);
  const excerpt = excerptOf(file, input.side, input.startLine, input.endLine);
  if (!excerpt) throw badRequest(`lines ${input.startLine}-${input.endLine} (${input.side}) are not one range of ${input.path}'s diff`);
  // With several repositories the diff's paths name theirs first (phases.parseRunDiff).
  const repos = Object.keys(run.baseShas ?? {});
  let specRepo = repos[0] ?? "";
  let path = input.path;
  if (repos.length > 1) {
    const slash = input.path.indexOf("/");
    specRepo = input.path.slice(0, slash);
    path = input.path.slice(slash + 1);
    if (slash < 0 || !repos.includes(specRepo)) throw badRequest(`${input.path} names no repository of run ${input.runId}`);
  }
  // base_shas is keyed by the name lux checked the repository out under
  // (lux_name, migration 093); the anchor, runs.heads and agents name it by its own.
  const [named] = (await scope.sql`SELECT r.name FROM repositories r JOIN task_repositories tr ON tr.repository_id = r.id
    WHERE tr.task_id = ${task.id} AND lux_name(r.name) = ${specRepo}`) as Array<{ name: string }>;
  const repo = named?.name ?? specRepo;
  // A head only for what the Run published: lines of a live Run are uncommitted work.
  const head = run.status === "completed" ? run.heads?.[repo]?.sha ?? null : null;
  const anchor: FeedbackCodeAnchor = {
    kind: "code", runId: input.runId, repo, path, side: input.side, startLine: input.startLine, endLine: input.endLine,
    baseSha: run.baseShas?.[specRepo] ?? run.base ?? "", headSha: head, diffChecksum: run.checksum, excerpt,
  };
  return { kind: "code", runId: input.runId, anchor };
}

async function list(ctx: RequestContext): Promise<Response> {
  const person = author(ctx);
  const taskId = ctx.params.id!;
  const asked = intParam(ctx.url, "attempt", { min: 1, max: 2_147_483_647 }) ?? null;
  const out = await withOrg(ctx.principal.organizationId, async (scope) => {
    const task = await taskOf(scope, taskId);
    const attempt = asked ?? task.attempt;
    const feedback = await scope.sql`
      SELECT ${scope.sql.unsafe(ITEM_SELECT)} FROM feedback_items f
      WHERE f.task_id = ${taskId} AND f.attempt = ${attempt}
        AND (f.status <> 'draft' OR f.author_person_id = ${person})
      ORDER BY f.status = 'draft', COALESCE(f.submitted_at, f.created_at), f.id
      LIMIT 500`;
    const [{ blocking }] = (await scope.sql`
      SELECT count(*)::int AS blocking FROM feedback_items
      WHERE task_id = ${taskId} AND attempt = ${attempt}
        AND (status IN ('open', 'delivered') OR (status = 'resolved' AND resolved_verified_at IS NULL))`) as [{ blocking: number }];
    return { feedback, blocking };
  });
  return json(out);
}

async function createDraft(ctx: RequestContext): Promise<Response> {
  const person = author(ctx);
  const input = await body(ctx, feedbackDraftSchema);
  const taskId = ctx.params.id!;
  const item = await withOrg(ctx.principal.organizationId, async (scope) => {
    const task = await taskOf(scope, taskId, true);
    refuseDone(task);
    const [{ drafts }] = (await scope.sql`SELECT count(*)::int AS drafts FROM feedback_items
      WHERE task_id = ${taskId} AND author_person_id = ${person} AND status = 'draft'`) as [{ drafts: number }];
    if (drafts >= FEEDBACK_LIMITS.drafts) throw conflict(`you have ${drafts} drafts on this task: submit or remove some first`);
    const built = await buildAnchor(scope, task, person, input.anchor);
    const [row] = await scope.sql`
      WITH f AS (
        INSERT INTO feedback_items (id, organization_id, task_id, attempt, author_person_id, body, anchor_kind, run_id, anchor)
        VALUES (${newId("feedbackItem")}, ${scope.organizationId}, ${taskId}, ${task.attempt}, ${person}, ${input.body},
                ${built.kind}, ${built.runId}, ${built.anchor}::jsonb)
        RETURNING *)
      SELECT ${scope.sql.unsafe(ITEM_SELECT)} FROM f`;
    return row;
  });
  return json(item, 201);
}

/** The caller's own draft, locked, on its task's current attempt. */
async function ownDraft(scope: OrgScope, id: string, person: string): Promise<TaskRow> {
  const [row] = (await scope.sql`SELECT task_id AS "taskId", attempt FROM feedback_items
    WHERE id = ${id} AND author_person_id = ${person} AND status = 'draft'`) as Array<{ taskId: string; attempt: number }>;
  // Someone else's draft, or a submitted item, is not found: its existence is not told.
  if (!row) throw notFound(`draft ${id} not found`);
  const task = await taskOf(scope, row.taskId, true);
  if (row.attempt !== task.attempt) throw conflict(`draft ${id} is on attempt ${row.attempt}, which is read-only`);
  return task;
}

async function updateDraft(ctx: RequestContext): Promise<Response> {
  const person = author(ctx);
  const input = await body(ctx, feedbackDraftSchema);
  const id = ctx.params.id!;
  const item = await withOrg(ctx.principal.organizationId, async (scope) => {
    const task = await ownDraft(scope, id, person);
    refuseDone(task);
    const built = await buildAnchor(scope, task, person, input.anchor);
    const [row] = await scope.sql`
      WITH f AS (
        UPDATE feedback_items SET body = ${input.body}, anchor_kind = ${built.kind}, run_id = ${built.runId},
          anchor = ${built.anchor}::jsonb, updated_at = now()
        WHERE id = ${id} AND author_person_id = ${person} AND status = 'draft'
        RETURNING *)
      SELECT ${scope.sql.unsafe(ITEM_SELECT)} FROM f`;
    if (!row) throw notFound(`draft ${id} not found`);
    return row;
  });
  return json(item);
}

async function deleteDraft(ctx: RequestContext): Promise<Response> {
  const person = author(ctx);
  const id = ctx.params.id!;
  await withOrg(ctx.principal.organizationId, async (scope) => {
    await ownDraft(scope, id, person);
    const rows = await scope.sql`DELETE FROM feedback_items
      WHERE id = ${id} AND author_person_id = ${person} AND status = 'draft' RETURNING id`;
    if (!rows[0]) throw notFound(`draft ${id} not found`);
  });
  return noContent();
}

/** Forwarded with the body validated here, so the orchestrator is sent only a well-formed request. */
function forward<T>(schema: ZodType<T>, path: (ctx: RequestContext) => string) {
  return async (ctx: RequestContext): Promise<Response> => {
    author(ctx);
    const input = await body(ctx, schema);
    return orchestrator(ctx.principal.organizationId, "POST", path(ctx), JSON.stringify(input), ctx.principal);
  };
}

export function registerFeedbackRoutes(router: Router): void {
  router.get("/v1/tasks/:id/feedback", list);
  router.post("/v1/tasks/:id/feedback/drafts", createDraft);
  router.patch("/v1/feedback/:id", updateDraft);
  router.delete("/v1/feedback/:id", deleteDraft);
  router.post("/v1/tasks/:id/feedback/submit", forward(feedbackSubmitSchema, (ctx) => `/internal/tasks/${ctx.params.id}/feedback/submit`));
  router.post("/v1/feedback/:id/reply", forward(feedbackReplySchema, (ctx) => `/internal/feedback/${ctx.params.id}/reply`));
  router.post("/v1/feedback/:id/resolve", forward(feedbackResolveSchema, (ctx) => `/internal/feedback/${ctx.params.id}/resolve`));
  router.post("/v1/feedback/:id/reopen", forward(feedbackReopenSchema, (ctx) => `/internal/feedback/${ctx.params.id}/reopen`));
}
