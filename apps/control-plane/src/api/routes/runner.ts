/**
 * Runner protocol — the control plane's side of the worker contract.
 *
 * The division of responsibility (plan §51, §69): the control plane decides
 * *what* should happen and *whether it is allowed*; the runner makes it real
 * on a node. The runner owns no workflow or business policy.
 *
 * These endpoints require a runner-kind API key. A user key cannot lease work,
 * and a runner key cannot reach the product API.
 */

import { z } from "zod";
import { EventTypes, newId, resolveAgentModel } from "@dude/domain";
import type { AgentModels } from "@dude/domain";
import { withOrg, withoutTenant, type OrgScope } from "../../db/client.ts";
import { promptFor } from "../prompts.ts";
import { PHASE_PUBLISHES, ROLE_FOR_PHASE } from "../../workflow/policy.ts";
import { appendInScope } from "../../events/ledger.ts";
import { eventBus } from "../../events/bus.ts";
import { badRequest, json, notFound, parseBody } from "../http.ts";
import type { RequestContext, Router } from "../router.ts";

/** How long a lease is valid before the control plane may reclaim the Run. */
const LEASE_SECONDS = 90;

/**
 * The role a claimed Run executes as.
 *
 * One orchestrator per Run for now (plan §9.2); specialist roles are spawned
 * as subagents rather than claimed independently.
 */
const DEFAULT_RUN_ROLE = "orchestrator" as const;

const registerInput = z.object({
  name: z.string().min(1).max(200),
  pool: z.string().min(1).max(100).default("local"),
  cpuMillis: z.number().int().nonnegative().default(0),
  memoryMb: z.number().int().nonnegative().default(0),
  maxRuns: z.number().int().positive().default(1),
  // Nullable as well as optional: a client with nothing cached may send null
  // rather than omitting the field, and that should not fail registration.
  cachedImages: z.array(z.string()).nullish().transform((v) => v ?? []),
  cachedRepositories: z.array(z.string()).nullish().transform((v) => v ?? []),
});

const heartbeatInput = z.object({
  activeRuns: z.number().int().nonnegative().default(0),
  cachedImages: z.array(z.string()).nullish(),
  cachedRepositories: z.array(z.string()).nullish(),
  status: z.enum(["ready", "draining"]).default("ready"),
});

const claimInput = z.object({
  /** Upper bound on Runs to take in this poll. */
  limit: z.number().int().positive().max(10).default(1),
});

const runUpdateInput = z.object({
  status: z.enum(["starting", "running", "completed", "failed", "aborted"]),
  error: z.string().max(10_000).nullable().default(null),
  workspacePath: z.string().nullable().default(null),
  /**
   * Whether this Run's workspace could be rebuilt elsewhere without loss.
   *
   * The runner knows, because it can see whether the working tree is dirty.
   * False pins the Run to this node until the work is committed.
   */
  workspacePortable: z.boolean().nullish(),
});

const runtimeInput = z.object({
  containerId: z.string().nullable().default(null),
  imageDigest: z.string().nullable().default(null),
  status: z.enum(["creating", "starting", "running", "stopping", "stopped", "destroyed", "failed"]),
});

/**
 * Register a worker, or re-register one that restarted.
 *
 * Re-registration is keyed on (organization, name) so a runner that restarts
 * reclaims its identity — and with it, its repository cache affinity — rather
 * than leaking a new worker row on every restart.
 */
