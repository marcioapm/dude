/**
 * Task, run and session routes.
 *
 * A Task is the user-facing unit of requested work. A Run is one
 * execution attempt of it — retrying never erases a prior attempt, so cost,
 * duration and failure remain inspectable per attempt (plan §39).
 */

import { auditActor } from "../auth.ts";
import { z } from "zod";
import {
  ATTACHMENT_LIMITS, BUILDER_OFFLINE_SECONDS, EventTypes, TASK_GOAL_TOO_SHORT, TASK_GOAL_TOO_SHORT_DETAILS, agentRoleSchema, newId,
  resolveAgentModel, resolveTier,
  taskCriteriaInput, taskGoalInput, taskGoalShortBy,
} from "@dude/domain";
import type { AgentModels } from "@dude/domain";
import { withOrg, withoutTenant, type OrgScope } from "../../db/client.ts";
import { appendInScope } from "../../events/ledger.ts";
import { badRequest, conflict, json, notFound, parseBody } from "../http.ts";
import { orchestrator } from "../../orchestrator/client.ts";
import { requireOrgAdmin } from "../access.ts";
import { listTiers } from "./models.ts";
import { REPOSITORIES_JSON, setTaskRepositories, taskRepositoriesInput } from "./taskRepositories.ts";
import { ownerJson, peopleJson } from "./people.ts";
import type { RequestContext, Router } from "../router.ts";

const TASK_SELECT = `
  id, organization_id AS "organizationId", project_id AS "projectId", epic_id AS "epicId", ${REPOSITORIES_JSON},
  title, goal, acceptance_criteria AS "acceptanceCriteria", status,
  (SELECT key_prefix FROM projects p WHERE p.id = tasks.project_id) || '-' || number AS key, -- see navigation.ts
  requested_by AS "requestedBy", ${ownerJson()}, ${peopleJson()}, created_at AS "createdAt", updated_at AS "updatedAt"`;

// A Run's columns, as a fragment of `sql`: the builder's offline threshold
// is a parameter.
const runSelect = (sql: OrgScope["sql"]) => sql`
  id, organization_id AS "organizationId", project_id AS "projectId",
  task_id AS "taskId", attempt, status, error, kind,
  phase, role, category, parent_run_id AS "parentRunId", base_refs AS "baseRefs",
  (SELECT COALESCE(json_object_agg(k, v->>'sha'), '{}'::json) FROM jsonb_each(heads) AS h(k, v)) AS heads,
  branch, harness, model, model_tier AS "modelTier", dude_pause AS "dudePause", machine, image,
  -- Waiting for its image: the job it waits on is still queued or running,
  -- and it has no lux Run yet (a phase Run pending, a woken preview paused).
  (SELECT json_build_object('buildId', b.id, 'state', b.state, 'kind', b.kind, 'imageName', i.name, 'version', v.number,
      'builderOfflineSince', CASE WHEN ib.seen_at IS NULL OR ib.seen_at < now() - make_interval(secs => ${BUILDER_OFFLINE_SECONDS})
        THEN COALESCE(ib.seen_at, runs.image_waiting_since) END)
   FROM image_builds b JOIN image_versions v ON v.id = b.image_version_id JOIN images i ON i.id = v.image_id
   LEFT JOIN image_builder ib ON true
   WHERE b.id = runs.image_build_id AND b.state IN ('queued', 'running') AND runs.lux_run_id IS NULL
           AND runs.status NOT IN ('completed', 'failed', 'aborted')) AS "preparingImage",
  json_build_object('input', input_tokens, 'output', output_tokens, 'cacheRead', cache_read_tokens,
    'cacheWrite', cache_write_tokens, 'context', context_tokens) AS tokens,
  created_at AS "createdAt", started_at AS "startedAt", ended_at AS "endedAt"`;

