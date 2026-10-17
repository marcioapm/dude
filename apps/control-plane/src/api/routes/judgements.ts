/**
 * Judgements: which tier answers the organisation's judgements, the calls
 * it has made, and a test question.
 *
 * The setting is organizations.judgement_tier_id (migration 141): everyone
 * in the organisation reads it, its admins change it; NULL is Off. Calls are
 * model_calls rows the orchestrator wrote (purpose 'judgement'), read here
 * under the organisation's row-level security. The test question is asked
 * by the orchestrator, which holds the proxy's key, and recorded nowhere.
 */

import {
  EventTypes,
  JUDGEMENT_PAGE_MAX,
  JUDGEMENT_WINDOW_DAYS,
  judgementSettingsInputSchema,
  judgementTestInputSchema,
  type JudgementCall,
  type JudgementCallDetail,
  type JudgementCallsResponse,
  type JudgementSettings,
  type JudgementTestResult,
} from "@dude/domain";
import { config } from "../../config.ts";
import { withOrg, type OrgScope } from "../../db/client.ts";
import { appendInScope } from "../../events/ledger.ts";
import { orchestrator } from "../../orchestrator/client.ts";
import { isOrgAdmin, requireOrgAdmin } from "../access.ts";
import { auditActor } from "../auth.ts";
import { badRequest, intParam, json, notFound, parseBody } from "../http.ts";
import type { RequestContext, Router } from "../router.ts";

async function settings(ctx: RequestContext): Promise<JudgementSettings> {
  const canEdit = await isOrgAdmin(ctx);
  const [row] = (await withOrg(ctx.principal.organizationId, (scope) =>
    scope.sql`SELECT judgement_tier_id AS "tierId" FROM organizations WHERE id = ${scope.organizationId}`)) as Array<{ tierId: string | null }>;
  return { tierId: row?.tierId ?? null, canEdit };
}

/**
 * Set the judgement tier, or Off. The tier is held FOR SHARE to the end of
 * the transaction, as checkTiers holds a role's: a removal either waits and
 * then moves the setting with its roles, or has gone and this is refused.
 */
async function putSettings(ctx: RequestContext): Promise<Response> {
  await requireOrgAdmin(ctx);
  const { tierId } = await parseBody(ctx.request, judgementSettingsInputSchema);
  await withOrg(ctx.principal.organizationId, async (scope) => {
    if (tierId !== null) {
      const found = await scope.sql`SELECT id FROM model_tiers WHERE id = ${tierId} FOR SHARE`;
      if (found.length === 0) throw badRequest(`there is no model tier ${tierId}`);
    }
    await scope.sql`UPDATE organizations SET judgement_tier_id = ${tierId}, updated_at = now() WHERE id = ${scope.organizationId}`;
    const actor = auditActor(ctx.principal);
    await appendInScope(scope, {
      eventType: EventTypes.SettingsUpdated,
      organizationId: ctx.principal.organizationId,
      projectId: null,
      actor: { type: actor.kind, id: actor.id },
      source: "control-plane",
      payload: { scope: "organization", changed: { judgementTier: tierId } },
    });
  });
  return json(await settings(ctx));
}

/** A call's columns as the API names them; question is stored apart so a list never reads request. */
const CALL_COLUMNS = `
  c.id, c.created_at AS "createdAt", c.asker, c.about, c.question,
  c.result->>'decision' AS decision, (c.result->>'confidence')::float8 AS confidence, c.result->>'reasoning' AS reasoning,
  c.status, c.error, c.tier_name AS "tierName", c.model, c.latency_ms AS "latencyMs",
  c.input_tokens AS "inputTokens", c.output_tokens AS "outputTokens"`;

async function listCalls(scope: OrgScope, url: URL): Promise<JudgementCallsResponse> {
  const limit = intParam(url, "limit", { min: 1, max: JUDGEMENT_PAGE_MAX }) ?? 50;
  const asker = url.searchParams.get("asker") || null;
  const before = url.searchParams.get("before") || null;
  const since = new Date(Date.now() - JUDGEMENT_WINDOW_DAYS * 24 * 3600_000);
  // One more than asked, to know whether there is a next page.
  const rows = (await scope.sql`
    SELECT ${scope.sql.unsafe(CALL_COLUMNS)} FROM model_calls c
    WHERE c.purpose = 'judgement' AND c.created_at >= ${since}
      AND (${asker}::text IS NULL OR c.asker = ${asker})
      AND (${before}::text IS NULL OR (c.created_at, c.id) < (SELECT created_at, id FROM model_calls WHERE id = ${before}))
    ORDER BY c.created_at DESC, c.id DESC
    LIMIT ${limit + 1}`) as JudgementCall[];
  const page = rows.slice(0, limit);
  const next = rows.length > limit ? page.at(-1)!.id : null;
  // Neither the count nor the askers depend on asker or before; a later page keeps the first page's.
  if (before) return { calls: page, summary: null, askers: null, next };
  const byAsker = (await scope.sql`
    SELECT asker, count(*)::int AS calls FROM model_calls
    WHERE purpose = 'judgement' AND created_at >= ${since}
    GROUP BY asker ORDER BY asker`) as Array<{ asker: string; calls: number }>;
  return {
    calls: page,
    summary: { calls: byAsker.reduce((n, r) => n + r.calls, 0), since: since.toISOString() },
    askers: byAsker.map((r) => r.asker),
    next,
  };
}

async function getCall(scope: OrgScope, id: string): Promise<JudgementCallDetail> {
  const [row] = (await scope.sql`
    SELECT ${scope.sql.unsafe(CALL_COLUMNS)}, c.request, c.response_text AS "responseText"
    FROM model_calls c WHERE c.id = ${id} AND c.purpose = 'judgement'`) as JudgementCallDetail[];
  if (!row) throw notFound(`no judgement ${id}`);
  return row;
}

/** The test question, asked of the tier given; an admin's, as a test message on the Models page is. */
async function testJudgement(ctx: RequestContext): Promise<Response> {
  await requireOrgAdmin(ctx);
  const input = await parseBody(ctx.request, judgementTestInputSchema);
  const res = await orchestrator(ctx.principal.organizationId, "POST", "/internal/judgements/test",
    JSON.stringify(input), undefined, config().orchestratorTimeouts.testMessageMs);
  if (!res.ok) return res;
  return json((await res.json()) as JudgementTestResult);
}

export function registerJudgementRoutes(router: Router): void {
  router.get("/v1/judgements/settings", async (ctx) => json(await settings(ctx)));
  router.put("/v1/judgements/settings", putSettings);
  router.post("/v1/judgements/test", testJudgement);
  router.get("/v1/judgements", async (ctx) => json(await withOrg(ctx.principal.organizationId, (scope) => listCalls(scope, ctx.url))));
  // After /v1/judgements/settings, which it would read as an id.
  router.get("/v1/judgements/:id", async (ctx) => json(await withOrg(ctx.principal.organizationId, (scope) => getCall(scope, ctx.params.id!))));
}
