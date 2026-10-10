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
 * One mapping worth naming. The design system's tree is Task → attempt →
 * agent. With phased delivery an attempt is several Runs — implement, review,
 * fix — each one agent in its own container, so each *phase Run* becomes one
 * agent node under its attempt. That is what makes "who is working on this
 * right now" answerable from the tree: the running reviewer is a live row.
 */

import { peopleJson } from "./people.ts";
import { escalationJson } from "./work.ts";
import { DEFAULT_RUN_ROLE, TERMINAL_RUN_STATUSES, isConductor, runLabel } from "@dude/domain";
import type { Escalation, PersonRef, RunStatus, SessionStatus } from "@dude/domain";
import { withOrg } from "../../db/client.ts";
import { json } from "../http.ts";
import type { RequestContext, Router } from "../router.ts";
import { PROJECT_IMAGE_URL } from "./projects.ts";

/** Attempts to carry per task. Older ones are history. */
const ATTEMPTS_PER_TASK = 3;

interface ProjectRow {
  id: string;
  name: string;
  imageUrl: string | null;
}

interface EpicRow {
  id: string;
  projectId: string;
  title: string;
}

interface TaskRow {
  id: string;
  key: string;
  projectId: string;
  epicId: string | null;
  title: string;
  status: string;
  /** Everyone on it, the owner first. */
  people: PersonRef[];
  statusSince: string;
  escalation: Escalation | null;
}