const SESSION_SELECT = `
  id, organization_id AS "organizationId", run_id AS "runId",
  parent_session_id AS "parentSessionId", role, harness, model, status,
  external_session_id AS "externalSessionId",
  created_at AS "createdAt", ended_at AS "endedAt"`;

/**
 * The SELECT expression for a task's open escalation, `{reason, detail, at}`
 * or null, for the `tasks` rows under `alias`: the workflow's last
 * `question.asked` of kind "escalation" (delivery.escalate), while the task
 * still waits on a person — nothing has moved it out of awaiting_input
 * since. The workflow ends on it, so there is at most one worth showing.
 */
export function escalationJson(alias = "tasks"): string {
  return `(SELECT json_build_object('reason', e.payload->>'reason', 'detail', e.payload->'detail',
    'actions', COALESCE(e.payload->'actions', '["stop"]'::jsonb), 'at', e.occurred_at)
  FROM events e
  WHERE ${alias}.status = 'awaiting_input'
    AND e.task_id = ${alias}.id AND e.event_type = 'question.asked' AND e.payload->>'kind' = 'escalation'
    AND e.cursor > COALESCE((SELECT max(s.cursor) FROM events s WHERE s.task_id = ${alias}.id
      AND s.event_type = 'task.status_changed' AND s.payload->>'status' <> 'awaiting_input'), 0)
    -- Decided: the workflow is carrying it out.
    AND NOT EXISTS (SELECT 1 FROM events d WHERE d.task_id = ${alias}.id AND d.event_type = 'task.decided'
      AND d.cursor > e.cursor)
  ORDER BY e.cursor DESC LIMIT 1) AS escalation`;
}

// ---------------------------------------------------------------------------
// Tasks
// ---------------------------------------------------------------------------

const createTaskInput = z.object({
  projectId: z.string().min(1),
  epicId: z.string().min(1).nullable().default(null),
  /** The repositories it works on; the project's only one when none is named. */
  repositories: taskRepositoriesInput.default([]),
  title: z.string().min(1).max(500),
  goal: taskGoalInput.default(""),
  acceptanceCriteria: taskCriteriaInput.default([]),
});

async function createTask(ctx: RequestContext): Promise<Response> {
  const input = await parseBody(ctx.request, createTaskInput);
  if (taskGoalShortBy(input.goal ?? "") > 0) throw badRequest(TASK_GOAL_TOO_SHORT, TASK_GOAL_TOO_SHORT_DETAILS);
  const { organizationId } = ctx.principal;

  const result = await withOrg(organizationId, async (scope) => {
    // RLS already confines this to the caller's org, so a miss means the
    // project does not exist *for them* — which is the correct 404 either way.
    // Takes the project's next number, locking the row so two creates
    // cannot take the same one.
    const project = (await scope.sql`
      UPDATE projects SET next_task_number = next_task_number + 1
      WHERE id = ${input.projectId}
      RETURNING next_task_number - 1 AS number`) as Array<{ number: number }>;
    if (project.length === 0) return { missingProject: true as const };

    const taskId = newId("task");
    // Whoever creates it drives it until they hand it to someone.
    await scope.sql`
      INSERT INTO tasks (id, organization_id, project_id, number, epic_id, title, goal,
                               acceptance_criteria, status)
       VALUES (${taskId}, ${organizationId}, ${input.projectId}, ${project[0]!.number}, ${input.epicId},
               ${input.title}, ${input.goal},
               ${input.acceptanceCriteria ?? []}::jsonb, 'received')`;
     await scope.sql`
       INSERT INTO task_people (task_id, person_id, organization_id, position)
       VALUES (${taskId}, ${ctx.principal.personId}, ${organizationId}, 0)`;
    const missing = await setTaskRepositories(scope, organizationId, input.projectId, taskId, input.repositories ?? []);
    // Thrown, so the transaction and the task's number roll back.
    if (missing) throw notFound(`repository ${missing} is not in this project`);
    const rows = (await scope.sql`
      SELECT ${scope.sql.unsafe(TASK_SELECT)} FROM tasks WHERE id = ${taskId}`) as Array<Record<string, unknown>>;

    const actor = auditActor(ctx.principal);
    const event = await appendInScope(scope, {
      eventType: EventTypes.TaskCreated,
      organizationId,
      projectId: input.projectId,
      taskId,
      actor: { type: actor.kind, id: actor.id },
      source: "control-plane",
      correlationId: taskId,
      payload: { title: input.title, goal: input.goal },
    });

    return { task: rows[0]!, event };
  });

  if ("missingProject" in result) throw notFound(`project ${input.projectId} not found`);
  return json(result.task, 201);
}

