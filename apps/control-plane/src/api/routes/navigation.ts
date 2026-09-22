/**
 * The navigation tree: everything the sidebar needs, in one read.
 *
 * The sidebar answers "what needs me, what is active, what is ready, who is
 * working on what" across every project at once, which means it needs the
 * whole hierarchy — projects, epics, work items, their current run and that
 * run's sessions. Assembling that client-side would be a request per work
 * item; assembling it per level here is five queries regardless of size.
 *
 * Deliberately a read model, not the domain records: it carries only what a
 * row renders, so the panel does not pay for goals, acceptance criteria,
 * workspace paths or model configuration it never shows.
 */

import { withOrg } from "../../db/client.ts";
import { json } from "../http.ts";
import type { RequestContext, Router } from "../router.ts";

/** Attempts to carry per work item. Older ones fold into one row each. */
const RUNS_PER_WORK_ITEM = 5;

interface ProjectRow {
  id: string;
  name: string;
}

interface EpicRow {
  id: string;
  projectId: string;
  title: string;
}

interface WorkItemRow {
  id: string;
  projectId: string;
  epicId: string | null;
  title: string;
  status: string;
  requestedBy: string | null;
}

interface RunRow {
  id: string;
  workItemId: string;
  attempt: number;
  status: string;
}

interface SessionRow {
  id: string;
  runId: string;
  parentSessionId: string | null;
  role: string;
  status: string;
}

/** Group rows by a key, preserving the order the query returned them in. */
function groupBy<T, K extends string | null>(rows: T[], key: (row: T) => K): Map<K, T[]> {
  const out = new Map<K, T[]>();
  for (const row of rows) {
    const k = key(row);
    const bucket = out.get(k);
    if (bucket) bucket.push(row);
    else out.set(k, [row]);
  }
  return out;
}

async function getNavigation(ctx: RequestContext): Promise<Response> {
  const tree = await withOrg(ctx.principal.organizationId, async (scope) => {
    const { sql } = scope;

    const projects = (await sql`
      SELECT id, name FROM projects ORDER BY name`) as ProjectRow[];
    if (projects.length === 0) return [];

    const epics = (await sql`
      SELECT id, project_id AS "projectId", title FROM epics
      ORDER BY created_at`) as EpicRow[];

    const workItems = (await sql`
      SELECT id, project_id AS "projectId", epic_id AS "epicId", title, status,
             requested_by AS "requestedBy"
      FROM work_items ORDER BY created_at DESC LIMIT 500`) as WorkItemRow[];

    // Only the most recent attempts: older ones are history, and the tree
    // folds them into a single row each anyway.
    const runs = (await sql`
      SELECT id, "workItemId", attempt, status FROM (
        SELECT r.id, r.work_item_id AS "workItemId", r.attempt, r.status,
               row_number() OVER (PARTITION BY r.work_item_id ORDER BY r.attempt DESC) AS rn
        FROM runs r
      ) ranked
      WHERE rn <= ${RUNS_PER_WORK_ITEM}
      ORDER BY "workItemId", attempt ASC`) as RunRow[];

    const sessions =
      runs.length === 0
        ? []
        : ((await sql`
            SELECT id, run_id AS "runId", parent_session_id AS "parentSessionId", role, status
            FROM sessions WHERE run_id IN ${sql(runs.map((r) => r.id))}
            ORDER BY created_at ASC`) as SessionRow[]);

    // -- assemble, deepest first ------------------------------------------

    const sessionsByRun = groupBy(sessions, (s) => s.runId);
    const runsByWorkItem = groupBy(runs, (r) => r.workItemId);
    const workItemsByEpic = groupBy(workItems, (w) => w.epicId);
    const workItemsByProject = groupBy(
      workItems.filter((w) => w.epicId === null),
      (w) => w.projectId,
    );
    const epicsByProject = groupBy(epics, (e) => e.projectId);

    /** Nest a run's sessions under their parents; subagents are children. */
    const nestSessions = (runId: string) => {
      const flat = sessionsByRun.get(runId) ?? [];
      const nodes = new Map(
        flat.map((s) => [
          s.id,
          { id: s.id, role: s.role, status: s.status, children: [] as unknown[] },
        ]),
      );
      const roots: unknown[] = [];
      for (const s of flat) {
        const node = nodes.get(s.id)!;
        const parent = s.parentSessionId ? nodes.get(s.parentSessionId) : undefined;
        if (parent) parent.children.push(node);
        else roots.push(node);
      }
      return roots;
    };

    const buildWorkItem = (w: WorkItemRow) => ({
      id: w.id,
      title: w.title,
      status: w.status,
      // Whoever asked for it is the person waiting on it. Real membership
      // lands with the organization model (plan §53).
      people: w.requestedBy ? [{ id: w.requestedBy, name: w.requestedBy }] : [],
      runs: (runsByWorkItem.get(w.id) ?? []).map((r) => ({
        id: r.id,
        attempt: r.attempt,
        status: r.status,
        sessions: nestSessions(r.id),
      })),
    });

    return projects.map((p) => ({
      id: p.id,
      name: p.name,
      epics: (epicsByProject.get(p.id) ?? []).map((e) => ({
        id: e.id,
        title: e.title,
        workItems: (workItemsByEpic.get(e.id) ?? []).map(buildWorkItem),
      })),
      workItems: (workItemsByProject.get(p.id) ?? []).map(buildWorkItem),
    }));
  });

  return json({ projects: tree });
}

export function registerNavigationRoutes(router: Router): void {
  router.get("/v1/navigation", getNavigation);
}