async function register(ctx: RequestContext): Promise<Response> {
  const input = await parseBody(ctx.request, registerInput);
  const { organizationId } = ctx.principal;

  const worker = await withoutTenant(async ({ sql }) => {
    const rows = (await sql`
      INSERT INTO workers (id, organization_id, name, pool, status, cpu_millis, memory_mb,
                           max_runs, cached_images, cached_repositories, last_heartbeat_at)
      VALUES (${newId("worker")}, ${organizationId}, ${input.name}, ${input.pool}, 'ready',
              ${input.cpuMillis}, ${input.memoryMb}, ${input.maxRuns},
              ${input.cachedImages}::jsonb, ${input.cachedRepositories}::jsonb, now())
      ON CONFLICT (organization_id, name) WHERE organization_id IS NOT NULL DO UPDATE SET
        pool = EXCLUDED.pool,
        status = 'ready',
        cpu_millis = EXCLUDED.cpu_millis,
        memory_mb = EXCLUDED.memory_mb,
        max_runs = EXCLUDED.max_runs,
        cached_images = EXCLUDED.cached_images,
        cached_repositories = EXCLUDED.cached_repositories,
        last_heartbeat_at = now()
      RETURNING id, name, pool, status, max_runs AS "maxRuns", active_runs AS "activeRuns"`) as Array<
      Record<string, unknown>
    >;
    return rows[0]!;
  });

  const event = await withOrg(organizationId, (scope) =>
    appendInScope(scope, {
      eventType: EventTypes.WorkerRegistered,
      organizationId,
      actor: { type: "system", id: worker.id as string },
      source: "runner",
      payload: { name: input.name, pool: input.pool },
    }),
  );
  eventBus.publish(event);

  return json({ ...worker, leaseSeconds: LEASE_SECONDS }, 201);
}

/**
 * Heartbeat.
 *
 * Also the runner's opportunity to report updated cache contents, which feeds
 * placement affinity: a node that already mirrors a repository should be
 * preferred for Runs touching it (plan §61).
 */
async function heartbeat(ctx: RequestContext): Promise<Response> {
  const input = await parseBody(ctx.request, heartbeatInput);
  const workerId = ctx.params.id!;

  const updated = await withoutTenant(async ({ sql }) => {
    const rows = (await sql`
      UPDATE workers SET
        last_heartbeat_at = now(),
        active_runs = ${input.activeRuns},
        status = ${input.status},
        cached_images = COALESCE(${input.cachedImages ?? null}::jsonb, cached_images),
        cached_repositories = COALESCE(${input.cachedRepositories ?? null}::jsonb, cached_repositories)
      WHERE id = ${workerId} AND organization_id = ${ctx.principal.organizationId}
      RETURNING id`) as Array<{ id: string }>;
    return rows[0];
  });

  if (!updated) throw notFound(`worker ${workerId} not found`);
  return json({ ok: true, leaseSeconds: LEASE_SECONDS });
}

/**
 * Claim pending Runs for a worker.
 *
 * `FOR UPDATE SKIP LOCKED` means several runners can poll concurrently
 * without blocking or double-claiming. The lease expiry is what lets the
 * control plane recover work from a worker that disappears (plan §32).
 */