const deliverInput = z.object({
  /** Overrides for this task only; unset fields keep the default. */
  policy: z.record(z.string(), z.unknown()).optional(),
  /** Images uploaded to the task, given with its prompt to every agent the task is the prompt of. */
  attachmentIds: z.array(z.string().min(1)).max(ATTACHMENT_LIMITS.perMessage).optional(),
});

/**
 * Start the delivery workflow for a task.
 *
 * The orchestrator runs it; this authenticates the user and forwards. The
 * task is the idempotency key there, so a second call joins the
 * delivery already in flight.
 */
async function deliverTask(ctx: RequestContext): Promise<Response> {
  const input = await parseBody(ctx.request, deliverInput);
  // The policy is what admins set: only they may loosen it for one task.
  if (input.policy) await requireOrgAdmin(ctx);
  return orchestrator(ctx.principal.organizationId, "POST", `/internal/tasks/${ctx.params.id}/deliver`,
    JSON.stringify(input), ctx.principal);
}

/** Mark finished work that has nothing to merge done: a person has read it. */
async function markTaskDone(ctx: RequestContext): Promise<Response> {
  return orchestrator(ctx.principal.organizationId, "POST", `/internal/tasks/${ctx.params.id}/done`,
    "{}", ctx.principal);
}

/** Its owner decides how delivery goes on after it stopped for them. */
async function decideTask(ctx: RequestContext): Promise<Response> {
  return orchestrator(ctx.principal.organizationId, "POST", `/internal/tasks/${ctx.params.id}/decide`,
    await ctx.request.text(), ctx.principal);
}

async function listTasks(ctx: RequestContext): Promise<Response> {
  const projectId = ctx.url.searchParams.get("projectId");
  const status = ctx.url.searchParams.get("status");

  const tasks = await withOrg(ctx.principal.organizationId, async (scope) => {
    return (await scope.sql`
      SELECT ${scope.sql.unsafe(TASK_SELECT)} FROM tasks
      WHERE (${projectId}::text IS NULL OR project_id = ${projectId})
        AND (${status}::text IS NULL OR status::text = ${status})
      ORDER BY created_at DESC
      LIMIT 200`) as Array<Record<string, unknown>>;
  });
  return json({ tasks });
}

async function getTask(ctx: RequestContext): Promise<Response> {
  const id = ctx.params.id!;
  const task = await withOrg(ctx.principal.organizationId, async (scope) => {
    const rows = (await scope.sql`
      SELECT ${scope.sql.unsafe(TASK_SELECT)}, ${scope.sql.unsafe(escalationJson())}
      FROM tasks WHERE id = ${id}`) as Array<
      Record<string, unknown>
    >;
    if (!rows[0]) return null;
    const runs = await scope.sql`
      SELECT ${runSelect(scope.sql)} FROM runs WHERE task_id = ${id}
      ORDER BY attempt DESC`;
    return { ...rows[0], runs };
  });

  if (!task) throw notFound(`task ${id} not found`);
  return json(task);
}

// ---------------------------------------------------------------------------
// Runs
// ---------------------------------------------------------------------------

/**
 * Create a Run for a Task.
 *
 * The attempt number is derived inside the transaction, and `runs` has a
 * UNIQUE(task_id, attempt) constraint, so two concurrent creates cannot
 * both claim the same attempt — the loser gets a 409 rather than a duplicate.
 */
