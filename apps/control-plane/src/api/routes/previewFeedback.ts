/** Server feedback is stored only: it never wakes agents or creates tasks. */
import { newId, previewFeedbackSubmitSchema, previewFeedbackStatusSchema, type PreviewFeedbackItem } from "@dude/domain";
import { withOrg, type OrgScope } from "../../db/client.ts";
import { HttpError, badRequest, conflict, forbidden, intParam, json, notFound, parseBody, readCapped } from "../http.ts";
import type { RequestContext, Router } from "../router.ts";

const SELECT = `f.id, f.project_id AS "projectId", f.preview_run_id AS "previewRunId",
  f.preview_server_name AS "serverName", f.body, f.anchor, f.capture_sha AS "captureSha", f.status,
  f.created_at AS "createdAt", f.resolved_at AS "resolvedAt", f.resolved_by AS "resolvedBy",
  f.resolution_note AS "resolutionNote", f.history,
  (SELECT json_build_object('id', p.id, 'name', p.name) FROM people p WHERE p.id = f.author_person_id) AS author`;

/** Run first, matching wake/feed lifecycle transactions, then server and item. */
export async function liveFeedbackServer(scope: OrgScope, id: string, name: string): Promise<{ projectId: string; url: string }> {
  const [r] = await scope.sql`SELECT project_id AS "projectId" FROM runs WHERE id = ${id}
    AND kind = 'preview' AND preview_branch IS NOT NULL
    AND status IN ('pending', 'scheduled', 'starting', 'running', 'paused') FOR SHARE` as Array<{ projectId: string }>;
  if (!r) throw notFound();
  const [s] = await scope.sql`SELECT url FROM preview_servers WHERE run_id = ${id} AND name = ${name}
    AND deleted_at IS NULL FOR SHARE` as Array<{ url: string }>;
  if (!s) throw notFound();
  return { ...r, url: s.url };
}

function person(ctx: RequestContext): string {
  if (!ctx.principal.personId) throw forbidden("sign in to submit feedback");
  return ctx.principal.personId;
}

async function capped(ctx: RequestContext): Promise<Request> {
  const bytes = await readCapped(ctx.request, 128 * 1024, () => new HttpError(413, "feedback request is too large", "too_large"));
  return new Request(ctx.request.url, { method: "POST", body: bytes });
}

async function submit(ctx: RequestContext): Promise<Response> {
  const author = person(ctx);
  const input = await parseBody(await capped(ctx), previewFeedbackSubmitSchema);
  const id = ctx.params.id!, name = ctx.params.name!;
  const item = await withOrg(ctx.principal.organizationId, async (scope) => {
    const owner = await liveFeedbackServer(scope, id, name);
    if (!owner.url || new URL(input.anchor.url).origin !== new URL(owner.url).origin) throw badRequest("feedback must name this preview server's origin");
    // Serialize retries by one author, including two tabs reconnecting at once.
    await scope.sql`SELECT pg_advisory_xact_lock(hashtextextended(${JSON.stringify([id, name, author, input.submissionKey])}, 0))`;
    const [existing] = await scope.sql`SELECT ${scope.sql.unsafe(SELECT)},
        ((f.anchor - 'attachmentId') = (${input.anchor}::jsonb - 'attachmentId')
          AND f.body = ${input.body} AND f.capture_sha IS NOT DISTINCT FROM ${input.captureSha ?? null}) AS "sameContent"
      FROM preview_feedback f
      WHERE f.preview_run_id = ${id} AND f.preview_server_name = ${name}
      AND f.author_person_id = ${author} AND f.submission_key = ${input.submissionKey}` as Array<PreviewFeedbackItem & { sameContent: boolean }>;
    if (existing) {
      if (!existing.sameContent) throw conflict("submission key already used for different feedback");
      if (existing.anchor.attachmentId !== input.anchor.attachmentId) {
        const [same] = await scope.sql`SELECT
          (anchor->>'attachmentId' IS NOT DISTINCT FROM ${input.anchor.attachmentId ?? null}
            OR EXISTS (SELECT 1 FROM attachments a JOIN attachments b ON a.sha256 = b.sha256
              WHERE a.id = anchor->>'attachmentId' AND b.id = ${input.anchor.attachmentId ?? null}
                AND b.preview_run_id = ${id} AND b.preview_server_name = ${name} AND b.uploaded_by = ${author})) AS same
          FROM preview_feedback WHERE id = ${existing.id}`;
        if (!same.same) throw conflict("submission key already used for different feedback");
      }
      const { sameContent: _same, ...item } = existing;
      return item;
    }
    if (input.anchor.attachmentId) {
      const [a] = await scope.sql`SELECT id FROM attachments WHERE id = ${input.anchor.attachmentId}
        AND preview_run_id = ${id} AND preview_server_name = ${name} AND uploaded_by = ${author} FOR UPDATE`;
      if (!a) throw badRequest("screenshot is not your upload to this preview server");
    }
    const [row] = await scope.sql`WITH f AS (INSERT INTO preview_feedback
      (id, organization_id, project_id, preview_run_id, preview_server_name, author_person_id, submission_key, body, anchor, capture_sha)
      VALUES (${newId("feedbackItem")}, ${scope.organizationId}, ${owner.projectId}, ${id}, ${name}, ${author},
        ${input.submissionKey}, ${input.body}, ${input.anchor}::jsonb, ${input.captureSha ?? null}) RETURNING *)
      SELECT ${scope.sql.unsafe(SELECT)} FROM f`;
    return row;
  });
  return json(item, 201, { "cache-control": "no-store" });
}

