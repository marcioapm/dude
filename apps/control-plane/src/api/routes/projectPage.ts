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
  owners: Array<{ id: string; name: string }>;
  prs: Record<string, number>;
  costUsd: number;
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
             -- Who drives its tasks, most tasks first.
             COALESCE((SELECT json_agg(json_build_object('id', o.id, 'name', o.name) ORDER BY o.n DESC, o.name)
                       FROM (SELECT k.id, k.name, count(*) AS n FROM tasks t JOIN api_keys k ON k.id = t.owner_key_id
                             WHERE t.epic_id = e.id AND k.revoked_at IS NULL GROUP BY k.id, k.name) o), '[]') AS owners,
             COALESCE((SELECT json_object_agg(s.state, s.n) FROM (
                         SELECT pr.state::text AS state, count(*) AS n FROM pull_requests pr JOIN tasks t ON t.id = pr.task_id
                         WHERE t.epic_id = e.id GROUP BY pr.state) s), '{}') AS prs,
             COALESCE((SELECT sum((ev.payload->>'costUsd')::numeric) FROM events ev JOIN tasks t ON t.id = ev.task_id
                       WHERE t.epic_id = e.id AND ev.event_type = 'agent.model.request.completed'
                         AND jsonb_typeof(ev.payload->'costUsd') = 'number'), 0)::float8 AS "costUsd",
             (SELECT max(t.updated_at) FROM tasks t WHERE t.epic_id = e.id) AS "lastActivity",
             (SELECT count(*)::int FROM tasks t WHERE t.epic_id = e.id AND t.status IN ('awaiting_input', 'awaiting_confirmation')) AS "needsYou"
      FROM epics e WHERE e.project_id = ${projectId}
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