async function claimRuns(ctx: RequestContext): Promise<Response> {
  const input = await parseBody(ctx.request, claimInput);
  const workerId = ctx.params.id!;
  const { organizationId } = ctx.principal;

  // organizations is not tenant-scoped, so the org-level model defaults are
  // read once here and applied to every claimed Run below.
  const orgRows = await withoutTenant(async ({ sql }) => {
    return (await sql`
      SELECT default_agent_models AS "defaultAgentModels" FROM organizations
      WHERE id = ${organizationId}`) as Array<{ defaultAgentModels: AgentModels }>;
  });
  const organizationDefaults = orgRows[0]?.defaultAgentModels;

  const claimed = await withOrg(organizationId, async (scope) => {
    // One query: claim the runs and join everything the runner needs to
    // execute them. Looping a per-run lookup here would be N+1 on the hot
    // path every runner polls.
    const runs = (await scope.sql`
      WITH candidate AS (
        SELECT id FROM runs
        WHERE status = 'pending'
          AND (
            -- Unstarted work: any worker may take it.
            home_worker_id IS NULL
            -- Already ours: reclaim it, workspace and all.
            OR home_worker_id = ${workerId}
            -- Someone else's workspace, but rebuildable from the mirror
            -- without losing anything.
            OR workspace_portable
          )
        ORDER BY created_at
        LIMIT ${input.limit}
        FOR UPDATE SKIP LOCKED
      ), claimed AS (
        UPDATE runs r
        SET status = 'scheduled',
            worker_id = ${workerId},
            -- Claiming a portable Run moves its home: the workspace is about
            -- to be materialized here.
            home_worker_id = ${workerId},
            lease_expires_at = now() + ${`${LEASE_SECONDS} seconds`}::interval
        FROM candidate
        WHERE r.id = candidate.id
        RETURNING r.id, r.work_item_id, r.project_id, r.attempt, r.phase, r.base_ref
      )
      SELECT
        c.id,
        c.work_item_id AS "workItemId",
        c.project_id   AS "projectId",
        c.attempt,
        c.phase,
        c.base_ref      AS "baseRef",
        p.runtime_image AS "runtimeImage",
        p.agent_models  AS "agentModels",
        -- The task the agent is given, composed from the Work Item.
        w.title, w.goal, w.acceptance_criteria AS "acceptanceCriteria",
        COALESCE(
          (SELECT jsonb_agg(jsonb_build_object(
                    'name', repo.name, 'url', repo.url,
                    'defaultBranch', repo.default_branch, 'trust', repo.trust)
                  ORDER BY repo.name)
           FROM repositories repo WHERE repo.project_id = c.project_id),
          '[]'::jsonb
        ) AS repositories
      FROM claimed c
      JOIN projects p ON p.id = c.project_id
      JOIN work_items w ON w.id = c.work_item_id`) as Array<Record<string, unknown>>;

    const orgDefaults = organizationDefaults ?? {};
    const enriched = [];

    for (const run of runs) {
      /*
       * A Run created by the delivery workflow carries a phase; one created
       * directly through the API does not, and runs as an orchestrator.
       * Both have to work: a phase is how the workflow drives specialists,
       * not a requirement for executing a Run at all.
       */
      const phase = (run.phase as string | null) ?? null;
      const role = phase ? ROLE_FOR_PHASE[phase]! : DEFAULT_RUN_ROLE;

      // Resolve the model here rather than on the node: which model runs a
      // role is policy, and policy belongs to the control plane.
      const agentModels = (run.agentModels ?? {}) as AgentModels;
      const resolved = resolveAgentModel(role, { agentModels }, { defaultAgentModels: orgDefaults });

      // A fix Run needs to see what it is fixing, so the findings travel in
      // its prompt rather than as a second call the runner would have to
      // know to make.
      const findings =
        phase === "fix" ? await openFindingsFor(scope, run.workItemId as string) : undefined;

      enriched.push({
        id: run.id,
        workItemId: run.workItemId,
        projectId: run.projectId,
        attempt: run.attempt,
        repositories: run.repositories,
        runtimeImage: run.runtimeImage,
        phase,
        role,
        // Where this Run's workspace starts. Null means the repository's
        // default branch — the implement phase, or a Run with no phase.
        baseRef: run.baseRef ?? null,
        // Whether the runner pushes what this Run commits. A reviewer gets a
        // full sandbox and may run anything; it simply does not publish.
        publishes: phase ? PHASE_PUBLISHES[phase]! : true,
        model: resolved?.model ?? null,
        prompt: promptFor(phase ?? "implement", {
          title: String(run.title ?? ""),
          goal: String(run.goal ?? ""),
          acceptanceCriteria: (run.acceptanceCriteria ?? []) as string[],
          category: (run.category as string | null) ?? null,
          ...(findings ? { findings } : {}),
          context: resolved?.context ?? null,
        }),
      });

      const event = await appendInScope(scope, {
        eventType: EventTypes.RunLeaseAcquired,
        organizationId,
        projectId: run.projectId as string,
        workItemId: run.workItemId as string,
        runId: run.id as string,
        actor: { type: "system", id: workerId },
        source: "runner",
        correlationId: run.workItemId as string,
        payload: { workerId, leaseSeconds: LEASE_SECONDS },
      });
      eventBus.publish(event);
    }
    return enriched;
  });

  return json({ runs: claimed, leaseSeconds: LEASE_SECONDS });
}

