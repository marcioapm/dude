/**
 * A task's relations and its start when unblocked
 * (docs/design/task-relations.md, "Routes"). Display and management, so
 * the backend's to write; the orchestrator writes them only through agent
 * tools and filing, in the same SQL.
 *
 * - Adding is any member's. A `blocks` edge that would close a loop is
 *   refused naming it, under the organisation's relation lock, so two
 *   writers cannot each close half of one.
 * - Removing is the owner's: the blocked task's owner for `blocks`, either
 *   task's for `related` (a task nobody owns is anyone's). `canRemove` is
 *   worked out here for the reader, so the page has no rule of its own.
 * - Start when unblocked is its task's owner's, and only on a task that is
 *   blocked by something that can still merge.
 *
 * Raw SQL, as the repo writes it: the loop's walk is the
 * task_relation_would_cycle function's (migration 111).
 */

import { z } from "zod";
import {
  EventTypes, NOT_STARTED_STATUSES, firstName, keyList, neverMergesRefusal, newId,
  type AutoStartEnd, type AutoStartHow, type BlockerRef, type TaskRelationView, type TaskRelations, type TaskStatus,
} from "@dude/domain";
import { withOrg, type OrgScope } from "../../db/client.ts";
import { HttpError, badRequest, json, noContent, notFound, parseBody } from "../http.ts";
import { kickOrchestrator } from "../../orchestrator/client.ts";
import { recordAs } from "./people.ts";
import type { RequestContext, Router } from "../router.ts";

interface TaskRow {
  id: string;
  key: string;
  projectId: string;
  status: TaskStatus;
  ownerId: string | null;
  ownerName: string | null;
}

const TASK_ROW = `t.id, p.key_prefix || '-' || t.number AS key, t.project_id AS "projectId", t.status,
  o.id AS "ownerId", o.name AS "ownerName"`;
const OWNER_JOIN = `LEFT JOIN LATERAL (SELECT pe.id, pe.name FROM task_people tp JOIN people pe ON pe.id = tp.person_id
  WHERE tp.task_id = t.id AND pe.removed_at IS NULL ORDER BY tp.position, tp.person_id LIMIT 1) o ON true`;

/** A task by its id or its key (CP-41), in the caller's organisation; null when none. */
async function findTask(scope: OrgScope, idOrKey: string): Promise<TaskRow | null> {
  const m = /^([A-Za-z][A-Za-z0-9]*)-0*([0-9]+)$/.exec(idOrKey.trim());
  const rows = (await scope.sql`
    SELECT ${scope.sql.unsafe(TASK_ROW)}
    FROM tasks t JOIN projects p ON p.id = t.project_id ${scope.sql.unsafe(OWNER_JOIN)}
    WHERE t.id = ${idOrKey} OR (${m ? m[1]! : null}::text IS NOT NULL
      AND upper(p.key_prefix) = upper(${m ? m[1]! : ""}) AND t.number = ${m ? Number(m[2]) : 0})
    LIMIT 1`) as TaskRow[];
  return rows[0] ?? null;
}

const ownerName = (name: string | null | undefined) => firstName(name, "its owner");

/** Whether `person` may remove the edge `from → to` of `kind`, and if not, whose it is. */
function removal(kind: "blocks" | "related", from: TaskRow, to: TaskRow, here: TaskRow, person: string): { can: boolean; by: string | null } {
  const owners = kind === "blocks" ? [to] : [from, to];
  if (owners.some((t) => t.ownerId === null || t.ownerId === person)) return { can: true, by: null };
  const holder = owners.find((t) => t.id === here.id) ?? owners[0]!;
  const name = ownerName(holder.ownerName);
  return { can: false, by: holder.id === here.id ? `Only ${name}, the owner, can remove this` : `Only ${name}, ${holder.key}'s owner, can remove this` };
}

