/**
 * Event ledger — plan §4.2, §18, §106.
 *
 * Append-only and authoritative: relational tables hold current state, this
 * ledger explains how that state came to be. It is what makes metrics,
 * debugging, replay and session inspection possible.
 *
 * Two channels, deliberately (plan §106):
 *   - durable semantic events (here) — persisted, cursored, replayable;
 *   - high-volume streams (stdout bytes, model tokens) — NOT one row each.
 */

import { newId, type EventInput, type PersistedEvent, type PersonRef } from "@dude/domain";
import { withOrg, type OrgScope } from "../db/client.ts";

/**
 * Columns selected for read paths, aliased to the domain's camelCase shape.
 *
 * A plain string spliced in via `sql.unsafe`: identifiers cannot be bound
 * parameters. It contains no caller input.
 */
const EVENT_COLUMNS = `
  cursor,
  id                AS "eventId",
  event_type        AS "eventType",
  occurred_at       AS "occurredAt",
  organization_id   AS "organizationId",
  project_id        AS "projectId",
  task_id      AS "taskId",
  run_id            AS "runId",
  session_id        AS "sessionId",
  workflow_run_id   AS "workflowRunId",
  automation_run_id AS "automationRunId",
  actor_type        AS "actorType",
  actor_id          AS "actorId",
  source,
  correlation_id    AS "correlationId",
  causation_id      AS "causationId",
  payload,
  -- Legacy human actors name keys; person actors name people directly.
  CASE WHEN actor_type = 'human' THEN
    (SELECT person_ref(p) FROM api_keys k JOIN people p ON p.id = k.person_id WHERE k.id = events.actor_id)
  WHEN actor_type = 'person' THEN
    (SELECT person_ref(p) FROM people p WHERE p.id = events.actor_id)
  END               AS "actorPerson"`;

interface EventRow {
  cursor: string | number;
  eventId: string;
  eventType: string;
  occurredAt: string | Date;
  organizationId: string;
  projectId: string | null;
  taskId: string | null;
  runId: string | null;
  sessionId: string | null;
  workflowRunId: string | null;
  automationRunId?: string | null;
  actorType: PersistedEvent["actor"]["type"];
  actorId: string;
  source: PersistedEvent["source"];
  correlationId: string | null;
  causationId: string | null;
  payload: Record<string, unknown>;
  actorPerson?: PersonRef | null;
}

function toPersisted(row: EventRow): PersistedEvent {
  return {
    // bigint arrives as a string from the driver; the cursor must be a number
    // for callers to compare and increment.
    cursor: Number(row.cursor),
    eventId: row.eventId,
    eventType: row.eventType,
    occurredAt:
      row.occurredAt instanceof Date ? row.occurredAt.toISOString() : new Date(row.occurredAt).toISOString(),
    organizationId: row.organizationId,
    projectId: row.projectId,
    taskId: row.taskId,
    runId: row.runId,
    sessionId: row.sessionId,
    workflowRunId: row.workflowRunId,
    // Only an automation run's activity carries it (migration 216).
    ...(row.automationRunId ? { automationRunId: row.automationRunId } : {}),
    actor: row.actorPerson
      ? { type: row.actorType, ...row.actorPerson, ...(row.actorType === "human" ? { keyId: row.actorId } : {}) }
      : { type: row.actorType, id: row.actorId },
    source: row.source,
    correlationId: row.correlationId,
    causationId: row.causationId,
    payload: row.payload ?? {},
  };
}

/**
 * Append one event within an existing transaction.
 *
 * Use this when the event must commit atomically with the state change that
 * produced it — which is nearly always. Live subscribers hear of it through
 * the NOTIFY the insert raises, which Postgres delivers only on commit, so a
 * rolled-back transaction never leaks a phantom event to the UI.
 */
export async function appendInScope(scope: OrgScope, input: EventInput): Promise<PersistedEvent> {
  if (input.organizationId !== scope.organizationId) {
    throw new Error(
      `event organizationId ${input.organizationId} does not match scope ${scope.organizationId}`,
    );
  }

  const id = input.eventId ?? newId("event");
  const rows = (await scope.sql`
    INSERT INTO events (
      id, organization_id, event_type, occurred_at,
      project_id, task_id, run_id, session_id, workflow_run_id,
      actor_type, actor_id, source, correlation_id, causation_id, payload
    ) VALUES (
      ${id}, ${input.organizationId}, ${input.eventType},
      ${input.occurredAt ?? new Date().toISOString()},
      ${input.projectId ?? null}, ${input.taskId ?? null}, ${input.runId ?? null},
      ${input.sessionId ?? null}, ${input.workflowRunId ?? null},
      ${input.actor.type}, ${input.actor.id}, ${input.source},
      ${input.correlationId ?? null}, ${input.causationId ?? null},
      ${input.payload ?? {}}::jsonb
    )
    RETURNING ${scope.sql.unsafe(EVENT_COLUMNS)}`) as EventRow[];

  const row = rows[0];
  if (!row) throw new Error("event insert returned no row");
  return toPersisted(row);
}