/**
 * Compose the agent's task from the Work Item.
 *
 * Deliberately plain: the goal and acceptance criteria as the requester wrote
 * them. Role instructions live in the agent definition, not here, so this does
 * not quietly become a second place where behaviour is specified.
 */
/**
 * The open findings a fix Run has to address.
 *
 * Read inside the claim transaction so the prompt reflects the findings as
 * they were when the Run was claimed, rather than a set that could change
 * between the claim and the agent starting.
 */
async function openFindingsFor(scope: OrgScope, workItemId: string) {
  return (await scope.sql`
    SELECT severity, category, file, line, title, description,
           suggested_fix AS "suggestedFix"
    FROM review_findings
    WHERE work_item_id = ${workItemId} AND status = 'open'
    ORDER BY
      CASE severity
        WHEN 'blocking' THEN 1 WHEN 'high' THEN 2 WHEN 'medium' THEN 3
        WHEN 'low' THEN 4 ELSE 5
      END,
      created_at`) as Array<{
    severity: string;
    category: string;
    file: string | null;
    line: number | null;
    title: string;
    description: string;
    suggestedFix: string;
  }>;
}

/**
 * Renew the lease on a Run the worker is still executing.
 *
 * Doubles as the control channel. The runner already calls this on a timer,
 * so pending pause/abort requests and undelivered steering directives ride
 * back on the response rather than needing a second polling loop — and a
 * runner that has stopped renewing is one that would not have heard a
 * separate poll either.
 */
async function renewLease(ctx: RequestContext): Promise<Response> {
  const runId = ctx.params.id!;

  const result = await withOrg(ctx.principal.organizationId, async ({ sql }) => {
    const rows = (await sql`
      UPDATE runs
      SET lease_expires_at = now() + ${`${LEASE_SECONDS} seconds`}::interval
      WHERE id = ${runId} AND status IN ('scheduled', 'starting', 'running')
      RETURNING id, control::text AS control, control_reason AS "controlReason"`) as Array<{
      id: string;
      control: string;
      controlReason: string | null;
    }>;
    const run = rows[0];
    if (!run) return null;

    // Claim undelivered directives in the same round trip. Marking them
    // delivered here means the runner owns them: a directive handed over but
    // never applied is lost, which is why delivery is only marked once the
    // runner has it in hand.
    const directives = (await sql`
      UPDATE directives
      SET delivered_at = now()
      WHERE id IN (
        SELECT id FROM directives
        WHERE run_id = ${runId} AND delivered_at IS NULL
        ORDER BY created_at
      )
      RETURNING id, text, scope, created_at AS "createdAt"`) as Array<Record<string, unknown>>;

    return { run, directives };
  });

  if (!result) throw notFound(`run ${runId} is not leasable`);
  return json({
    ok: true,
    leaseSeconds: LEASE_SECONDS,
    control: result.run.control,
    controlReason: result.run.controlReason,
    directives: result.directives,
  });
}