/** Tell open pages that relations changed (live only, not in the ledger). */
async function relationsChanged(scope: OrgScope, taskIds: string[]): Promise<void> {
  await scope.sql`SELECT pg_notify('dude_events', ${JSON.stringify({ organizationId: scope.organizationId, relationsChanged: { taskIds } })})`;
}

async function getRelations(ctx: RequestContext): Promise<Response> {
  const out = await withOrg(ctx.principal.organizationId, async (scope) => {
    const here = await findTask(scope, ctx.params.id!);
    if (!here) return null;
    return readRelations(scope, here, ctx.principal.personId);
  });
  if (!out) throw notFound(`task ${ctx.params.id} not found`);
  return json(out);
}

interface EdgeRow {
  fromId: string;
  toId: string;
  kind: "blocks" | "related";
  why: string | null;
  createdAt: string;
  person: string | null;
  role: string | null;
  runOwner: string | null;
}

/** The task's relations as its page reads them, for `person`. */
export async function readRelations(scope: OrgScope, here: TaskRow, person: string): Promise<TaskRelations> {
  const edges = (await scope.sql`
    SELECT r.from_task_id AS "fromId", r.to_task_id AS "toId", r.kind, r.why, r.created_at AS "createdAt",
      cp.name AS person, run.role, ro.name AS "runOwner"
    FROM task_relations r
    LEFT JOIN people cp ON cp.id = r.created_by_person
    LEFT JOIN runs run ON run.id = r.created_by_run_id
    LEFT JOIN LATERAL (SELECT pe.name FROM task_people tp JOIN people pe ON pe.id = tp.person_id
      WHERE tp.task_id = run.task_id AND pe.removed_at IS NULL ORDER BY tp.position, tp.person_id LIMIT 1) ro ON true
    WHERE r.from_task_id = ${here.id} OR r.to_task_id = ${here.id}
    ORDER BY r.created_at`) as EdgeRow[];
  const others = [...new Set(edges.map((e) => (e.fromId === here.id ? e.toId : e.fromId)))];
  const tasks = (await scope.sql`
    SELECT ${scope.sql.unsafe(TASK_ROW)}, t.title, p.name AS "projectName",
      t.status = 'done' AND EXISTS (SELECT 1 FROM pull_requests pr WHERE pr.task_id = t.id AND pr.state = 'merged') AS merged
    FROM tasks t JOIN projects p ON p.id = t.project_id ${scope.sql.unsafe(OWNER_JOIN)}
    WHERE t.id = ANY(${scope.sql.array(others, "text")}::text[])`) as Array<TaskRow & { title: string; projectName: string; merged: boolean }>;
  const byId = new Map(tasks.map((t) => [t.id, t]));
  const out: TaskRelations = { blockedBy: [], blocks: [], related: [], autoStart: null,
    owner: here.ownerId ? { id: here.ownerId, name: here.ownerName ?? "" } : null };
  for (const e of edges) {
    const other = byId.get(e.fromId === here.id ? e.toId : e.fromId);
    if (!other) continue;
    const from = e.fromId === here.id ? here : other;
    const to = e.toId === here.id ? here : other;
    const may = removal(e.kind, from, to, here, person);
    const view: TaskRelationView = {
      task: { id: other.id, key: other.key, title: other.title, status: other.status,
        project: other.projectId !== here.projectId ? other.projectName : null,
        unmerged: other.status === "done" && !other.merged, merged: other.merged,
        owner: other.ownerId ? { id: other.ownerId, name: other.ownerName ?? "" } : null },
      createdBy: e.person ? { kind: "person", name: e.person } : { kind: "agent", name: `${ownerName(e.runOwner)}'s ${e.role ?? "agent"}`, role: e.role },
      createdAt: e.createdAt, why: e.why, canRemove: may.can, removableBy: may.by,
    };
    if (e.kind === "related") out.related.push(view);
    else if (e.toId === here.id) out.blockedBy.push(view);
    else out.blocks.push(view);
  }
  const auto = (await scope.sql`
    SELECT a.how, a.set_at AS at, p.id, p.name FROM task_auto_starts a JOIN people p ON p.id = a.set_by_person
    WHERE a.task_id = ${here.id}`) as Array<{ how: AutoStartHow; at: string; id: string; name: string }>;
  if (auto[0]) out.autoStart = { how: auto[0].how, at: new Date(auto[0].at).toISOString(), by: { id: auto[0].id, name: auto[0].name } };
  return out;
}