async function list(ctx: RequestContext): Promise<Response> {
  const status = ctx.url.searchParams.get("status") ?? "open";
  if (!["open", "resolved", "all"].includes(status)) throw badRequest("status is open, resolved or all");
  const limit = intParam(ctx.url, "limit", { min: 1, max: 200 }) ?? 50;
  const after = ctx.url.searchParams.get("after") ?? "";
  if (after.length > 100 || (after && !/^fbi_[a-zA-Z0-9_-]+$/.test(after))) throw badRequest("invalid cursor");
  const server = ctx.params.name ?? ctx.url.searchParams.get("server");
  const out = await withOrg(ctx.principal.organizationId, async (scope) => {
    const [r] = await scope.sql`SELECT id FROM runs WHERE id = ${ctx.params.id!} AND preview_branch IS NOT NULL`;
    if (!r) throw notFound();
    const rows = await scope.sql`SELECT ${scope.sql.unsafe(SELECT)} FROM preview_feedback f
      WHERE f.preview_run_id = ${r.id} AND (${server ?? null}::text IS NULL OR f.preview_server_name = ${server ?? null})
      AND (${status} = 'all' OR f.status = ${status}) AND f.id > ${after} ORDER BY f.id LIMIT ${limit + 1}` as PreviewFeedbackItem[];
    return { feedback: rows.slice(0, limit), nextCursor: rows.length > limit ? rows[limit - 1]!.id : null };
  });
  return json(out, 200, { "cache-control": "no-store" });
}

async function show(ctx: RequestContext): Promise<Response> {
  const row = await withOrg(ctx.principal.organizationId, async (scope) => {
    const [f] = await scope.sql`SELECT ${scope.sql.unsafe(SELECT)} FROM preview_feedback f WHERE f.id = ${ctx.params.id!}`;
    if (!f) throw notFound();
    return f;
  });
  return json(row, 200, { "cache-control": "no-store" });
}

function change(status: "open" | "resolved") {
  return async (ctx: RequestContext): Promise<Response> => {
    const by = person(ctx);
    const input = await parseBody(await capped(ctx), previewFeedbackStatusSchema);
    const row = await withOrg(ctx.principal.organizationId, async (scope) => {
      const [owner] = await scope.sql`SELECT preview_run_id, preview_server_name FROM preview_feedback WHERE id = ${ctx.params.id!}`;
      if (!owner) throw notFound();
      await liveFeedbackServer(scope, owner.preview_run_id, owner.preview_server_name);
      const [f] = await scope.sql`SELECT id, status FROM preview_feedback WHERE id = ${ctx.params.id!} FOR UPDATE`;
      if (!f) throw notFound();
      if (f.status !== status) await scope.sql`UPDATE preview_feedback SET status = ${status},
        resolved_at = CASE WHEN ${status} = 'resolved' THEN now() END,
        resolved_by = CASE WHEN ${status} = 'resolved' THEN ${by} END,
        resolution_note = CASE WHEN ${status} = 'resolved' THEN ${input.note ?? ""} ELSE resolution_note END,
        history = history || jsonb_build_array(jsonb_build_object('status', ${status}::text, 'personId', ${by}::text,
          'note', ${input.note ?? ""}::text, 'at', now())) WHERE id = ${f.id}`;
      return (await scope.sql`SELECT ${scope.sql.unsafe(SELECT)} FROM preview_feedback f WHERE f.id = ${f.id}`)[0];
    });
    return json(row, 200, { "cache-control": "no-store" });
  };
}

export function registerPreviewFeedbackRoutes(router: Router): void {
  router.get("/v1/branch-previews/:id/feedback", list);
  router.get("/v1/branch-previews/:id/servers/:name/feedback", list);
  router.post("/v1/branch-previews/:id/servers/:name/feedback", submit);
  router.get("/v1/preview-feedback/:id", show);
  router.post("/v1/preview-feedback/:id/resolve", change("resolved"));
  router.post("/v1/preview-feedback/:id/reopen", change("open"));
}
