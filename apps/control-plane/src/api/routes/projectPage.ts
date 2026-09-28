/**
 * A project's page: its epics by state — in progress as cards, planned and
 * done as lists — each with how its tasks stand, its pull requests, who
 * drives its work and what it cost.
 *
 * The sidebar's tree has the tasks but not this: pull requests, costs and
 * finish times are per epic here, in one read, so the page does not add up
 * a request per task.
 */

import { epicState, type EpicState } from "@dude/domain";
import { withOrg } from "../../db/client.ts";
import { json, notFound } from "../http.ts";
import type { RequestContext, Router } from "../router.ts";

/** A task status, as the lane the project page counts it in. */
const LANE: Record<string, "done" | "review" | "progress" | "backlog"> = {
  received: "backlog",
  intake: "backlog",
  awaiting_confirmation: "backlog",
  queued: "backlog",
  running: "progress",
  awaiting_input: "progress",
  review: "review",
  ready_to_merge: "review",
  done: "done",
  failed: "done",
  aborted: "done",
};

interface EpicRow {
  id: string;
  title: string;
  description: string;
  position: number;
  storedState: EpicState | null;
  createdAt: string;
  updatedAt: string;
  statuses: string[];
  owners: Array<{ id: string; name: string; photoUrl: string | null; online: boolean }>;
  prs: Record<string, number>;
  costUsd: number;
  machineUsd: number;
  lastActivity: string | null;
  needsYou: number;
}

async function projectOverview(ctx: RequestContext): Promise<Response> {
  const projectId = ctx.params.id!;
  const overview = await withOrg(ctx.principal.organizationId, async (scope) => {
    const projects = (await scope.sql`SELECT id, name FROM projects WHERE id = ${projectId}`) as Array<{ id: string; name: string }>;
    if (!projects[0]) return null;
    const epics = (await scope.sql`
      SELECT e.id, e.title, e.description, e.position, e.state AS "storedState",
             e.created_at AS "createdAt", e.updated_at AS "updatedAt",
             COALESCE((SELECT array_agg(t.status::text) FROM tasks t WHERE t.epic_id = e.id), '{}') AS statuses,
             -- Who owns its tasks, as people, most tasks first.
             COALESCE((SELECT json_agg(person_ref(p) ORDER BY o.n DESC, p.name)
                       FROM (SELECT tp.person_id, count(*) AS n
                             FROM tasks t JOIN task_people tp ON tp.task_id = t.id AND tp.position = 0
                             WHERE t.epic_id = e.id GROUP BY tp.person_id) o
                       JOIN people p ON p.id = o.person_id AND p.removed_at IS NULL), '[]') AS owners,
             COALESCE((SELECT json_object_agg(s.state, s.n) FROM (
                         SELECT pr.state::text AS state, count(*) AS n FROM pull_requests pr JOIN tasks t ON t.id = pr.task_id
                         WHERE t.epic_id = e.id GROUP BY pr.state) s), '{}') AS prs,
             -- What it cost, tokens and machine time, as its metrics count it.
             m.cost_usd AS "costUsd", m.machine_usd AS "machineUsd",
             (SELECT max(t.updated_at) FROM tasks t WHERE t.epic_id = e.id) AS "lastActivity",
             (SELECT count(*)::int FROM tasks t WHERE t.epic_id = e.id AND t.status IN ('awaiting_input', 'awaiting_confirmation')) AS "needsYou"
      FROM epics e CROSS JOIN LATERAL epic_metrics(e.id) m WHERE e.project_id = ${projectId}
      ORDER BY e.position, e.created_at`) as EpicRow[];

    return {
      project: projects[0],
      epics: epics.map(({ statuses, storedState, ...e }) => {
        const lanes = { done: 0, review: 0, progress: 0, backlog: 0 };
        for (const s of statuses) lanes[LANE[s] ?? "backlog"]++;
        return {
          ...e,
          state: epicState(storedState, statuses),
          stateSet: storedState !== null,
          tasks: statuses.length,
          lanes,
        };
      }),
    };
  });
  if (!overview) throw notFound(`project ${projectId} not found`);
  return json(overview);
}

export function registerProjectPageRoutes(router: Router): void {
  router.get("/v1/projects/:id/overview", projectOverview);
}