const addInput = z.object({
  kind: z.enum(["blocked_by", "blocks", "related"]),
  /** The other task, by key (CP-41) or id. */
  task: z.string().min(1).max(200),
  why: z.string().max(2000).optional(),
}).strict();

async function addRelation(ctx: RequestContext): Promise<Response> {
  const input = await parseBody(ctx.request, addInput);
  const out = await withOrg(ctx.principal.organizationId, async (scope) => {
    const here = await findTask(scope, ctx.params.id!);
    if (!here) throw notFound(`task ${ctx.params.id} not found`);
    const other = await findTask(scope, input.task);
    if (!other) throw notFound(`no task ${input.task} in this organisation`);
    if (other.id === here.id) throw new HttpError(400, "a task can't be related to itself", "self");
    let [from, to] = input.kind === "blocked_by" ? [other, here] : [here, other];
    const kind = input.kind === "related" ? "related" : "blocks";
    if (kind === "related" && from.id > to.id) [from, to] = [to, from];
    // One writer at a time per organisation: the loop check and the insert
    // must see each other's edges.
    await scope.sql`SELECT pg_advisory_xact_lock(hashtext('task_relations:' || ${scope.organizationId}))`;
    const existing = (await scope.sql`
      SELECT kind FROM task_relations WHERE (from_task_id = ${here.id} AND to_task_id = ${other.id})
        OR (from_task_id = ${other.id} AND to_task_id = ${here.id})`) as Array<{ kind: string }>;
    if (existing.length > 0) throw new HttpError(409, `${here.key} and ${other.key} are already related`, "duplicate");
    if (kind === "blocks") {
      const rows = (await scope.sql`SELECT task_relation_would_cycle(${from.id}, ${to.id}) AS loop`) as Array<{ loop: string[] | null }>;
      const loop = rows[0]?.loop;
      if (loop) {
        throw new HttpError(409, `That would make a loop: ${loop.join(" → ")}. A task can't wait on itself.`, "cycle", { loop });
      }
    }
    const why = input.why?.trim() || null;
    await scope.sql`
      INSERT INTO task_relations (organization_id, from_task_id, to_task_id, kind, created_by_person, why)
      VALUES (${scope.organizationId}, ${from.id}, ${to.id}, ${kind}::task_relation_kind, ${ctx.principal.personId}, ${why})`;
    const payload = { kind, from: from.key, to: to.key, ...(why ? { why } : {}) };
    for (const t of [here, other]) await recordAs(scope, ctx, EventTypes.TaskRelationAdded, payload, { projectId: t.projectId, taskId: t.id });
    await relationsChanged(scope, [here.id, other.id]);
    return readRelations(scope, here, ctx.principal.personId);
  });
  return json(out, 201);
}