async function createRun(ctx: RequestContext): Promise<Response> {
  const taskId = ctx.params.id!;
  const { organizationId } = ctx.principal;

  const result = await withOrg(organizationId, async (scope) => {
    const tasks = (await scope.sql`
      SELECT id, project_id FROM tasks WHERE id = ${taskId} LIMIT 1`) as Array<{
      id: string;
      project_id: string;
    }>;
    const task = tasks[0];
    if (!task) return { missing: true as const };

    const attemptRows = (await scope.sql`
      SELECT COALESCE(MAX(attempt), 0) + 1 AS attempt FROM runs WHERE task_id = ${taskId}`) as Array<{
      attempt: number;
    }>;
    const attempt = Number(attemptRows[0]?.attempt ?? 1);

    const runId = newId("run");
    const rows = (await scope.sql`
      INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status)
      VALUES (${runId}, ${organizationId}, ${task.project_id}, ${taskId}, ${attempt}, 'pending')
      RETURNING ${runSelect(scope.sql)}`) as Array<Record<string, unknown>>;

    await scope.sql`UPDATE tasks SET status = 'queued' WHERE id = ${taskId}`;

    const actor = auditActor(ctx.principal);
    const event = await appendInScope(scope, {
      eventType: EventTypes.RunCreated,
      organizationId,
      projectId: task.project_id,
      taskId,
      runId,
      actor: { type: actor.kind, id: actor.id },
      source: "control-plane",
      correlationId: taskId,
      payload: { attempt },
    });

    return { run: rows[0]!, event };
  }).catch((err: unknown) => {
    if (err instanceof Error && err.message.includes("runs_task_id_attempt_key")) {
      return { raced: true as const };
    }
    throw err;
  });

  if ("missing" in result) throw notFound(`task ${taskId} not found`);
  if ("raced" in result) throw conflict("a run for this attempt was created concurrently; retry");
  return json(result.run, 201);
}

async function getRun(ctx: RequestContext): Promise<Response> {
  const id = ctx.params.id!;
  const run = await withOrg(ctx.principal.organizationId, async (scope) => {
    const rows = (await scope.sql`
      SELECT ${runSelect(scope.sql)} FROM runs WHERE id = ${id}`) as Array<Record<string, unknown>>;
    if (!rows[0]) return null;
    const sessions = await scope.sql`
      SELECT ${scope.sql.unsafe(SESSION_SELECT)} FROM sessions WHERE run_id = ${id}
      ORDER BY created_at ASC`;
    return { ...rows[0], sessions };
  });

  if (!run) throw notFound(`run ${id} not found`);
  return json(run);
}

// ---------------------------------------------------------------------------
// Sessions
// ---------------------------------------------------------------------------

const createSessionInput = z.object({
  role: agentRoleSchema,
  parentSessionId: z.string().min(1).nullable().default(null),
  /** Overrides the resolved project/org tier when set (an organization's tier id). */
  tier: z.string().min(1).optional(),
  harness: z.string().min(1).optional(),
}).strict();

const DEFAULT_HARNESS = "opencode";

/**
 * Create a Session under a Run, its model the one its tier requests: the
 * role's tier from the project's per-role configuration, then the
 * organization default.
 */
