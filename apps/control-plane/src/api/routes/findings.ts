/**
 * Review findings: what a reviewer reports, and what the loop acts on.
 *
 * A finding is a row rather than a passage of prose, because the delivery
 * workflow has to decide things about it — does it block, has the fixer
 * already failed at it twice — and a paragraph cannot be queried. The
 * reviewer describes what it found; policy decides what that means
 * (plan §11.2).
 *
 * Recorded by the orchestrator from what a reviewer said; here a person
 * lists them, and may accept or reopen one.
 */

import { z } from "zod";
import { EventTypes } from "@dude/domain";
import { withOrg } from "../../db/client.ts";
import { appendInScope } from "../../events/ledger.ts";
import { json, notFound, parseBody } from "../http.ts";
import type { RequestContext, Router } from "../router.ts";

const FINDING_SELECT = `
  id, organization_id AS "organizationId", work_item_id AS "workItemId",
  run_id AS "runId", category, severity, status, repo, file, line,
  title, description, suggested_fix AS "suggestedFix",
  resolved_by_run_id AS "resolvedByRunId", resolution_note AS "resolutionNote",
  fix_attempts AS "fixAttempts",
  created_at AS "createdAt", updated_at AS "updatedAt"`;


async function listFindings(ctx: RequestContext): Promise<Response> {
  const workItemId = ctx.url.searchParams.get("workItemId");
  const runId = ctx.url.searchParams.get("runId");
  const status = ctx.url.searchParams.get("status");

  const findings = await withOrg(ctx.principal.organizationId, async (scope) => {
    return (await scope.sql`
      SELECT ${scope.sql.unsafe(FINDING_SELECT)} FROM review_findings
      WHERE (${workItemId}::text IS NULL OR work_item_id = ${workItemId})
        AND (${runId}::text IS NULL OR run_id = ${runId})
        AND (${status}::text IS NULL OR status::text = ${status})
      ORDER BY
        CASE severity
          WHEN 'blocking' THEN 1 WHEN 'high' THEN 2 WHEN 'medium' THEN 3
          WHEN 'low' THEN 4 ELSE 5
        END,
        created_at
      LIMIT 500`) as Array<Record<string, unknown>>;
  });

  return json({ findings });
}

const resolveInput = z.object({
  status: z.enum(["resolved", "accepted", "open"]),
  note: z.string().max(2000).default(""),
});

/**
 * Change a finding's status.
 *
 * `accepted` is the interesting one: a human deciding to ship a known
 * problem. Recorded as a decision with a note rather than by deleting the
 * finding, so the next person to read the code can see it was considered.
 */
async function resolveFinding(ctx: RequestContext): Promise<Response> {
  const id = ctx.params.id!;
  const input = await parseBody(ctx.request, resolveInput);
  const { organizationId } = ctx.principal;

  const result = await withOrg(organizationId, async (scope) => {
    const rows = (await scope.sql`
      UPDATE review_findings SET
        status = ${input.status}::finding_status,
        resolution_note = ${input.note ?? ""},
        updated_at = now()
      WHERE id = ${id}
      RETURNING ${scope.sql.unsafe(FINDING_SELECT)}`) as Array<Record<string, unknown>>;
    const finding = rows[0];
    if (!finding) return { missing: true as const };

    const event = await appendInScope(scope, {
      eventType: EventTypes.FindingResolved,
      organizationId,
      projectId: null,
      workItemId: finding.workItemId as string,
      runId: (finding.runId as string | null) ?? null,
      actor: { type: "human", id: ctx.principal.apiKeyId },
      source: "control-plane",
      correlationId: finding.workItemId as string,
      payload: { status: input.status, note: input.note, title: finding.title },
    });

    return { finding, event };
  });

  if ("missing" in result) throw notFound(`finding ${id} not found`);
  return json(result.finding);
}


export function registerFindingRoutes(router: Router): void {
  router.get("/v1/findings", listFindings);
  router.post("/v1/findings/:id/resolve", resolveFinding);
}