/** Report Run progress or completion. */
async function updateRun(ctx: RequestContext): Promise<Response> {
  const input = await parseBody(ctx.request, runUpdateInput);
  const runId = ctx.params.id!;
  const { organizationId } = ctx.principal;
  const terminal = ["completed", "failed", "aborted"].includes(input.status);

  const result = await withOrg(organizationId, async (scope) => {
    const rows = (await scope.sql`
      UPDATE runs SET
        status = ${input.status}::run_status,
        error = COALESCE(${input.error}, error),
        workspace_path = COALESCE(${input.workspacePath}, workspace_path),
        workspace_portable = COALESCE(${input.workspacePortable ?? null}, workspace_portable),
        -- Stamped on the first transition out of scheduling, so elapsed time
        -- covers container startup rather than only the agent's own work.
        started_at = CASE WHEN ${input.status} IN ('starting', 'running') AND started_at IS NULL
                          THEN now() ELSE started_at END,
        ended_at = CASE WHEN ${terminal} THEN now() ELSE ended_at END,
        -- A finished Run no longer holds capacity.
        lease_expires_at = CASE WHEN ${terminal} THEN NULL ELSE lease_expires_at END
      WHERE id = ${runId}
      RETURNING id, work_item_id AS "workItemId", project_id AS "projectId", status`) as Array<
      Record<string, unknown>
    >;
    const run = rows[0];
    if (!run) return null;

    // 'starting' is progress, not a milestone worth its own ledger entry;
    // runtime.creating already records it.
    const eventType =
      input.status === "running"
        ? EventTypes.RunStarted
        : input.status === "completed"
          ? EventTypes.RunCompleted
          : input.status === "failed"
            ? EventTypes.RunFailed
            : input.status === "aborted"
              ? EventTypes.RunAborted
              : null;

    if (eventType === null) return { run, event: null };

    const event = await appendInScope(scope, {
      eventType,
      organizationId,
      projectId: run.projectId as string,
      workItemId: run.workItemId as string,
      runId: run.id as string,
      actor: { type: "system", id: ctx.principal.apiKeyId },
      source: "runner",
      correlationId: run.workItemId as string,
      payload: { status: input.status, error: input.error },
    });

    return { run, event };
  });

  if (!result) throw notFound(`run ${runId} not found`);
  if (result.event) eventBus.publish(result.event);
  return json(result.run);
}

/**
 * Record or update the runtime instance (container) for a Run.
 *
 * A container is disposable while the Run and its Sessions are not. A new
 * generation is created only when the runner reports a container that is not
 * the current one; every later status report for that same container updates
 * it in place. Without that distinction each lifecycle transition would mint
 * a new generation and leave the previous one stuck at its last status.
 */
async function upsertRuntime(ctx: RequestContext): Promise<Response> {
  const input = await parseBody(ctx.request, runtimeInput);
  const runId = ctx.params.id!;
  const { organizationId } = ctx.principal;

  const result = await withOrg(organizationId, async (scope) => {
    const runs = (await scope.sql`
      SELECT id, worker_id, project_id FROM runs WHERE id = ${runId}`) as Array<{
      id: string;
      worker_id: string | null;
      project_id: string;
    }>;
    const run = runs[0];
    if (!run) return null;
    if (!run.worker_id) return { unleased: true as const };

    const terminal = ["stopped", "destroyed", "failed"].includes(input.status);

    const current = (await scope.sql`
      SELECT id, generation, container_id FROM runtime_instances
      WHERE run_id = ${runId}
      ORDER BY generation DESC LIMIT 1`) as Array<{
      id: string;
      generation: number;
      container_id: string | null;
    }>;

    const latest = current[0];
    // Same container, or a report that does not name one: update in place.
    const isSameContainer =
      latest !== undefined &&
      (input.containerId === null ||
        latest.container_id === null ||
        latest.container_id === input.containerId);

    let runtime: Record<string, unknown>;
    if (isSameContainer) {
      const updated = (await scope.sql`
        UPDATE runtime_instances SET
          status = ${input.status}::runtime_status,
          container_id = COALESCE(${input.containerId}, container_id),
          image_digest = COALESCE(${input.imageDigest}, image_digest),
          started_at = CASE WHEN ${input.status} = 'running' AND started_at IS NULL
                            THEN now() ELSE started_at END,
          stopped_at = CASE WHEN ${terminal} AND stopped_at IS NULL THEN now() ELSE stopped_at END,
          destroyed_at = CASE WHEN ${input.status} = 'destroyed' AND destroyed_at IS NULL
                              THEN now() ELSE destroyed_at END
        WHERE id = ${latest.id}
        RETURNING id, generation, status`) as Array<Record<string, unknown>>;
      runtime = updated[0]!;
    } else {
      const inserted = (await scope.sql`
        INSERT INTO runtime_instances (
          id, organization_id, run_id, worker_id, container_id, image_digest, generation, status,
          started_at
        ) VALUES (
          ${newId("runtimeInstance")}, ${organizationId}, ${runId}, ${run.worker_id},
          ${input.containerId}, ${input.imageDigest}, ${(latest?.generation ?? 0) + 1},
          ${input.status}::runtime_status,
          ${input.status === "running" ? new Date().toISOString() : null}
        )
        RETURNING id, generation, status`) as Array<Record<string, unknown>>;
      runtime = inserted[0]!;
    }

    const event = await appendInScope(scope, {
      eventType:
        input.status === "running"
          ? EventTypes.RuntimeStarted
          : input.status === "destroyed"
            ? EventTypes.RuntimeDestroyed
            : terminal
              ? EventTypes.RuntimeStopped
              : EventTypes.RuntimeCreating,
      organizationId,
      projectId: run.project_id,
      runId,
      actor: { type: "system", id: run.worker_id },
      source: "runner",
      correlationId: runId,
      payload: {
        containerId: input.containerId,
        status: input.status,
        generation: runtime.generation,
      },
    });

    return { runtime, event };
  });

  if (!result) throw notFound(`run ${runId} not found`);
  if ("unleased" in result) throw badRequest(`run ${runId} has no worker lease`);
  eventBus.publish(result.event);
  return json(result.runtime);
}

