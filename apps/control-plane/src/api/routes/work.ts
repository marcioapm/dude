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
  BUILDER_OFFLINE_SECONDS, EventTypes, TASK_GOAL_TOO_SHORT, TASK_GOAL_TOO_SHORT_DETAILS, agentRoleSchema, blockedRefusal, newId,
  resolveAgentModel, resolveTier,
  taskCriteriaInput, taskGoalInput, taskGoalShortBy, taskSourceInputSchema,
} from "@dude/domain";
import type { AgentModels, BlockerRef } from "@dude/domain";
import { withOrg, withoutTenant, type OrgScope } from "../../db/client.ts";
import { appendInScope } from "../../events/ledger.ts";
import { HttpError, badRequest, conflict, json, notFound, parseBody } from "../http.ts";
import { orchestrator } from "../../orchestrator/client.ts";
import { requireOrgAdmin } from "../access.ts";
import { listTiers } from "./models.ts";
import { REPOSITORIES_JSON, setTaskRepositories, taskRepositoriesInput } from "./taskRepositories.ts";
import { SOURCE_JSON, checkTaskSource, setTaskSource, taskSourceInput } from "./taskSource.ts";
import { ownerJson, peopleJson } from "./people.ts";
import { syncTaskAttachments } from "./attachments.ts";
import { agentMachines } from "./machines.ts";
import { answers as answersInput, text as messageInput } from "./sessions.ts";
import type { RequestContext, Router } from "../router.ts";

/**
 * Who takes the task's delivery decisions, from its latest delivery's state
 * (the orchestrator's State.Decider): "policy" (Deliver) or "conductor";
 * "policy" for a task with no delivery. With it, the decision the delivery
 * waits on for the conductor, `{point, actions, phases}`, or null.
 */
const DECIDING_JSON = `
  COALESCE((SELECT w.state->>'decider' FROM workflow_runs w WHERE w.task_id = tasks.id
    ORDER BY w.created_at DESC LIMIT 1), 'policy') AS decider,
  COALESCE((SELECT (w.state->>'handedBack')::boolean FROM workflow_runs w WHERE w.task_id = tasks.id
    ORDER BY w.created_at DESC LIMIT 1), false) AS "handedBack",
  (SELECT json_build_object('point', w.state->'decision'->>'point')
   FROM workflow_runs w WHERE w.task_id = tasks.id AND w.status IN ('running', 'waiting')
     AND w.step = 'conductorDecision' AND w.state ? 'decision' AND NOT w.state->'decision' ? 'taken'
   ORDER BY w.created_at DESC LIMIT 1) AS "awaitingDecision"`;

const TASK_SELECT = `
  id, organization_id AS "organizationId", project_id AS "projectId", epic_id AS "epicId", ${REPOSITORIES_JSON}, ${SOURCE_JSON},
  title, goal, acceptance_criteria AS "acceptanceCriteria", status,
  (SELECT key_prefix FROM projects p WHERE p.id = tasks.project_id) || '-' || number AS key, -- see navigation.ts
  requested_by AS "requestedBy", ${ownerJson()}, ${peopleJson()}, ${DECIDING_JSON}, created_at AS "createdAt", updated_at AS "updatedAt"`;

