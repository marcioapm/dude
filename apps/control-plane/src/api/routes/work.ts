/**
 * Work item, run and session routes.
 *
 * A Work Item is the user-facing unit of requested work. A Run is one
 * execution attempt of it — retrying never erases a prior attempt, so cost,
 * duration and failure remain inspectable per attempt (plan §39).
 */

import { z } from "zod";
import { EventTypes, agentRoleSchema, newId, resolveAgentModel } from "@dude/domain";
import type { AgentModels } from "@dude/domain";
import { withOrg, withoutTenant } from "../../db/client.ts";
import { appendInScope } from "../../events/ledger.ts";
import { badRequest, conflict, json, notFound, parseBody } from "../http.ts";
import { orchestrator } from "../../orchestrator/client.ts";
import type { RequestContext, Router } from "../router.ts";

const WORK_ITEM_SELECT = `
  id, organization_id AS "organizationId", project_id AS "projectId", epic_id AS "epicId", repository_id AS "repositoryId",
  title, goal, acceptance_criteria AS "acceptanceCriteria", status,
  requested_by AS "requestedBy", created_at AS "createdAt", updated_at AS "updatedAt"`;

const RUN_SELECT = `
  id, organization_id AS "organizationId", project_id AS "projectId",
  work_item_id AS "workItemId", attempt, status, error,
  phase, role, category, parent_run_id AS "parentRunId", base_ref AS "baseRef",
  head_sha AS "headSha", branch, harness, model,
  json_build_object('input', input_tokens, 'output', output_tokens, 'cacheRead', cache_read_tokens,
    'cacheWrite', cache_write_tokens, 'context', context_tokens) AS tokens,
  created_at AS "createdAt", started_at AS "startedAt", ended_at AS "endedAt"`;

const SESSION_SELECT = `
  id, organization_id AS "organizationId", run_id AS "runId",
  parent_session_id AS "parentSessionId", role, harness, model, status,
  external_session_id AS "externalSessionId",
  created_at AS "createdAt", ended_at AS "endedAt"`;

// ---------------------------------------------------------------------------
// Work items
// ---------------------------------------------------------------------------

const createWorkItemInput = z.object({
  projectId: z.string().min(1),
  epicId: z.string().min(1).nullable().default(null),
  /** The repository it changes; the project's only one when omitted. */
  repositoryId: z.string().min(1).nullable().default(null),
  title: z.string().min(1).max(500),
  goal: z.string().max(10_000).default(""),
  acceptanceCriteria: z.array(z.string().max(2000)).default([]),
});

async function createWorkItem(ctx: RequestContext): Promise<Response> {
  const input = await parseBody(ctx.request, createWorkItemInput);
  const { organizationId } = ctx.principal;

  const result = await withOrg(organizationId, async (scope) => {
    // RLS already confines this to the caller's org, so a miss means the
    // project does not exist *for them* — which is the correct 404 either way.
    const project = await scope.sql`SELECT id FROM projects WHERE id = ${input.projectId} LIMIT 1`;
    if (project.length === 0) return { missingProject: true as const };
    if (input.repositoryId) {
      const repo = await scope.sql`SELECT 1 FROM repositories WHERE id = ${input.repositoryId} AND project_id = ${input.projectId}`;
      if (repo.length === 0) return { missingRepository: true as const };
    }

    const workItemId = newId("workItem");
    const rows = (await scope.sql`
      INSERT INTO work_items (id, organization_id, project_id, epic_id, repository_id, title, goal,
                              acceptance_criteria, status)
      VALUES (${workItemId}, ${organizationId}, ${input.projectId}, ${input.epicId}, ${input.repositoryId},
              ${input.title}, ${input.goal},
              ${input.acceptanceCriteria ?? []}::jsonb, 'received')
      RETURNING ${scope.sql.unsafe(WORK_ITEM_SELECT)}`) as Array<Record<string, unknown>>;

    const event = await appendInScope(scope, {
      eventType: EventTypes.WorkItemCreated,
      organizationId,
      projectId: input.projectId,
      workItemId,
      actor: { type: "human", id: ctx.principal.apiKeyId },
      source: "control-plane",
      correlationId: workItemId,
      payload: { title: input.title, goal: input.goal },
    });

    return { workItem: rows[0]!, event };
  });

  if ("missingProject" in result) throw notFound(`project ${input.projectId} not found`);
  if ("missingRepository" in result) throw notFound(`repository ${input.repositoryId} is not in this project`);
  return json(result.workItem, 201);
}

const deliverInput = z.object({
  repositoryId: z.string().min(1).optional(),
  /** Overrides for this work item only; unset fields keep the default. */
  policy: z.record(z.string(), z.unknown()).optional(),
});

/**
 * Start the delivery workflow for a work item.
 *
 * The orchestrator runs it; this authenticates the user and forwards. The
 * work item is the idempotency key there, so a second call joins the
 * delivery already in flight.
 */
async function deliverWorkItem(ctx: RequestContext): Promise<Response> {
  const input = await parseBody(ctx.request, deliverInput);
  return orchestrator(ctx.principal.organizationId, "POST", `/internal/work-items/${ctx.params.id}/deliver`,
    JSON.stringify(input), ctx.principal.apiKeyId);
}

