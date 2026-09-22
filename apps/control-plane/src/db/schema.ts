/**
 * Drizzle schema — typed mirror of migrations/*.sql.
 *
 * The .sql files are the source of truth for DDL; this file exists so queries
 * are typed and refactor-safe. Keep the two in sync: a column added in a
 * migration must be added here in the same change.
 *
 * Note that Drizzle is not aware of row-level security. Every query against a
 * tenant table must still run inside `withOrg` — see ./client.ts.
 */

import {
  bigint,
  bigserial,
  boolean,
  index,
  integer,
  jsonb,
  numeric,
  pgEnum,
  pgTable,
  text,
  timestamp,
  uniqueIndex,
} from "drizzle-orm/pg-core";
import type { AgentModels } from "@dude/domain";

const ts = (name: string) => timestamp(name, { withTimezone: true, mode: "string" });

// ---------------------------------------------------------------------------
// Enums
// ---------------------------------------------------------------------------

export const orgRoleEnum = pgEnum("org_role", ["owner", "admin", "member", "viewer"]);

export const trustClassEnum = pgEnum("trust_class", ["trusted_internal", "untrusted_external"]);

export const workItemStatusEnum = pgEnum("work_item_status", [
  "received", "intake", "awaiting_confirmation", "queued", "running",
  "awaiting_input", "review", "ready_to_merge", "done", "failed", "aborted",
]);

export const workerStatusEnum = pgEnum("worker_status", [
  "registering", "ready", "draining", "lost",
]);

export const runStatusEnum = pgEnum("run_status", [
  "pending", "scheduled", "starting", "running", "paused",
  "completed", "failed", "aborted",
]);

export const runtimeStatusEnum = pgEnum("runtime_status", [
  "creating", "starting", "running", "stopping", "stopped", "destroyed", "failed",
]);

export const agentRoleEnum = pgEnum("agent_role", [
  "orchestrator", "investigator", "implementer", "reviewer", "simplifier", "qa_browser",
]);

export const sessionStatusEnum = pgEnum("session_status", [
  "pending", "running", "awaiting_input", "completed", "failed", "aborted",
]);

export const workflowRunStatusEnum = pgEnum("workflow_run_status", [
  "running", "waiting", "completed", "failed", "dead_lettered", "aborted",
]);

export const questionStatusEnum = pgEnum("question_status", [
  "open", "answered", "cancelled", "expired",
]);

// ---------------------------------------------------------------------------
// Organizations, users, membership
// ---------------------------------------------------------------------------

export const organizations = pgTable("organizations", {
  id: text("id").primaryKey(),
  name: text("name").notNull(),
  slug: text("slug").notNull().unique(),
  defaultAgentModels: jsonb("default_agent_models").$type<AgentModels>().notNull().default({}),
  createdAt: ts("created_at").notNull().defaultNow(),
  updatedAt: ts("updated_at").notNull().defaultNow(),
});

export const users = pgTable("users", {
  id: text("id").primaryKey(),
  email: text("email").notNull().unique(),
  name: text("name").notNull().default(""),
  createdAt: ts("created_at").notNull().defaultNow(),
});

export const orgMemberships = pgTable("org_memberships", {
  organizationId: text("organization_id").notNull().references(() => organizations.id, { onDelete: "cascade" }),
  userId: text("user_id").notNull().references(() => users.id, { onDelete: "cascade" }),
  role: orgRoleEnum("role").notNull().default("member"),
  createdAt: ts("created_at").notNull().defaultNow(),
});

export const apiKeys = pgTable("api_keys", {
  id: text("id").primaryKey(),
  organizationId: text("organization_id").notNull().references(() => organizations.id, { onDelete: "cascade" }),
  name: text("name").notNull(),
  keyHash: text("key_hash").notNull().unique(),
  keyPrefix: text("key_prefix").notNull(),
  kind: text("kind").$type<"user" | "runner">().notNull().default("user"),
  createdAt: ts("created_at").notNull().defaultNow(),
  lastUsedAt: ts("last_used_at"),
  revokedAt: ts("revoked_at"),
});

// ---------------------------------------------------------------------------
// Projects and repositories
// ---------------------------------------------------------------------------

