/**
 * Human intervention routes — plan §24.
 *
 * Steer, pause, resume and abort are first-class workflow primitives, not
 * chat hacks (plan §4.5). Each records a durable domain object and a ledger
 * event, so a human redirecting an agent is as auditable as the agent's own
 * actions.
 *
 * The control plane records the request; the runner acts on it. That split
 * matters: a Run may be on a node that is briefly unreachable, and the
 * request must survive that rather than being lost in an RPC.
 */

import { z } from "zod";
import {
  ALL_RUN_STATUSES,
  EventTypes,
  TERMINAL_RUN_STATUSES,
  controlForPause,
  newId,
  pauseModeSchema,
  directiveScopeSchema,
} from "@dude/domain";
import { withOrg } from "../../db/client.ts";
import { appendInScope } from "../../events/ledger.ts";
import { eventBus } from "../../events/bus.ts";
import { conflict, json, notFound, parseBody } from "../http.ts";
import type { RequestContext, Router } from "../router.ts";

const DIRECTIVE_SELECT = `
  id, organization_id AS "organizationId", work_item_id AS "workItemId",
  run_id AS "runId", text, scope, created_by AS "createdBy",
  created_at AS "createdAt", supersedes, delivered_at AS "deliveredAt"`;

/**
 * Statuses from which a Run can still be intervened on.
 *
 * Derived rather than listed: a status added to the domain must not silently
 * become un-interveneable because someone forgot to extend a literal here.
 */
const LIVE_RUN_STATUSES: readonly string[] = ALL_RUN_STATUSES.filter(
  (status) => !TERMINAL_RUN_STATUSES.includes(status),
);

interface RunRow {
  id: string;
  organization_id: string;
  project_id: string;
  work_item_id: string;
  status: string;
  control: string;
}

/**
 * Load a Run for intervention, or explain why it cannot be.
 *
 * Returns a discriminated result rather than throwing, so the caller can
 * throw outside the transaction — a rollback triggered by an HTTP error is
 * noise in the logs.
 *
 * `accepts` decides which statuses this intervention applies to: pause and
 * abort want any live Run, resume wants only a paused one. One query, one
 * row shape, a predicate per caller.
 */
async function loadRun(
  scope: { sql: import("bun").TransactionSQL },
  runId: string,
  accepts: (status: string) => boolean,
): Promise<{ run: RunRow } | { missing: true } | { rejected: string }> {
  const rows = (await scope.sql`
    SELECT id, organization_id, project_id, work_item_id, status::text, control::text
    FROM runs WHERE id = ${runId}`) as RunRow[];

  const run = rows[0];
  if (!run) return { missing: true };
  if (!accepts(run.status)) return { rejected: run.status };
  return { run };
}

/** Accepts any Run that has not finished. */
const isLive = (status: string) => LIVE_RUN_STATUSES.includes(status);

// ---------------------------------------------------------------------------
// Steer
// ---------------------------------------------------------------------------

const steerInput = z.object({
  text: z.string().min(1).max(10_000),
  scope: directiveScopeSchema.default("run"),
  /** An earlier directive this one replaces. The earlier row is kept. */
  supersedes: z.string().min(1).nullish(),
});

/**
 * Steer a Run.
 *
 * The directive is stored undelivered; the runner picks it up and hands it to
 * the agent. Delivery timing follows plan §24: a session mid-turn is
 * interrupted, one that is waiting receives it on its next wake. Either way
 * the instruction is durable — steering a Run whose worker just died still
 * applies when the Run is retried.
 */
