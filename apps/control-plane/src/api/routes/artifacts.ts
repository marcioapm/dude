/**
 * Artifacts: files agents publish for people — notes, a design, a report,
 * a screenshot — listed with the task that asked for them.
 *
 * The orchestrator records them as lux collects them; lux keeps the bytes.
 * Here a person lists them and reads one, the bytes streamed from lux
 * through the orchestrator, which holds the lux key.
 */

import { withOrg } from "../../db/client.ts";
import { orchestratorStream } from "../../orchestrator/client.ts";
import { badRequest, json } from "../http.ts";
import type { RequestContext, Router } from "../router.ts";

async function listArtifacts(ctx: RequestContext): Promise<Response> {
  const taskId = ctx.url.searchParams.get("taskId");
  if (!taskId) throw badRequest("taskId is required");
  const artifacts = await withOrg(ctx.principal.organizationId, async (scope) => {
    return (await scope.sql`
      SELECT a.id, r.task_id AS "taskId", a.run_id AS "runId", a.name,
        a.content_type AS "contentType", a.size_bytes::float8 AS "sizeBytes", a.sha256, a.epoch,
        a.created_at AS "createdAt", r.phase::text AS phase, r.role::text AS role
      FROM artifacts a JOIN runs r ON r.id = a.run_id
      WHERE r.task_id = ${taskId}
      ORDER BY a.created_at DESC, a.epoch DESC, a.id DESC
      LIMIT 500`) as Array<Record<string, unknown>>;
  });
  return json({ artifacts });
}

/** The bytes as the agent wrote them, never run as one of our pages. */
async function artifactContent(ctx: RequestContext): Promise<Response> {
  const res = await orchestratorStream(
    ctx.principal.organizationId,
    `/internal/artifacts/${encodeURIComponent(ctx.params.id!)}/content`,
  );
  const headers = new Headers();
  for (const name of ["content-type", "content-length", "x-content-sha256"]) {
    const value = res.headers.get(name);
    if (value) headers.set(name, value);
  }
  headers.set("x-content-type-options", "nosniff");
  headers.set("content-security-policy", "sandbox; default-src 'none'");
  return new Response(res.body, { status: res.status, headers });
}

export function registerArtifactRoutes(router: Router): void {
  router.get("/v1/artifacts", listArtifacts);
  router.get("/v1/artifacts/:id/content", artifactContent);
}
