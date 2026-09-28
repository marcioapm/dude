/**
 * Time and cost, per Run, per task, per epic (migration 028's functions):
 * how long agents worked, how long they waited on people, how long the
 * change sat in review, what it cost. Read from what the ledger and the
 * Runs already record; seconds in the database, milliseconds here.
 */

import { costSplit } from "@dude/domain";
import { withOrg } from "../../db/client.ts";
import { json, notFound } from "../http.ts";
import type { RequestContext, Router } from "../router.ts";

const ms = (s: unknown) => (s === null || s === undefined ? null : Math.round(Number(s) * 1000));

const tokens = (row: Record<string, unknown>) => ({ input: Number(row.input_tokens), output: Number(row.output_tokens) });

/**
 * What it cost, both ways: `costUsd` stays the model's tokens, as it always
 * was, and `cost` is the whole, split into tokens and machine time.
 */
const costs = (row: Record<string, unknown>) => ({
  costUsd: Number(row.cost_usd),
  cost: costSplit(Number(row.cost_usd), Number(row.machine_usd)),
});

async function taskMetrics(ctx: RequestContext): Promise<Response> {
  const id = ctx.params.id!;
  const out = await withOrg(ctx.principal.organizationId, async ({ sql }) => {
    const [task] = (await sql`SELECT * FROM task_metrics(${id})`) as Array<Record<string, unknown>>;
    if (!task) return null;
    const runs = (await sql`
      SELECT r.id, r.phase, r.role, r.category, r.status, m.*
      FROM runs r CROSS JOIN LATERAL run_metrics(r.id) m
      WHERE r.task_id = ${id} ORDER BY r.created_at`) as Array<Record<string, unknown>>;
    return {
      leadMs: ms(task.lead_seconds),
      activeMs: ms(task.active_seconds),
      humanWaitMs: ms(task.human_wait_seconds),
      reviewMs: ms(task.review_seconds),
      ...costs(task),
      tokens: tokens(task),
      runs: runs.map((r) => ({
        id: r.id, phase: r.phase, role: r.role, category: r.category, status: r.status,
        activeMs: ms(r.active_seconds), parkedMs: ms(r.parked_seconds), ...costs(r),
        tokens: tokens(r),
      })),
    };
  });
  if (!out) throw notFound(`task ${id} not found`);
  return json(out);
}

async function epicMetrics(ctx: RequestContext): Promise<Response> {
  const id = ctx.params.id!;
  const out = await withOrg(ctx.principal.organizationId, async ({ sql }) => {
    const found = (await sql`SELECT 1 FROM epics WHERE id = ${id}`) as unknown[];
    if (found.length === 0) return null;
    const [m] = (await sql`SELECT * FROM epic_metrics(${id})`) as Array<Record<string, unknown>>;
    return {
      tasks: Number(m!.tasks),
      done: Number(m!.done),
      leadMsMedian: ms(m!.lead_seconds_median),
      activeMs: ms(m!.active_seconds),
      humanWaitMs: ms(m!.human_wait_seconds),
      reviewMs: ms(m!.review_seconds),
      ...costs(m!),
      tokens: tokens(m!),
    };
  });
  if (!out) throw notFound(`epic ${id} not found`);
  return json(out);
}

export function registerMetricsRoutes(router: Router): void {
  router.get("/v1/tasks/:id/metrics", taskMetrics);
  router.get("/v1/epics/:id/metrics", epicMetrics);
}
