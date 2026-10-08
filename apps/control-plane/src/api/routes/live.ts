/**
 * A Run's diff: its agent's checkout against the commit it started from,
 * uncommitted work included. The orchestrator reads it through lux while
 * the agent works, and lux's beforeStop hook leaves the final one when the
 * container stops; the latest is kept (run_diffs). Each change is a
 * run.diff.updated event on the stream — a summary, with no lines — which
 * tells a watching browser to fetch this.
 */

import type { RunDiff } from "@dude/domain";
import { withOrg } from "../../db/client.ts";
import { json, notFound } from "../http.ts";
import type { RequestContext, Router } from "../router.ts";

async function runDiff(ctx: RequestContext): Promise<Response> {
  const id = ctx.params.id!;
  const out = await withOrg(ctx.principal.organizationId, async ({ sql }) => {
    const [row] = (await sql`
      SELECT r.id, d.base, d.files, d.checksum, d.final, d.updated_at AS "updatedAt"
      FROM runs r LEFT JOIN run_diffs d ON d.run_id = r.id
      WHERE r.id = ${id} AND (r.session_id IS NULL OR session_role(r.session_id, ${ctx.principal.personId}) IS NOT NULL)`) as Array<{ id: string; base: string | null; files: RunDiff["files"] | null;
        checksum: string | null; final: boolean | null; updatedAt: Date | null }>;
    return row;
  });
  if (!out) throw notFound(`run ${id} not found`);
  // Not read yet (the agent has not started, or never ran on lux): nothing
  // changed, against no commit.
  const diff: RunDiff = {
    base: out.base ?? "",
    files: out.files ?? [],
    checksum: out.checksum ?? "",
    final: out.final ?? false,
    updatedAt: (out.updatedAt ?? new Date(0)).toISOString(),
  };
  return json(diff);
}

export function registerLiveRoutes(router: Router): void {
  router.get("/v1/runs/:id/diff", runDiff);
}
