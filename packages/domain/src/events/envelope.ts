import { z } from "zod";

/**
 * Event envelope — plan §18.1.
 *
 * Events are append-only and are the authoritative audit trail of what
 * happened and when. Relational tables hold current state; this ledger
 * explains how that state came to be.
 */

export const actorTypeSchema = z.enum(["system", "human", "person", "agent", "integration"]);
export type ActorType = z.infer<typeof actorTypeSchema>;

export const actorSchema = z.object({
  type: actorTypeSchema,
  id: z.string(),
});
export type Actor = z.infer<typeof actorSchema>;

export const eventSourceSchema = z.enum([
  "control-plane",
  "runner",
  "harness",
  "github",
  "slack",
  "jira",
  "web",
  "cli",
]);
export type EventSource = z.infer<typeof eventSourceSchema>;

export const eventEnvelopeSchema = z.object({
  eventId: z.string(),
  eventType: z.string(),
  occurredAt: z.string().datetime({ offset: true }),
  organizationId: z.string(),

  projectId: z.string().nullable().default(null),
  taskId: z.string().nullable().default(null),
  runId: z.string().nullable().default(null),
  sessionId: z.string().nullable().default(null),
  workflowRunId: z.string().nullable().default(null),

  actor: actorSchema,
  source: eventSourceSchema,

  /** Groups every event belonging to one logical operation. */
  correlationId: z.string().nullable().default(null),
  /** The event that directly caused this one. */
  causationId: z.string().nullable().default(null),

  payload: z.record(z.unknown()).default({}),
});

export type EventEnvelope = z.infer<typeof eventEnvelopeSchema>;

/**
 * What a producer supplies; the ledger fills in id, cursor and timestamp.
 *
 * The scope and causation fields are optional rather than required-nullable.
 * Most events belong to one part of the hierarchy, and spelling out four
 * `null`s at every call site is noise that hides the field that matters — and
 * makes adding a scope column an edit to every producer.
 */
export type EventInput = Pick<EventEnvelope, "eventType" | "organizationId" | "actor" | "source"> &
  Partial<
    Pick<
      EventEnvelope,
      | "eventId"
      | "occurredAt"
      | "projectId"
      | "taskId"
      | "runId"
      | "sessionId"
      | "workflowRunId"
      | "correlationId"
      | "causationId"
      | "payload"
    >
  >;

/**
 * A persisted event also carries a monotonic per-session cursor so the UI
 * can resume a dropped SSE stream without losing activity (plan §106).
 */
export interface PersistedEvent extends EventEnvelope {
  cursor: number;
  /**
   * In API output, human and person actors resolve to a `PersonRef`.
   * Legacy human actors also carry `keyId`, the key the ledger recorded.
   * An actor the ledger cannot resolve stays as recorded.
   */
  actor: EventEnvelope["actor"] & {
    name?: string;
    photoUrl?: string | null;
    online?: boolean;
    keyId?: string;
  };
}