/**
 * Ingest events produced inside the execution plane.
 *
 * The runner and harness are event *sources*; normalizing and persisting them
 * here keeps one durable ledger rather than several partial ones.
 */
const ingestInput = z.object({
  events: z
    .array(
      z.object({
        eventType: z.string().min(1),
        occurredAt: z.string().datetime({ offset: true }).optional(),
        runId: z.string().nullable().default(null),
        sessionId: z.string().nullable().default(null),
        projectId: z.string().nullable().default(null),
        workItemId: z.string().nullable().default(null),
        actorType: z.enum(["system", "human", "agent", "integration"]).default("agent"),
        actorId: z.string().default("runner"),
        correlationId: z.string().nullable().default(null),
        causationId: z.string().nullable().default(null),
        payload: z.record(z.unknown()).default({}),
      }),
    )
    .min(1)
    .max(500),
});

async function ingestEvents(ctx: RequestContext): Promise<Response> {
  const input = await parseBody(ctx.request, ingestInput);
  const { organizationId } = ctx.principal;

  const events = await withOrg(organizationId, async (scope) => {
    const out = [];
    for (const e of input.events) {
      out.push(
        await appendInScope(scope, {
          eventType: e.eventType,
          organizationId,
          projectId: e.projectId ?? null,
          workItemId: e.workItemId ?? null,
          runId: e.runId ?? null,
          sessionId: e.sessionId ?? null,
          actor: { type: e.actorType ?? "agent", id: e.actorId ?? "runner" },
          source: "runner",
          correlationId: e.correlationId ?? null,
          causationId: e.causationId ?? null,
          payload: e.payload ?? {},
          ...(e.occurredAt ? { occurredAt: e.occurredAt } : {}),
        }),
      );
    }
    return out;
  });

  for (const event of events) eventBus.publish(event);
  return json({ accepted: events.length, cursor: events[events.length - 1]?.cursor ?? null }, 202);
}

export function registerRunnerRoutes(router: Router): void {
  const runnerOnly = { requireKind: "runner" as const };

  router.post("/v1/runner/workers", register, runnerOnly);
  router.post("/v1/runner/workers/:id/heartbeat", heartbeat, runnerOnly);
  router.post("/v1/runner/workers/:id/claim", claimRuns, runnerOnly);

  router.post("/v1/runner/runs/:id/lease", renewLease, runnerOnly);
  router.post("/v1/runner/runs/:id/status", updateRun, runnerOnly);
  router.post("/v1/runner/runs/:id/runtime", upsertRuntime, runnerOnly);

  router.post("/v1/runner/events", ingestEvents, runnerOnly);
}