export const projects = pgTable(
  "projects",
  {
    id: text("id").primaryKey(),
    organizationId: text("organization_id").notNull().references(() => organizations.id, { onDelete: "cascade" }),
    name: text("name").notNull(),
    slug: text("slug").notNull(),
    description: text("description").notNull().default(""),
    /** Per-role model selection; unset roles fall back to the org default. */
    agentModels: jsonb("agent_models").$type<AgentModels>().notNull().default({}),
    runtimeImage: text("runtime_image"),
    createdAt: ts("created_at").notNull().defaultNow(),
    updatedAt: ts("updated_at").notNull().defaultNow(),
  },
  (t) => [uniqueIndex("projects_organization_id_slug_key").on(t.organizationId, t.slug)],
);

export const repositories = pgTable(
  "repositories",
  {
    id: text("id").primaryKey(),
    organizationId: text("organization_id").notNull().references(() => organizations.id, { onDelete: "cascade" }),
    projectId: text("project_id").notNull().references(() => projects.id, { onDelete: "cascade" }),
    name: text("name").notNull(),
    url: text("url").notNull(),
    defaultBranch: text("default_branch").notNull().default("main"),
    trust: trustClassEnum("trust").notNull().default("trusted_internal"),
    createdAt: ts("created_at").notNull().defaultNow(),
  },
  (t) => [uniqueIndex("repositories_project_id_name_key").on(t.projectId, t.name)],
);

// ---------------------------------------------------------------------------
// Epics and work items
// ---------------------------------------------------------------------------

export const epics = pgTable("epics", {
  id: text("id").primaryKey(),
  organizationId: text("organization_id").notNull().references(() => organizations.id, { onDelete: "cascade" }),
  projectId: text("project_id").notNull().references(() => projects.id, { onDelete: "cascade" }),
  title: text("title").notNull(),
  description: text("description").notNull().default(""),
  createdAt: ts("created_at").notNull().defaultNow(),
  updatedAt: ts("updated_at").notNull().defaultNow(),
});

export const workItems = pgTable(
  "work_items",
  {
    id: text("id").primaryKey(),
    organizationId: text("organization_id").notNull().references(() => organizations.id, { onDelete: "cascade" }),
    projectId: text("project_id").notNull().references(() => projects.id, { onDelete: "cascade" }),
    epicId: text("epic_id").references(() => epics.id, { onDelete: "set null" }),
    title: text("title").notNull(),
    goal: text("goal").notNull().default(""),
    acceptanceCriteria: jsonb("acceptance_criteria").$type<string[]>().notNull().default([]),
    status: workItemStatusEnum("status").notNull().default("received"),
    requestedBy: text("requested_by").references(() => users.id, { onDelete: "set null" }),
    createdAt: ts("created_at").notNull().defaultNow(),
    updatedAt: ts("updated_at").notNull().defaultNow(),
  },
  (t) => [index("work_items_project_status_idx").on(t.projectId, t.status)],
);

// ---------------------------------------------------------------------------
// Workers
// ---------------------------------------------------------------------------

export const workers = pgTable("workers", {
  id: text("id").primaryKey(),
  organizationId: text("organization_id").references(() => organizations.id, { onDelete: "cascade" }),
  name: text("name").notNull(),
  pool: text("pool").notNull().default("local"),
  status: workerStatusEnum("status").notNull().default("registering"),
  cpuMillis: integer("cpu_millis").notNull().default(0),
  memoryMb: integer("memory_mb").notNull().default(0),
  maxRuns: integer("max_runs").notNull().default(1),
  activeRuns: integer("active_runs").notNull().default(0),
  cachedImages: jsonb("cached_images").$type<string[]>().notNull().default([]),
  cachedRepositories: jsonb("cached_repositories").$type<string[]>().notNull().default([]),
  registeredAt: ts("registered_at").notNull().defaultNow(),
  lastHeartbeatAt: ts("last_heartbeat_at").notNull().defaultNow(),
});

// ---------------------------------------------------------------------------
// Runs, runtimes, sessions
// ---------------------------------------------------------------------------

export const runs = pgTable(
  "runs",
  {
    id: text("id").primaryKey(),
    organizationId: text("organization_id").notNull().references(() => organizations.id, { onDelete: "cascade" }),
    projectId: text("project_id").notNull().references(() => projects.id, { onDelete: "cascade" }),
    workItemId: text("work_item_id").notNull().references(() => workItems.id, { onDelete: "cascade" }),
    attempt: integer("attempt").notNull(),
    status: runStatusEnum("status").notNull().default("pending"),
    workerId: text("worker_id").references(() => workers.id, { onDelete: "set null" }),
    workspacePath: text("workspace_path"),
    leaseExpiresAt: ts("lease_expires_at"),
    error: text("error"),
    createdAt: ts("created_at").notNull().defaultNow(),
    startedAt: ts("started_at"),
    endedAt: ts("ended_at"),
  },
  (t) => [uniqueIndex("runs_work_item_id_attempt_key").on(t.workItemId, t.attempt)],
);