async function createSession(ctx: RequestContext): Promise<Response> {
  const runId = ctx.params.id!;
  const input = await parseBody(ctx.request, createSessionInput);
  // The tiers are what admins set: only they may pick another for one session.
  if (input.tier || input.harness) await requireOrgAdmin(ctx);
  const { organizationId } = ctx.principal;

  // organizations is not tenant-scoped, so the org defaults are read outside
  // the tenant transaction and passed in.
  const orgRows = await withoutTenant(async ({ sql }) => {
    return (await sql`
      SELECT default_agent_models AS "defaultAgentModels" FROM organizations
      WHERE id = ${organizationId}`) as Array<{ defaultAgentModels: AgentModels }>;
  });
  const defaultAgentModels = orgRows[0]?.defaultAgentModels ?? {};

  const result = await withOrg(organizationId, async (scope) => {
    const runs = (await scope.sql`
      SELECT r.id, r.project_id, p.agent_models AS "agentModels"
      FROM runs r JOIN projects p ON p.id = r.project_id
      WHERE r.id = ${runId} LIMIT 1`) as Array<{
      id: string;
      project_id: string;
      agentModels: AgentModels;
    }>;
    const run = runs[0];
    if (!run) return { missing: true as const };

    // Read for its harness only: the model comes from the tier below.
    const resolved = resolveAgentModel(
      input.role,
      { agentModels: run.agentModels ?? {} },
      { defaultAgentModels },
    );
    const tiers = await listTiers(scope);
    const tierId = input.tier ?? resolveTier(input.role, { project: run.agentModels, organization: defaultAgentModels }, tiers).tierId;
    const tier = tiers.find((t) => t.id === tierId);
    if (input.tier && !tier) throw badRequest(`there is no model tier ${input.tier}`);
    if (!tier) return { unconfigured: `the ${input.role} names no model tier; set one in Agents` };
    if (!tier.model) return { unconfigured: `the ${input.role} runs on ${tier.name}, which names no model yet. An admin sets it in Models.` };
    const model = tier.model;

    const harness = input.harness ?? resolved?.harness ?? DEFAULT_HARNESS;
    const sessionId = newId("session");

    const rows = (await scope.sql`
      INSERT INTO sessions (id, organization_id, run_id, parent_session_id, role, harness, model, status)
      VALUES (${sessionId}, ${organizationId}, ${runId}, ${input.parentSessionId},
              ${input.role}, ${harness}, ${model}, 'pending')
      RETURNING ${scope.sql.unsafe(SESSION_SELECT)}`) as Array<Record<string, unknown>>;

    const event = await appendInScope(scope, {
      eventType: input.parentSessionId ? EventTypes.SubagentStarted : EventTypes.SessionStarted,
      organizationId,
      projectId: run.project_id,
      runId,
      sessionId,
      actor: { type: "agent", id: sessionId },
      source: "control-plane",
      correlationId: runId,
      payload: { role: input.role, model, tier: tier.name, harness, parentSessionId: input.parentSessionId },
    });

    return { session: rows[0]!, event };
  });

  if ("missing" in result) throw notFound(`run ${runId} not found`);
  if ("unconfigured" in result) throw badRequest(result.unconfigured);
  return json(result.session, 201);
}

async function getSession(ctx: RequestContext): Promise<Response> {
  const id = ctx.params.id!;
  const session = await withOrg(ctx.principal.organizationId, async (scope) => {
    const rows = (await scope.sql`
      SELECT ${scope.sql.unsafe(SESSION_SELECT)} FROM sessions WHERE id = ${id}`) as Array<
      Record<string, unknown>
    >;
    if (!rows[0]) return null;
    const children = await scope.sql`
      SELECT ${scope.sql.unsafe(SESSION_SELECT)} FROM sessions WHERE parent_session_id = ${id}
      ORDER BY created_at ASC`;
    return { ...rows[0], children };
  });

  if (!session) throw notFound(`session ${id} not found`);
  return json(session);
}

export function registerWorkRoutes(router: Router): void {
  router.post("/v1/tasks", createTask);
  router.get("/v1/tasks", listTasks);
  router.get("/v1/tasks/:id", getTask);
  router.post("/v1/tasks/:id/runs", createRun);
  router.post("/v1/tasks/:id/deliver", deliverTask);
  router.post("/v1/tasks/:id/done", markTaskDone);
  router.post("/v1/tasks/:id/decide", decideTask);

  router.get("/v1/runs/:id", getRun);
  router.post("/v1/runs/:id/sessions", createSession);

  router.get("/v1/sessions/:id", getSession);
}