async function removeRelation(ctx: RequestContext): Promise<Response> {
  const side = ctx.params.kind!;
  if (!["blocked_by", "blocks", "related"].includes(side)) throw badRequest("kind is blocked_by, blocks or related");
  let freed = false;
  await withOrg(ctx.principal.organizationId, async (scope) => {
    const here = await findTask(scope, ctx.params.id!);
    const other = await findTask(scope, ctx.params.other!);
    if (!here || !other) throw notFound("no such relation");
    let [from, to] = side === "blocked_by" ? [other, here] : [here, other];
    const kind = side === "related" ? "related" : "blocks";
    if (kind === "related" && from.id > to.id) [from, to] = [to, from];
    const may = removal(kind, from, to, here, ctx.principal.personId);
    if (!may.can) {
      const holder = kind === "blocks" ? to : here;
      const why = kind === "blocks" ? "it holds up their task" : "it's on their task";
      throw new HttpError(403, `only ${ownerName(holder.ownerName)} can remove this relation — ${why}`, "not_owner");
    }
    await scope.sql`SELECT pg_advisory_xact_lock(hashtext('task_relations:' || ${scope.organizationId}))`;
    const gone = await scope.sql`
      DELETE FROM task_relations WHERE from_task_id = ${from.id} AND to_task_id = ${to.id} AND kind = ${kind}::task_relation_kind
      RETURNING 1`;
    if (gone.length === 0) throw notFound("no such relation");
    for (const t of [here, other]) {
      await recordAs(scope, ctx, EventTypes.TaskRelationRemoved, { kind, from: from.key, to: to.key }, { projectId: t.projectId, taskId: t.id });
    }
    if (kind === "blocks") freed = await releasedByRemoval(scope, ctx, to);
    await relationsChanged(scope, [here.id, other.id]);
  });
  // A delivery parked on the edge reads its blockers again now.
  if (freed) await kickOrchestrator(ctx.principal.organizationId);
  return noContent();
}

/**
 * After a blocks edge into `task` is removed: with nothing open in front of
 * it, its start when unblocked ends (the person removing is present and
 * acting, so it is simply startable), and a delivery parked at its
 * implement step is signalled to read its blockers again. Says whether it
 * is free now.
 */
async function releasedByRemoval(scope: OrgScope, ctx: RequestContext, task: TaskRow): Promise<boolean> {
  const open = (await scope.sql`SELECT 1 FROM task_blocked_by(${task.id})`) as unknown[];
  if (open.length > 0) return false;
  await endAutoStart(scope, ctx, task.id, task.projectId, "unblocked_by_removal");
  // The orchestrator's signal, written as workflow.SignalTx writes one
  // (orchestrator/internal/workflow/runtime.go signalTx): the backend cannot
  // call into its workflow runtime, and the signal must commit with the
  // removal. Row: a `sig` id, the run, the name, an object payload, no
  // idempotency key; then the run woken only if it waits on that name and
  // holds no timer. The control plane's relations test pins this row; the
  // orchestrator's TestAParkedDeliveryResumesOnTheBackendsSignal runs a
  // parked delivery on it.
  const parked = (await scope.sql`
    SELECT id FROM workflow_runs WHERE task_id = ${task.id} AND status = 'waiting' AND awaiting_signals ? ${RELATIONS_UNBLOCK_SIGNAL}`) as Array<{ id: string }>;
  for (const { id } of parked) {
    await scope.sql`INSERT INTO workflow_signals (id, organization_id, workflow_run_id, name, payload, idempotency_key)
      VALUES (${newId("workflowSignal")}, ${scope.organizationId}, ${id}, ${RELATIONS_UNBLOCK_SIGNAL}, ${{ removed: true }}::jsonb, NULL)`;
    await scope.sql`UPDATE workflow_runs SET status = 'running', wake_at = NULL
      WHERE id = ${id} AND status = 'waiting' AND awaiting_signals ? ${RELATIONS_UNBLOCK_SIGNAL}
        AND (wait_until IS NULL OR wait_until <= now())`;
  }
  return true;
}

/** The signal a delivery parked on its blockers waits on (delivery.SignalRelationsUnblock). */
const RELATIONS_UNBLOCK_SIGNAL = "relations.unblocked";

/** End a task's start when unblocked without firing it, saying why; nothing when none is set. */
export async function endAutoStart(scope: OrgScope, ctx: RequestContext, taskId: string, projectId: string,
  why: Exclude<AutoStartEnd, "failed">): Promise<boolean> {
  const gone = (await scope.sql`DELETE FROM task_auto_starts WHERE task_id = ${taskId} RETURNING how`) as Array<{ how: string }>;
  if (!gone[0]) return false;
  await recordAs(scope, ctx, EventTypes.TaskAutoStartCancelled, { how: gone[0].how, why }, { projectId, taskId });
  await relationsChanged(scope, [taskId]);
  return true;
}