async function steerRun(ctx: RequestContext): Promise<Response> {
  const input = await parseBody(ctx.request, steerInput);
  const runId = ctx.params.id!;
  const { organizationId } = ctx.principal;

  const result = await withOrg(organizationId, async (scope) => {
    const loaded = await loadRun(scope, runId, isLive);
    if (!("run" in loaded)) return loaded;
    const { run } = loaded;

    const directiveId = newId("directive");
    const rows = (await scope.sql`
      INSERT INTO directives (id, organization_id, work_item_id, run_id, text, scope, supersedes)
      VALUES (${directiveId}, ${organizationId}, ${run.work_item_id}, ${runId},
              ${input.text}, ${input.scope ?? "run"}, ${input.supersedes ?? null})
      RETURNING ${scope.sql.unsafe(DIRECTIVE_SELECT)}`) as Array<Record<string, unknown>>;

    const event = await appendInScope(scope, {
      eventType: EventTypes.RunSteered,
      organizationId,
      projectId: run.project_id,
      workItemId: run.work_item_id,
      runId,
      actor: { type: "human", id: ctx.principal.apiKeyId },
      source: "control-plane",
      correlationId: run.work_item_id,
      payload: {
        directiveId,
        text: input.text,
        scope: input.scope ?? "run",
        supersedes: input.supersedes ?? null,
      },
    });

    return { directive: rows[0]!, event };
  });

  if ("missing" in result) throw notFound(`run ${runId} not found`);
  if ("rejected" in result) {
    throw conflict(`run ${runId} is ${result.rejected} and can no longer be steered`);
  }
  eventBus.publish(result.event);
  return json(result.directive, 201);
}

/** Directives issued for a Run, newest first. */
async function listDirectives(ctx: RequestContext): Promise<Response> {
  const runId = ctx.params.id!;
  const directives = await withOrg(ctx.principal.organizationId, async (scope) => {
    return (await scope.sql`
      SELECT ${scope.sql.unsafe(DIRECTIVE_SELECT)} FROM directives
      WHERE run_id = ${runId}
      ORDER BY created_at DESC`) as Array<Record<string, unknown>>;
  });
  return json({ directives });
}

// ---------------------------------------------------------------------------
// Pause / resume / abort
// ---------------------------------------------------------------------------

const pauseInput = z.object({
  mode: pauseModeSchema.default("graceful"),
  reason: z.string().max(2000).nullish(),
});

const reasonInput = z.object({
  reason: z.string().max(2000).nullish(),
});

/**
 * Request a pause.
 *
 * Records the request rather than performing it: the runner owns the agent
 * process, and the Run only becomes `paused` once the runner confirms. The
 * gap is why `control` is separate from `status` — during it, the Run is
 * genuinely still running with a pause pending.
 */
async function pauseRun(ctx: RequestContext): Promise<Response> {
  const input = await parseBody(ctx.request, pauseInput);
  const runId = ctx.params.id!;
  const { organizationId } = ctx.principal;
  const control = controlForPause(input.mode ?? "graceful");

  const result = await withOrg(organizationId, async (scope) => {
    const loaded = await loadRun(scope, runId, isLive);
    if (!("run" in loaded)) return loaded;
    const { run } = loaded;

    if (run.status === "paused") return { alreadyPaused: true as const };

    await scope.sql`
      UPDATE runs
      SET control = ${control}::run_control,
          control_requested_at = now(),
          control_reason = ${input.reason ?? null}
      WHERE id = ${runId}`;

    const event = await appendInScope(scope, {
      eventType: EventTypes.RunPaused,
      organizationId,
      projectId: run.project_id,
      workItemId: run.work_item_id,
      runId,
      actor: { type: "human", id: ctx.principal.apiKeyId },
      source: "control-plane",
      correlationId: run.work_item_id,
      payload: { mode: input.mode ?? "graceful", requested: true, reason: input.reason ?? null },
    });

    return { event };
  });

  if ("missing" in result) throw notFound(`run ${runId} not found`);
  if ("rejected" in result) {
    throw conflict(`run ${runId} is ${result.rejected} and cannot be paused`);
  }
  if ("alreadyPaused" in result) throw conflict(`run ${runId} is already paused`);

  eventBus.publish(result.event);
  return json({ ok: true, control, pending: true });
}