/** Append one event in its own transaction. Live subscribers hear of it through NOTIFY. */
export async function append(input: EventInput): Promise<PersistedEvent> {
  const event = await withOrg(input.organizationId, (scope) => appendInScope(scope, input));
  return event;
}

/** Append several events atomically. */
export async function appendMany(
  organizationId: string,
  inputs: readonly EventInput[],
): Promise<PersistedEvent[]> {
  if (inputs.length === 0) return [];
  const events = await withOrg(organizationId, async (scope) => {
    const out: PersistedEvent[] = [];
    for (const input of inputs) out.push(await appendInScope(scope, input));
    return out;
  });
  return events;
}

export interface EventQuery {
  /** Exclusive lower bound — the cursor the client last saw. */
  after?: number | undefined;
  sessionId?: string | undefined;
  runId?: string | undefined;
  taskId?: string | undefined;
  projectId?: string | undefined;
  /** An automation run's activity (events_automation_run_cursor_idx). */
  automationRunId?: string | undefined;
  eventTypes?: readonly string[] | undefined;
  limit?: number | undefined;
  /**
   * The person reading: a brainstorm session's events only if they are an
   * accepted member (session_visible). Unset only for the process's own
   * read that feeds the live bus, which filters per subscriber.
   */
  viewer?: string | undefined;
}

const DEFAULT_LIMIT = 200;
const MAX_LIMIT = 1000;

/**
 * Read events in cursor order.
 *
 * Ordering by the identity cursor (not by timestamp) is what makes resume
 * exact: two events committed in the same millisecond still have a total
 * order, so `after` can never skip or duplicate one.
 */
export async function query(organizationId: string, q: EventQuery = {}): Promise<PersistedEvent[]> {
  const limit = Math.min(Math.max(q.limit ?? DEFAULT_LIMIT, 1), MAX_LIMIT);
  const viewer = q.viewer ?? null;
  const sessionId = q.sessionId ?? null;

  return withOrg(organizationId, async (scope) => {
    // session_visible as one set of the viewer's sessions, each arm on an
    // index: their accepted rows (session_people_person_idx), the
    // organisation's open sessions (sessions_org_access_idx) and, for an
    // admin, those its admins own (sessions_owned_by_admins_idx). The same
    // answer as session_role(id, viewer) IS NOT NULL, without calling it per session.
    const rows = (await scope.sql`
      SELECT ${scope.sql.unsafe(EVENT_COLUMNS)} FROM events
      WHERE (${q.after ?? null}::bigint IS NULL OR cursor > ${q.after ?? null}::bigint)
        AND (${sessionId}::text IS NULL OR session_id = ${sessionId})
        AND (${q.runId ?? null}::text IS NULL OR run_id = ${q.runId ?? null})
        AND (${q.taskId ?? null}::text IS NULL OR task_id = ${q.taskId ?? null})
        AND (${q.projectId ?? null}::text IS NULL OR project_id = ${q.projectId ?? null})
        AND (${q.automationRunId ?? null}::text IS NULL OR automation_run_id = ${q.automationRunId ?? null})
        AND (${q.eventTypes?.length ? (q.eventTypes as string[]) : null}::text[] IS NULL
             OR event_type = ANY(${q.eventTypes?.length ? (q.eventTypes as string[]) : null}::text[]))
        AND (${viewer}::text IS NULL OR session_id IS NULL
             OR session_id IN (
               SELECT sp.session_id FROM people me
                 JOIN session_people sp ON sp.person_id = me.id AND sp.accepted_at IS NOT NULL
                 JOIN sessions s ON s.id = sp.session_id AND s.organization_id = me.organization_id
               WHERE me.id = ${viewer} AND me.removed_at IS NULL AND (${sessionId}::text IS NULL OR sp.session_id = ${sessionId})
               UNION ALL
               SELECT s.id FROM people me JOIN sessions s ON s.organization_id = me.organization_id AND s.access = 'organisation'
               WHERE me.id = ${viewer} AND me.removed_at IS NULL AND (${sessionId}::text IS NULL OR s.id = ${sessionId})
               UNION ALL
               SELECT s.id FROM people me JOIN sessions s ON s.organization_id = me.organization_id AND s.owned_by_admins
               WHERE me.id = ${viewer} AND me.removed_at IS NULL AND me.role = 'admin' AND (${sessionId}::text IS NULL OR s.id = ${sessionId}))
             OR NOT EXISTS (SELECT 1 FROM sessions s WHERE s.id = events.session_id))
      ORDER BY cursor ASC
      LIMIT ${limit}`) as EventRow[];
    return rows.map(toPersisted);
  });
}