export const runtimeInstances = pgTable(
  "runtime_instances",
  {
    id: text("id").primaryKey(),
    organizationId: text("organization_id").notNull().references(() => organizations.id, { onDelete: "cascade" }),
    runId: text("run_id").notNull().references(() => runs.id, { onDelete: "cascade" }),
    workerId: text("worker_id").notNull().references(() => workers.id, { onDelete: "cascade" }),
    containerId: text("container_id"),
    imageDigest: text("image_digest"),
    generation: integer("generation").notNull().default(1),
    status: runtimeStatusEnum("status").notNull().default("creating"),
    createdAt: ts("created_at").notNull().defaultNow(),
    startedAt: ts("started_at"),
    stoppedAt: ts("stopped_at"),
    destroyedAt: ts("destroyed_at"),
  },
  (t) => [uniqueIndex("runtime_instances_run_id_generation_key").on(t.runId, t.generation)],
);

export const sessions = pgTable(
  "sessions",
  {
    id: text("id").primaryKey(),
    organizationId: text("organization_id").notNull().references(() => organizations.id, { onDelete: "cascade" }),
    runId: text("run_id").notNull().references(() => runs.id, { onDelete: "cascade" }),
    parentSessionId: text("parent_session_id"),
    role: agentRoleEnum("role").notNull(),
    harness: text("harness").notNull(),
    model: text("model").notNull(),
    status: sessionStatusEnum("status").notNull().default("pending"),
    externalSessionId: text("external_session_id"),
    createdAt: ts("created_at").notNull().defaultNow(),
    endedAt: ts("ended_at"),
  },
  (t) => [index("sessions_run_idx").on(t.runId)],
);

// ---------------------------------------------------------------------------
// Event ledger
// ---------------------------------------------------------------------------

export const events = pgTable(
  "events",
  {
    /** Global monotonic cursor; drives SSE resume. */
    cursor: bigint("cursor", { mode: "number" }).primaryKey().generatedAlwaysAsIdentity(),
    id: text("id").notNull().unique(),
    organizationId: text("organization_id").notNull().references(() => organizations.id, { onDelete: "cascade" }),
    eventType: text("event_type").notNull(),
    occurredAt: ts("occurred_at").notNull().defaultNow(),
    projectId: text("project_id"),
    workItemId: text("work_item_id"),
    runId: text("run_id"),
    sessionId: text("session_id"),
    workflowRunId: text("workflow_run_id"),
    actorType: text("actor_type").$type<"system" | "human" | "agent" | "integration">().notNull(),
    actorId: text("actor_id").notNull(),
    source: text("source").notNull(),
    correlationId: text("correlation_id"),
    causationId: text("causation_id"),
    payload: jsonb("payload").$type<Record<string, unknown>>().notNull().default({}),
  },
  (t) => [
    index("events_session_cursor_idx").on(t.sessionId, t.cursor),
    index("events_run_cursor_idx").on(t.runId, t.cursor),
  ],
);

// ---------------------------------------------------------------------------
// Durable workflow runtime
// ---------------------------------------------------------------------------

export const workflowRuns = pgTable(
  "workflow_runs",
  {
    id: text("id").primaryKey(),
    organizationId: text("organization_id").notNull().references(() => organizations.id, { onDelete: "cascade" }),
    workflowType: text("workflow_type").notNull(),
    idempotencyKey: text("idempotency_key").notNull(),
    status: workflowRunStatusEnum("status").notNull().default("running"),
    step: text("step").notNull(),
    state: jsonb("state").$type<Record<string, unknown>>().notNull().default({}),
    attempt: integer("attempt").notNull().default(0),
    lastError: text("last_error"),
    wakeAt: ts("wake_at"),
    awaitingSignals: jsonb("awaiting_signals").$type<string[]>().notNull().default([]),
    workItemId: text("work_item_id").references(() => workItems.id, { onDelete: "cascade" }),
    runId: text("run_id").references(() => runs.id, { onDelete: "cascade" }),
    lockedBy: text("locked_by"),
    lockedUntil: ts("locked_until"),
    createdAt: ts("created_at").notNull().defaultNow(),
    updatedAt: ts("updated_at").notNull().defaultNow(),
  },
  (t) => [
    uniqueIndex("workflow_runs_org_type_idem_key").on(t.organizationId, t.workflowType, t.idempotencyKey),
  ],
);