/**
 * Resume a paused Run.
 *
 * Returns it to `pending` so it is re-claimable, rather than assuming the
 * prior worker survived — plan §24 is explicit that resume works from the
 * durable state, not from a live process.
 */
async function resumeRun(ctx: RequestContext): Promise<Response> {
  const input = await parseBody(ctx.request, reasonInput);
  const runId = ctx.params.id!;
  const { organizationId } = ctx.principal;

  const result = await withOrg(organizationId, async (scope) => {
    const loaded = await loadRun(scope, runId, (status) => status === "paused");
    if (!("run" in loaded)) {
      return "missing" in loaded ? loaded : { notPaused: loaded.rejected };
    }
    const { run } = loaded;

    await scope.sql`
      UPDATE runs
      SET status = 'pending',
          control = 'none',
          control_requested_at = NULL,
          control_reason = NULL,
          worker_id = NULL,
          lease_expires_at = NULL
      WHERE id = ${runId}`;

    const event = await appendInScope(scope, {
      eventType: EventTypes.RunResumed,
      organizationId,
      projectId: run.project_id,
      workItemId: run.work_item_id,
      runId,
      actor: { type: "human", id: ctx.principal.apiKeyId },
      source: "control-plane",
      correlationId: run.work_item_id,
      payload: { reason: input.reason ?? null },
    });

    return { event };
  });

  if ("missing" in result) throw notFound(`run ${runId} not found`);
  if ("notPaused" in result) {
    throw conflict(`run ${runId} is ${result.notPaused}, not paused`);
  }
  eventBus.publish(result.event);
  return json({ ok: true, status: "pending" });
}

/**
 * Abort a Run.
 *
 * Unlike pause, this is terminal immediately in the control plane: the Run is
 * marked aborted so nothing schedules more work for it, and the runner tears
 * down the container when it next polls. Logs, events and artifacts are
 * preserved (plan §24) — abort stops the work, it does not erase it.
 */
async function abortRun(ctx: RequestContext): Promise<Response> {
  const input = await parseBody(ctx.request, reasonInput);
  const runId = ctx.params.id!;
  const { organizationId } = ctx.principal;

  const result = await withOrg(organizationId, async (scope) => {
    const loaded = await loadRun(scope, runId, isLive);
    if (!("run" in loaded)) return loaded;
    const { run } = loaded;

    await scope.sql`
      UPDATE runs
      SET status = 'aborted',
          control = 'abort',
          control_requested_at = now(),
          control_reason = ${input.reason ?? null},
          ended_at = now(),
          lease_expires_at = NULL
      WHERE id = ${runId}`;

    // The work item stops too: a Run aborted by a human should not leave its
    // work item looking like it is still progressing.
    await scope.sql`
      UPDATE work_items SET status = 'aborted'
      WHERE id = ${run.work_item_id} AND status NOT IN ('done', 'failed', 'aborted')`;

    const event = await appendInScope(scope, {
      eventType: EventTypes.RunAborted,
      organizationId,
      projectId: run.project_id,
      workItemId: run.work_item_id,
      runId,
      actor: { type: "human", id: ctx.principal.apiKeyId },
      source: "control-plane",
      correlationId: run.work_item_id,
      payload: { reason: input.reason ?? null },
    });

    return { event };
  });

  if ("missing" in result) throw notFound(`run ${runId} not found`);
  if ("rejected" in result) {
    throw conflict(`run ${runId} is already ${result.rejected}`);
  }
  eventBus.publish(result.event);
  return json({ ok: true, status: "aborted" });
}

export function registerInterventionRoutes(router: Router): void {
  router.post("/v1/runs/:id/steer", steerRun);
  router.get("/v1/runs/:id/directives", listDirectives);

  router.post("/v1/runs/:id/pause", pauseRun);
  router.post("/v1/runs/:id/resume", resumeRun);
  router.post("/v1/runs/:id/abort", abortRun);
}
