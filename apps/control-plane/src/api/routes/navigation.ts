/**
 * The navigation tree: everything the sidebar and board need, in one read.
 *
 * The sidebar answers "what needs me, what is active, what is ready, who is
 * working on what" across every project at once, which means it needs the
 * whole hierarchy. Assembling that client-side would be a request per work
 * item; here it is a fixed handful of queries regardless of size.
 *
 * Deliberately a read model, not the domain records: it carries only what a
 * row renders, so the panel does not pay for goals, acceptance criteria,
 * workspace paths or model configuration it never shows.
 *
 * One mapping worth naming. The design system's tree is Work Item → attempt →
 * agent. With phased delivery an attempt is several Runs — implement, review,
 * fix — each one agent in its own container, so each *phase Run* becomes one
 * agent node under its attempt. That is what makes "who is working on this
 * right now" answerable from the tree: the running reviewer is a live row.
 */

import { DEFAULT_RUN_ROLE, TERMINAL_RUN_STATUSES, runLabel } from "@dude/domain";
import type { RunStatus, SessionStatus } from "@dude/domain";
import { withOrg } from "../../db/client.ts";
import { json } from "../http.ts";
import type { RequestContext, Router } from "../router.ts";

/** Attempts to carry per work item. Older ones are history. */
const ATTEMPTS_PER_WORK_ITEM = 3;

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
  key: string;
  projectId: string;
  epicId: string | null;
  title: string;
  status: string;
  requestedBy: string | null;
  statusSince: string;
}

interface RunRow {
  id: string;
  workItemId: string;
  attempt: number;
  status: RunStatus;
  phase: string | null;
  role: string | null;
  category: string | null;
  /** The open question the Run waits on, if any. */
  question: string | null;
}

/**
 * A Run's status as the tree's session vocabulary.
 *
 * The tree's agent rows speak SessionStatus, which has no scheduling detail
 * and no pause: a Run waiting for a worker is simply not running yet, and a
 * paused one is waiting on the person who paused it.
 */
const SESSION_STATUS: Record<RunStatus, SessionStatus> = {
  pending: "pending",
  scheduled: "pending",
  starting: "running",
  running: "running",
  paused: "awaiting_input",
  completed: "completed",
  failed: "failed",
  aborted: "aborted",
};

/** The status an attempt reports: its most recent phase's. */
function attemptStatus(runs: RunRow[]): RunStatus {
  const live = runs.find((r) => !TERMINAL_RUN_STATUSES.includes(r.status));
  return (live ?? runs[runs.length - 1])?.status ?? "pending";
}

/** Group rows by a key, preserving the order the query returned them in. */
function groupBy<T, K extends string | number | null>(rows: T[], key: (row: T) => K): Map<K, T[]> {
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
      ORDER BY position, created_at`) as EpicRow[];

    // `statusSince` is when the work item last changed status, read from the
    // ledger: the board shows time in column, and updated_at moves for
    // reasons that are not a column change.
    const workItems = (await sql`
      SELECT w.id, p.key_prefix || '-' || w.number AS key,
             w.project_id AS "projectId", w.epic_id AS "epicId", w.title, w.status,
             w.requested_by AS "requestedBy",
             COALESCE(
               (SELECT max(e.occurred_at) FROM events e
                WHERE e.work_item_id = w.id AND e.event_type = 'work_item.status_changed'),
               w.created_at
             ) AS "statusSince"
      FROM work_items w JOIN projects p ON p.id = w.project_id
      ORDER BY w.created_at DESC LIMIT 500`) as WorkItemRow[];

    const runs = (await sql`
      SELECT id, "workItemId", attempt, status, phase, role, category,
             -- The question an agent is waiting on: the Run is live, but
             -- blocked on a person. Open questions die with their Run.
             (SELECT q.prompt FROM questions q WHERE q.run_id = ranked.id AND q.status = 'open'
              ORDER BY q.asked_at DESC LIMIT 1) AS question
      FROM (
        SELECT r.id, r.work_item_id AS "workItemId", r.attempt, r.status, r.phase,
               r.role, r.category, r.created_at,
               dense_rank() OVER (PARTITION BY r.work_item_id ORDER BY r.attempt DESC) AS rank
        FROM runs r
      ) ranked
      WHERE rank <= ${ATTEMPTS_PER_WORK_ITEM}
      ORDER BY "workItemId", attempt, created_at`) as RunRow[];

    // Spend per work item. Cost events carry USD as reported by the harness.
    const costs = (await sql`
      SELECT work_item_id AS "workItemId",
             COALESCE(sum((payload->>'costUsd')::numeric), 0)::float8 AS "costUsd"
      FROM events
      WHERE event_type = 'agent.model.request.completed' AND work_item_id IS NOT NULL
        AND jsonb_typeof(payload->'costUsd') = 'number'
      GROUP BY work_item_id`) as Array<{ workItemId: string; costUsd: number }>;

    // -- assemble ----------------------------------------------------------

    const costByWorkItem = new Map(costs.map((c) => [c.workItemId, c.costUsd]));
    const runsByWorkItem = groupBy(runs, (r) => r.workItemId);
    const workItemsByEpic = groupBy(workItems, (w) => w.epicId);
    const workItemsByProject = groupBy(
      workItems.filter((w) => w.epicId === null),
      (w) => w.projectId,
    );
    const epicsByProject = groupBy(epics, (e) => e.projectId);

    const buildWorkItem = (w: WorkItemRow) => {
      const attempts = groupBy(runsByWorkItem.get(w.id) ?? [], (r) => r.attempt);
      return {
        id: w.id,
        key: w.key,
        title: w.title,
        status: w.status,
        statusSince: w.statusSince,
        costUsd: costByWorkItem.get(w.id) ?? 0,
        // Whoever asked for it is the person waiting on it. Real membership
        // lands with the organization model (plan §53).
        people: w.requestedBy ? [{ id: w.requestedBy, name: w.requestedBy }] : [],
        runs: [...attempts.entries()].map(([attempt, phaseRuns]) => ({
          // The attempt's id is its first Run's, so selecting the attempt
          // row can still land on a real Run.
          id: phaseRuns[0]!.id,
          attempt,
          status: attemptStatus(phaseRuns),
          sessions: phaseRuns.map((r) => ({
            id: r.id,
            role: r.role ?? DEFAULT_RUN_ROLE,
            status: r.question ? "awaiting_input" : SESSION_STATUS[r.status],
            title: runLabel(r),
            // What it asked, so the board and the attention list say it
            // without opening the chat.
            ...(r.question ? { activity: r.question } : {}),
          })),
        })),
      };
    };

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