const autoStartInput = z.object({ how: z.enum(["deliver", "talk"]) }).strict();

function ownerOnly(task: TaskRow, person: string, verb: string): void {
  if (task.ownerId !== null && task.ownerId !== person) {
    throw new HttpError(403, `only ${ownerName(task.ownerName)}, its owner, can ${verb} this`, "not_owner");
  }
}

async function setAutoStart(ctx: RequestContext): Promise<Response> {
  const input = await parseBody(ctx.request, autoStartInput);
  const out = await withOrg(ctx.principal.organizationId, async (scope) => {
    const task = await findTask(scope, ctx.params.id!);
    if (!task) throw notFound(`task ${ctx.params.id} not found`);
    ownerOnly(task, ctx.principal.personId, "set");
    await scope.sql`SELECT 1 FROM tasks WHERE id = ${task.id} FOR NO KEY UPDATE`;
    const started = (await scope.sql`SELECT 1 FROM workflow_runs WHERE task_id = ${task.id} AND status IN ('running', 'waiting')`) as unknown[];
    if (!NOT_STARTED_STATUSES.includes(task.status) || started.length > 0) {
      throw new HttpError(409, `${task.key} has started: there is nothing to start when it's unblocked`, "started");
    }
    const blockers = (await scope.sql`SELECT key, status FROM task_blocked_by(${task.id})`) as BlockerRef[];
    if (blockers.length === 0) throw new HttpError(409, "it can start now: Deliver or Talk it through", "not_blocked");
    const never = blockers.filter((b) => b.status === "done");
    if (never.length > 0) {
      throw new HttpError(409, neverMergesRefusal(never.map((b) => b.key)), "never_merges", { blockers: never });
    }
    await scope.sql`
      INSERT INTO task_auto_starts (task_id, organization_id, how, set_by_person)
      VALUES (${task.id}, ${scope.organizationId}, ${input.how}, ${ctx.principal.personId})
      ON CONFLICT (task_id) DO UPDATE SET how = EXCLUDED.how, set_by_person = EXCLUDED.set_by_person, set_at = now(), generation = EXCLUDED.generation, due_at = NULL, fire_attempts = 0`;
    await recordAs(scope, ctx, EventTypes.TaskAutoStartSet, { how: input.how }, { projectId: task.projectId, taskId: task.id });
    const how = input.how === "deliver" ? "Deliver" : "Talk it through";
    await recordAs(scope, ctx, EventTypes.ChatNotice, { about: "auto_start_set",
      text: `${ownerName(ctx.principal.name)} set this to start when it's unblocked, with ${how}. Nothing starts before ${keyList(blockers.map((b) => b.key))} ${blockers.length === 1 ? "merges" : "merge"}.` },
      { projectId: task.projectId, taskId: task.id });
    await relationsChanged(scope, [task.id]);
    return readRelations(scope, task, ctx.principal.personId);
  });
  return json(out);
}

async function cancelAutoStart(ctx: RequestContext): Promise<Response> {
  await withOrg(ctx.principal.organizationId, async (scope) => {
    const task = await findTask(scope, ctx.params.id!);
    if (!task) throw notFound(`task ${ctx.params.id} not found`);
    ownerOnly(task, ctx.principal.personId, "cancel");
    if (!(await endAutoStart(scope, ctx, task.id, task.projectId, "owner"))) throw notFound(`${task.key} is not set to start when unblocked`);
  });
  return noContent();
}

export function registerRelationRoutes(router: Router): void {
  router.get("/v1/tasks/:id/relations", getRelations);
  router.post("/v1/tasks/:id/relations", addRelation);
  router.delete("/v1/tasks/:id/relations/:kind/:other", removeRelation);
  router.put("/v1/tasks/:id/auto-start", setAutoStart);
  router.delete("/v1/tasks/:id/auto-start", cancelAutoStart);
}