export const workflowSignals = pgTable("workflow_signals", {
  id: text("id").primaryKey(),
  organizationId: text("organization_id").notNull().references(() => organizations.id, { onDelete: "cascade" }),
  workflowRunId: text("workflow_run_id").notNull().references(() => workflowRuns.id, { onDelete: "cascade" }),
  name: text("name").notNull(),
  payload: jsonb("payload").$type<Record<string, unknown>>().notNull().default({}),
  idempotencyKey: text("idempotency_key"),
  receivedAt: ts("received_at").notNull().defaultNow(),
  consumedAt: ts("consumed_at"),
});

export const workflowOutbox = pgTable("workflow_outbox", {
  id: bigserial("id", { mode: "number" }).primaryKey(),
  organizationId: text("organization_id").notNull().references(() => organizations.id, { onDelete: "cascade" }),
  workflowRunId: text("workflow_run_id").references(() => workflowRuns.id, { onDelete: "cascade" }),
  kind: text("kind").notNull(),
  payload: jsonb("payload").$type<Record<string, unknown>>().notNull().default({}),
  attempts: integer("attempts").notNull().default(0),
  nextAttemptAt: ts("next_attempt_at").notNull().defaultNow(),
  dispatchedAt: ts("dispatched_at"),
  lastError: text("last_error"),
  createdAt: ts("created_at").notNull().defaultNow(),
});

// ---------------------------------------------------------------------------
// Questions, artifacts, cost
// ---------------------------------------------------------------------------

export const questions = pgTable("questions", {
  id: text("id").primaryKey(),
  organizationId: text("organization_id").notNull().references(() => organizations.id, { onDelete: "cascade" }),
  workItemId: text("work_item_id").references(() => workItems.id, { onDelete: "cascade" }),
  runId: text("run_id").references(() => runs.id, { onDelete: "cascade" }),
  sessionId: text("session_id").references(() => sessions.id, { onDelete: "cascade" }),
  blocking: boolean("blocking").notNull().default(true),
  prompt: text("prompt").notNull(),
  options: jsonb("options").$type<string[]>().notNull().default([]),
  status: questionStatusEnum("status").notNull().default("open"),
  answer: text("answer"),
  answeredBy: text("answered_by").references(() => users.id, { onDelete: "set null" }),
  askedAt: ts("asked_at").notNull().defaultNow(),
  answeredAt: ts("answered_at"),
  expiresAt: ts("expires_at"),
});

export const artifacts = pgTable("artifacts", {
  id: text("id").primaryKey(),
  organizationId: text("organization_id").notNull().references(() => organizations.id, { onDelete: "cascade" }),
  runId: text("run_id").references(() => runs.id, { onDelete: "cascade" }),
  sessionId: text("session_id").references(() => sessions.id, { onDelete: "cascade" }),
  kind: text("kind").notNull(),
  name: text("name").notNull(),
  contentType: text("content_type").notNull().default("application/octet-stream"),
  sizeBytes: bigint("size_bytes", { mode: "number" }).notNull().default(0),
  storageKey: text("storage_key").notNull(),
  sha256: text("sha256").notNull(),
  createdAt: ts("created_at").notNull().defaultNow(),
});

export const costSamples = pgTable("cost_samples", {
  id: bigserial("id", { mode: "number" }).primaryKey(),
  organizationId: text("organization_id").notNull().references(() => organizations.id, { onDelete: "cascade" }),
  projectId: text("project_id").references(() => projects.id, { onDelete: "cascade" }),
  workItemId: text("work_item_id").references(() => workItems.id, { onDelete: "cascade" }),
  runId: text("run_id").references(() => runs.id, { onDelete: "cascade" }),
  sessionId: text("session_id").references(() => sessions.id, { onDelete: "cascade" }),
  role: agentRoleEnum("role"),
  model: text("model").notNull(),
  inputTokens: bigint("input_tokens", { mode: "number" }).notNull().default(0),
  outputTokens: bigint("output_tokens", { mode: "number" }).notNull().default(0),
  cachedTokens: bigint("cached_tokens", { mode: "number" }).notNull().default(0),
  costUsd: numeric("cost_usd", { precision: 12, scale: 6 }).notNull().default("0"),
  occurredAt: ts("occurred_at").notNull().defaultNow(),
});