// A Run's columns, as a fragment of `sql`: the builder's offline threshold
// is a parameter.
const runSelect = (sql: OrgScope["sql"]) => sql`
  id, organization_id AS "organizationId", project_id AS "projectId",
  task_id AS "taskId", attempt, status, error, kind,
  phase, role, category, parent_run_id AS "parentRunId", conductor_run_id AS "conductorRunId", base_refs AS "baseRefs",
  (SELECT COALESCE(json_object_agg(k, v->>'sha'), '{}'::json) FROM jsonb_each(heads) AS h(k, v)) AS heads,
  branch, harness, model, model_tier AS "modelTier", effort, dude_pause AS "dudePause", machine, image,
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
  created_at AS "createdAt", started_at AS "startedAt", ended_at AS "endedAt",
  -- While it makes no progress (run_stalled, migration 085): the latest
  -- report, in the words its reader was given.
  CASE WHEN run_stalled(runs) THEN (SELECT json_build_object('at', e.occurred_at,
      'text', COALESCE(e.payload->>'text', ''), 'owner', NOT COALESCE((e.payload->>'conducted')::boolean, false),
      'left', runs.stall_left_at IS NOT NULL)
    FROM events e WHERE e.run_id = runs.id AND e.event_type = 'run.stalled' ORDER BY e.cursor DESC LIMIT 1) END AS stalled,
  replaced_by AS "replacedBy",
  -- Where its start is (run.stage, the ledger's; no column holds it), while
  -- it is live and lux has not reported it running. Its twin over runs r is
  -- the orchestrator's phases.StartStageJSON (stage.go): change both.
  (SELECT json_build_object('stage', e.payload->>'stage', 'since', e.payload->>'since', 'epoch', (e.payload->>'epoch')::int)
   FROM events e WHERE e.run_id = runs.id AND e.event_type = 'run.stage'
     AND runs.status IN ('scheduled', 'starting', 'running') AND COALESCE(runs.lux_state, '') <> 'running'
   ORDER BY e.cursor DESC LIMIT 1) AS "startStage",
  can_run_containers AS "canRunContainers"`;

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
  // Resume only while the Run that failed is still kept to resume
  // (run_kept, migration 070): after that, the other ways are left.
  return `(SELECT json_build_object('reason', e.payload->>'reason', 'detail', e.payload->'detail',
    'actions', COALESCE(e.payload->'actions', '["stop"]'::jsonb) - CASE WHEN EXISTS (SELECT 1 FROM runs
        WHERE runs.id = e.payload->'detail'->>'runId' AND run_kept(runs)) THEN '' ELSE 'resume' END,
    'at', e.occurred_at)
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
  /** Existing work it starts from (a pull request or a branch); omitted or null for new work. */
  source: taskSourceInput.optional(),
});

async function createTask(ctx: RequestContext): Promise<Response> {
  const input = await parseBody(ctx.request, createTaskInput);
  if (taskGoalShortBy(input.goal ?? "") > 0) throw badRequest(TASK_GOAL_TOO_SHORT, TASK_GOAL_TOO_SHORT_DETAILS);
  const { organizationId } = ctx.principal;
  // Asked of GitHub before the transaction: no row is held while it answers.
  const source = input.source ? await checkTaskSource(ctx, input.projectId, input.source, null) : null;

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
    await setTaskSource(scope, organizationId, taskId, source);
    await syncTaskAttachments(scope, taskId, input.goal ?? "", input.acceptanceCriteria ?? []);
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

/**
 * Look a source up before a task names it: the pull request's title, head
 * and base, or the branch and its base — or why dude will not adopt it.
 * The task dialog's lookup; writes nothing. `taskId` names the task it is
 * for, when it exists.
 */
async function checkSource(ctx: RequestContext): Promise<Response> {
  const input = await parseBody(ctx.request, z.object({ source: taskSourceInputSchema, taskId: z.string().min(1).optional() }));
  return json(await checkTaskSource(ctx, ctx.params.id!, input.source, input.taskId ?? null));
}

const deliverInput = z.object({
  /** Overrides for this task only; unset fields keep the default. */
  policy: z.record(z.string(), z.unknown()).optional(),
  /** Start a blocked task anyway: a person only (the orchestrator checks), recorded with the note. */
  override: z.literal(true).optional(),
  note: z.string().max(2000).optional(),
}).strict();

const talkInput = deliverInput.omit({ policy: true });

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

// Words; or, through the conductor's question's form, the answers to it,
// naming the question.
const chatInput = z.union([messageInput, z.object({ questionId: z.string().min(1), ...answersInput.shape }).strict()]);

/**
 * A message in a task's Chat: it starts the task's conductor, or is its
 * next input (the orchestrator decides which, and answers which Run).
 */
async function chatTask(ctx: RequestContext): Promise<Response> {
  const input = await parseBody(ctx.request, chatInput);
  return orchestrator(ctx.principal.organizationId, "POST", `/internal/tasks/${ctx.params.id}/chat`,
    JSON.stringify(input), ctx.principal);
}

/** Try again on a conductor that failed: a fresh one takes what it never read. */
async function retryConductor(ctx: RequestContext): Promise<Response> {
  return orchestrator(ctx.principal.organizationId, "POST", `/internal/tasks/${ctx.params.id}/retry`, "{}", ctx.principal);
}

/**
 * Talk it through: a task not started gets its conductor, and a delivery it
 * decides, waiting on its first decision. No implementer starts until the
 * conductor says so.
 */
async function talkTask(ctx: RequestContext): Promise<Response> {
  // Images are the task text's to name: anything else in the body is dropped, as before.
  const raw = (await ctx.request.json().catch(() => ({}))) as Record<string, unknown>;
  const input = talkInput.safeParse({ override: raw.override, note: raw.note });
  if (!input.success) throw badRequest("request body failed validation", input.error.flatten());
  return orchestrator(ctx.principal.organizationId, "POST", `/internal/tasks/${ctx.params.id}/talk`,
    JSON.stringify(input.data), ctx.principal);
}

const deciderInput = z.object({ decider: z.enum(["policy", "conductor"]), openPullRequest: z.boolean().optional() }).strict();

/**
 * Who takes the delivery's decisions from the next one on: "policy" is Let
 * Deliver finish it; "conductor" gives them back to the conductor.
 */
async function setDecider(ctx: RequestContext): Promise<Response> {
  const input = await parseBody(ctx.request, deciderInput);
  return orchestrator(ctx.principal.organizationId, "POST", `/internal/tasks/${ctx.params.id}/decider`,
    JSON.stringify(input), ctx.principal);
}

/** How a stopped task can be picked back up, and until when a resume can. */
async function recoveryOptions(ctx: RequestContext): Promise<Response> {
  return orchestrator(ctx.principal.organizationId, "GET", `/internal/tasks/${ctx.params.id}/recover`, undefined, ctx.principal);
}

/** Pick a stopped task back up: resume, retry or restart, with a note for the agents. */
async function recoverTask(ctx: RequestContext): Promise<Response> {
  return orchestrator(ctx.principal.organizationId, "POST", `/internal/tasks/${ctx.params.id}/recover`,
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
  return json({ ...task, agentMachines: await agentMachines(ctx, { taskId: id }) });
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

    // Path 8: a bare Run is a start, refused while the task waits on unmerged
    // work. The rule is task_blocked_by's (migration 111), the one the
    // orchestrator's RefuseBlockedTx reads; the row is locked as it does.
    await scope.sql`SELECT 1 FROM tasks WHERE id = ${taskId} FOR NO KEY UPDATE`;
    const blockers = (await scope.sql`SELECT key, status FROM task_blocked_by(${taskId})`) as BlockerRef[];
    if (blockers.length > 0) return { blocked: blockers };

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
  if ("blocked" in result) throw await refusedStart(ctx, taskId, "create_run", result.blocked);
  return json(result.run, 201);
}

/**
 * A start refused because the task waits on unmerged work: recorded
 * (task.start_refused, never shown as a line), and answered as the
 * orchestrator answers it — 409 `blocked`, the same words, the blockers.
 */
export async function refusedStart(ctx: RequestContext, taskId: string, path: string, blockers: BlockerRef[]): Promise<HttpError> {
  const { organizationId } = ctx.principal;
  const key = await withOrg(organizationId, async (scope) => {
    const rows = (await scope.sql`
      SELECT t.project_id AS "projectId", p.key_prefix || '-' || t.number AS key
      FROM tasks t JOIN projects p ON p.id = t.project_id WHERE t.id = ${taskId}`) as Array<{ projectId: string; key: string }>;
    const actor = auditActor(ctx.principal);
    await appendInScope(scope, {
      eventType: EventTypes.TaskStartRefused, organizationId, projectId: rows[0]!.projectId, taskId,
      actor: { type: actor.kind, id: actor.id }, source: "control-plane", correlationId: taskId,
      payload: { path, blockers },
    });
    return rows[0]!.key;
  });
  return new HttpError(409, blockedRefusal(key, blockers), "blocked", { blockers });
}

async function getRun(ctx: RequestContext): Promise<Response> {
  const id = ctx.params.id!;
  const run = await withOrg(ctx.principal.organizationId, async (scope) => {
    // A brainstorm session's Run is its accepted members' alone.
    const rows = (await scope.sql`
      SELECT ${runSelect(scope.sql)} FROM runs
      WHERE id = ${id} AND (session_id IS NULL OR session_role(session_id, ${ctx.principal.personId}) IS NOT NULL)`) as Array<Record<string, unknown>>;
    if (!rows[0]) return null;
    const sessions = await scope.sql`
      SELECT ${scope.sql.unsafe(SESSION_SELECT)} FROM agent_sessions WHERE run_id = ${id}
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
      INSERT INTO agent_sessions (id, organization_id, run_id, parent_session_id, role, harness, model, status)
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