async function listWorkItems(ctx: RequestContext): Promise<Response> {
  const projectId = ctx.url.searchParams.get("projectId");
  const status = ctx.url.searchParams.get("status");

  const workItems = await withOrg(ctx.principal.organizationId, async (scope) => {
    return (await scope.sql`
      SELECT ${scope.sql.unsafe(WORK_ITEM_SELECT)} FROM work_items
      WHERE (${projectId}::text IS NULL OR project_id = ${projectId})
        AND (${status}::text IS NULL OR status::text = ${status})
      ORDER BY created_at DESC
      LIMIT 200`) as Array<Record<string, unknown>>;
  });
  return json({ workItems });
}

async function getWorkItem(ctx: RequestContext): Promise<Response> {
  const id = ctx.params.id!;
  const workItem = await withOrg(ctx.principal.organizationId, async (scope) => {
    const rows = (await scope.sql`
      SELECT ${scope.sql.unsafe(WORK_ITEM_SELECT)} FROM work_items WHERE id = ${id}`) as Array<
      Record<string, unknown>
    >;
    if (!rows[0]) return null;
    const runs = await scope.sql`
      SELECT ${scope.sql.unsafe(RUN_SELECT)} FROM runs WHERE work_item_id = ${id}
      ORDER BY attempt DESC`;
    return { ...rows[0], runs };
  });

  if (!workItem) throw notFound(`work item ${id} not found`);
  return json(workItem);
}

// ---------------------------------------------------------------------------
// Runs
// ---------------------------------------------------------------------------

/**
 * Create a Run for a Work Item.
 *
 * The attempt number is derived inside the transaction, and `runs` has a
 * UNIQUE(work_item_id, attempt) constraint, so two concurrent creates cannot
 * both claim the same attempt — the loser gets a 409 rather than a duplicate.
 */
async function createRun(ctx: RequestContext): Promise<Response> {
  const workItemId = ctx.params.id!;
  const { organizationId } = ctx.principal;

  const result = await withOrg(organizationId, async (scope) => {
    const workItems = (await scope.sql`
      SELECT id, project_id FROM work_items WHERE id = ${workItemId} LIMIT 1`) as Array<{
      id: string;
      project_id: string;
    }>;
    const workItem = workItems[0];
    if (!workItem) return { missing: true as const };

    const attemptRows = (await scope.sql`
      SELECT COALESCE(MAX(attempt), 0) + 1 AS attempt FROM runs WHERE work_item_id = ${workItemId}`) as Array<{
      attempt: number;
    }>;
    const attempt = Number(attemptRows[0]?.attempt ?? 1);

    const runId = newId("run");
    const rows = (await scope.sql`
      INSERT INTO runs (id, organization_id, project_id, work_item_id, attempt, status)
      VALUES (${runId}, ${organizationId}, ${workItem.project_id}, ${workItemId}, ${attempt}, 'pending')
      RETURNING ${scope.sql.unsafe(RUN_SELECT)}`) as Array<Record<string, unknown>>;

    await scope.sql`UPDATE work_items SET status = 'queued' WHERE id = ${workItemId}`;

    const event = await appendInScope(scope, {
      eventType: EventTypes.RunCreated,
      organizationId,
      projectId: workItem.project_id,
      workItemId,
      runId,
      actor: { type: "human", id: ctx.principal.apiKeyId },
      source: "control-plane",
      correlationId: workItemId,
      payload: { attempt },
    });

    return { run: rows[0]!, event };
  }).catch((err: unknown) => {
    if (err instanceof Error && err.message.includes("runs_work_item_id_attempt_key")) {
      return { raced: true as const };
    }
    throw err;
  });

  if ("missing" in result) throw notFound(`work item ${workItemId} not found`);
  if ("raced" in result) throw conflict("a run for this attempt was created concurrently; retry");
  return json(result.run, 201);
}

async function getRun(ctx: RequestContext): Promise<Response> {
  const id = ctx.params.id!;
  const run = await withOrg(ctx.principal.organizationId, async (scope) => {
    const rows = (await scope.sql`
      SELECT ${scope.sql.unsafe(RUN_SELECT)} FROM runs WHERE id = ${id}`) as Array<Record<string, unknown>>;
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
  /** Overrides the resolved project/org model when set. */
  model: z.string().min(1).optional(),
  harness: z.string().min(1).optional(),
});

const DEFAULT_HARNESS = "opencode";

/**
 * Create a Session under a Run, resolving which model to use from the
 * project's per-role configuration, then the organization default.
 */
async function createSession(ctx: RequestContext): Promise<Response> {
  const runId = ctx.params.id!;
  const input = await parseBody(ctx.request, createSessionInput);
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

    const resolved = resolveAgentModel(
      input.role,
      { agentModels: run.agentModels ?? {} },
      { defaultAgentModels },
    );
    const model = input.model ?? resolved?.model;
    if (!model) return { unconfigured: true as const };

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
      payload: { role: input.role, model, harness, parentSessionId: input.parentSessionId },
    });

    return { session: rows[0]!, event };
  });

  if ("missing" in result) throw notFound(`run ${runId} not found`);
  if ("unconfigured" in result) {
    throw badRequest(
      `no model configured for role "${input.role}"; set it on the project or organization, ` +
        `or pass an explicit model`,
    );
  }
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
  router.post("/v1/work-items", createWorkItem);
  router.get("/v1/work-items", listWorkItems);
  router.get("/v1/work-items/:id", getWorkItem);
  router.post("/v1/work-items/:id/runs", createRun);
  router.post("/v1/work-items/:id/deliver", deliverWorkItem);

  router.get("/v1/runs/:id", getRun);
  router.post("/v1/runs/:id/sessions", createSession);

  router.get("/v1/sessions/:id", getSession);
}