interface RunRow {
  id: string;
  taskId: string;
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

/**
 * The status an attempt reports: its most recent phase's. A task's
 * conductor talks about the work and is not a step of it: it reports the
 * attempt only when nothing else ran.
 */
function attemptStatus(all: RunRow[]): RunStatus {
  const phases = all.filter((r) => !isConductor(r));
  const runs = phases.length > 0 ? phases : all;
  const live = runs.find((r) => !TERMINAL_RUN_STATUSES.includes(r.status));
  return (live ?? runs[runs.length - 1])?.status ?? "pending";
}

/**
 * A Run as a session row: what it asks of a person, if anything, else its
 * status. A conductor dude parked between messages is quiet — waiting, not
 * waiting on you: nobody owes it anything.
 */
function sessionStatus(r: RunRow): SessionStatus {
  if (r.question) return "awaiting_input";
  if (isConductor(r) && r.status === "paused") return "pending";
  return SESSION_STATUS[r.status];
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
      SELECT id, name, ${sql.unsafe(PROJECT_IMAGE_URL)} AS "imageUrl" FROM projects ORDER BY name`) as ProjectRow[];
    if (projects.length === 0) return [];

    const epics = (await sql`
      SELECT id, project_id AS "projectId", title FROM epics
      ORDER BY position, created_at`) as EpicRow[];

    // `statusSince` is when the task last changed status, read from the
    // ledger: the board shows time in column, and updated_at moves for
    // reasons that are not a column change.
    const tasks = (await sql`
      SELECT w.id, p.key_prefix || '-' || w.number AS key,
             w.project_id AS "projectId", w.epic_id AS "epicId", w.title, w.status,
             ${sql.unsafe(peopleJson("w"))}, ${sql.unsafe(escalationJson("w"))},
             COALESCE(
               (SELECT max(e.occurred_at) FROM events e
                WHERE e.task_id = w.id AND e.event_type = 'task.status_changed'),
               w.created_at
             ) AS "statusSince"
      FROM tasks w JOIN projects p ON p.id = w.project_id
      ORDER BY w.created_at DESC LIMIT 500`) as TaskRow[];

    const runs = (await sql`
      SELECT id, "taskId", attempt, status, phase, role, category,
             -- The question an agent is waiting on: the Run is live, but
             -- blocked on a person. Open questions die with their Run.
             -- Or a repository it asked for, pending a person's decision.
             -- Several questions at once read as how many and their headers.
             COALESCE(
               (SELECT CASE WHEN jsonb_array_length(q.items) > 1
                         THEN 'asks ' || jsonb_array_length(q.items) || ' questions · ' ||
                              (SELECT string_agg(i->>'header', ', ' ORDER BY n) FROM jsonb_array_elements(q.items) WITH ORDINALITY AS t(i, n))
                         ELSE q.prompt END
                FROM questions q WHERE q.run_id = ranked.id AND q.status = 'open'
                ORDER BY q.asked_at DESC LIMIT 1),
               (SELECT CASE q.access WHEN 'write' THEN 'Change ' ELSE 'Read ' END || repo.name || '?'
                FROM repository_requests q JOIN repositories repo ON repo.id = q.repository_id
                WHERE q.run_id = ranked.id AND q.status = 'pending' ORDER BY q.created_at DESC LIMIT 1)
             ) AS question
      FROM (
        SELECT r.id, r.task_id AS "taskId", r.attempt, r.status, r.phase,
               r.role, r.category, r.created_at,
               dense_rank() OVER (PARTITION BY r.task_id ORDER BY r.attempt DESC) AS rank
        FROM runs r
        -- The agents: a branch preview is a task's servers, not one of them;
        -- a session's agent is no task's.
        WHERE r.kind = 'agent' AND r.task_id IS NOT NULL
      ) ranked
      WHERE rank <= ${ATTEMPTS_PER_TASK}
      -- The task's conductor first in its attempt, as in its Sessions.
      ORDER BY "taskId", attempt, (role = 'conductor' AND phase IS NULL) DESC, created_at`) as RunRow[];

    // Spend per task: its agents' model cost by the one rule every screen
    // uses (run_model_usd, migration 061): lux's AI cost once reported, else
    // what the harness reported — never both.
    const costs = (await sql`
      SELECT r.task_id AS "taskId", COALESCE(sum(run_model_usd(r)), 0)::float8 AS "costUsd"
      FROM runs r
      WHERE r.kind = 'agent' AND r.task_id IS NOT NULL
      GROUP BY r.task_id`) as Array<{ taskId: string; costUsd: number }>;

    // -- assemble ----------------------------------------------------------

    const costByTask = new Map(costs.map((c) => [c.taskId, c.costUsd]));
    const runsByTask = groupBy(runs, (r) => r.taskId);
    const tasksByEpic = groupBy(tasks, (w) => w.epicId);
    const tasksByProject = groupBy(
      tasks.filter((w) => w.epicId === null),
      (w) => w.projectId,
    );
    const epicsByProject = groupBy(epics, (e) => e.projectId);

    const buildTask = (w: TaskRow) => {
      const attempts = groupBy(runsByTask.get(w.id) ?? [], (r) => r.attempt);
      return {
        id: w.id,
        key: w.key,
        title: w.title,
        status: w.status,
        statusSince: w.statusSince,
        costUsd: costByTask.get(w.id) ?? 0,
        // Who is on it, the owner first: the person it waits on when an
        // agent asks, and who "Waiting on you" is for.
        people: w.people,
        // Why delivery stopped for a person, so "Needs you" can say it.
        escalation: w.escalation,
        runs: [...attempts.entries()].map(([attempt, phaseRuns]) => ({
          // The attempt's id is its first Run's, so selecting the attempt
          // row can still land on a real Run.
          id: phaseRuns[0]!.id,
          attempt,
          status: attemptStatus(phaseRuns),
          sessions: phaseRuns.map((r) => ({
            id: r.id,
            role: r.role ?? DEFAULT_RUN_ROLE,
            status: sessionStatus(r),
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
      imageUrl: p.imageUrl,
      epics: (epicsByProject.get(p.id) ?? []).map((e) => ({
        id: e.id,
        title: e.title,
        tasks: (tasksByEpic.get(e.id) ?? []).map(buildTask),
      })),
      tasks: (tasksByProject.get(p.id) ?? []).map(buildTask),
    }));
  });

  return json({ projects: tree });
}

export function registerNavigationRoutes(router: Router): void {
  router.get("/v1/navigation", getNavigation);
}