/** An agent's session inside a Run; one inside a brainstorm session's Run, for that session's accepted members only. */
async function getSession(ctx: RequestContext): Promise<Response> {
  const id = ctx.params.id!;
  const session = await withOrg(ctx.principal.organizationId, async (scope) => {
    const rows = (await scope.sql`
      SELECT ${scope.sql.unsafe(SESSION_SELECT)} FROM agent_sessions s WHERE id = ${id}
        AND NOT EXISTS (SELECT 1 FROM runs r WHERE r.id = s.run_id AND r.session_id IS NOT NULL
                        AND session_role(r.session_id, ${ctx.principal.personId}) IS NULL)`) as Array<
      Record<string, unknown>
    >;
    if (!rows[0]) return null;
    const children = await scope.sql`
      SELECT ${scope.sql.unsafe(SESSION_SELECT)} FROM agent_sessions WHERE parent_session_id = ${id}
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
  router.post("/v1/tasks/:id/chat", chatTask);
  router.post("/v1/tasks/:id/retry", retryConductor);
  router.post("/v1/tasks/:id/talk", talkTask);
  router.post("/v1/tasks/:id/decider", setDecider);
  router.get("/v1/tasks/:id/recover", recoveryOptions);
  router.post("/v1/tasks/:id/recover", recoverTask);
  router.post("/v1/projects/:id/sources/check", checkSource);

  router.get("/v1/runs/:id", getRun);
  router.post("/v1/runs/:id/sessions", createSession);

  router.get("/v1/sessions/:id", getSession);
}
