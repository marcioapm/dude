import { z } from "zod";

/**
 * Product hierarchy — plan §39.
 *
 *   Organization → Project → Epic → Work Item → Run → Session
 *
 * Sessions are execution detail, deliberately not the user-facing unit:
 * they get restarted, forked, replaced by another harness, or multiplied
 * by subagents, and none of that should change a user's mental model.
 */

// ---------------------------------------------------------------------------
// Agent roles and model selection
// ---------------------------------------------------------------------------

/** Roles a Session can play — plan §30 (the first agents). */
export const agentRoleSchema = z.enum([
  "orchestrator",
  "investigator",
  "implementer",
  "reviewer",
  "simplifier",
  "qa_browser",
]);
export type AgentRole = z.infer<typeof agentRoleSchema>;

export const ALL_AGENT_ROLES = agentRoleSchema.options;

/**
 * Model binding for one role. `harness` is optional so a project can
 * express "any harness that satisfies the capabilities" and let policy
 * pick — plan §45 capability negotiation.
 */
export const agentModelConfigSchema = z.object({
  model: z.string().min(1),
  harness: z.string().min(1).optional(),
  /** Overrides the harness default when set. */
  maxTokens: z.number().int().positive().optional(),
  temperature: z.number().min(0).max(2).optional(),
  /** Hard ceiling for one Session in this role, in USD. */
  costLimitUsd: z.number().positive().optional(),
});
export type AgentModelConfig = z.infer<typeof agentModelConfigSchema>;

/**
 * Per-project, per-role model configuration. Partial: any role left unset
 * falls back to the organization default, then to the system default.
 */
export const agentModelsSchema = z.record(agentRoleSchema, agentModelConfigSchema).default({});
export type AgentModels = z.infer<typeof agentModelsSchema>;

// ---------------------------------------------------------------------------
// Organization
// ---------------------------------------------------------------------------

export const organizationSchema = z.object({
  id: z.string(),
  name: z.string().min(1),
  slug: z.string().min(1),
  /** Org-wide fallback for roles a project does not configure. */
  defaultAgentModels: agentModelsSchema,
  createdAt: z.string().datetime({ offset: true }),
});
export type Organization = z.infer<typeof organizationSchema>;

export const orgRoleSchema = z.enum(["owner", "admin", "member", "viewer"]);
export type OrgRole = z.infer<typeof orgRoleSchema>;

// ---------------------------------------------------------------------------
// Project
// ---------------------------------------------------------------------------

export const repositorySchema = z.object({
  id: z.string(),
  /** Stable internal name used in workspace paths. */
  name: z.string().min(1),
  /** Clone URL; may be a local path for the local provisioner. */
  url: z.string().min(1),
  defaultBranch: z.string().min(1).default("main"),
  /** Trust class — plan §47. Governs network/credential posture. */
  trustClass: z.enum(["trusted_internal", "untrusted_external"]).default("trusted_internal"),
});
export type Repository = z.infer<typeof repositorySchema>;

export const projectSchema = z.object({
  id: z.string(),
  organizationId: z.string(),
  name: z.string().min(1),
  slug: z.string().min(1),
  description: z.string().default(""),
  repositories: z.array(repositorySchema).default([]),
  /** Per-role model selection for this project. */
  agentModels: agentModelsSchema,
  /** Container image for Run runtimes; null uses the system default. */
  runtimeImage: z.string().nullable().default(null),
  createdAt: z.string().datetime({ offset: true }),
});
export type Project = z.infer<typeof projectSchema>;

// ---------------------------------------------------------------------------
// Epic / Work Item
// ---------------------------------------------------------------------------

export const epicSchema = z.object({
  id: z.string(),
  organizationId: z.string(),
  projectId: z.string(),
  title: z.string().min(1),
  description: z.string().default(""),
  createdAt: z.string().datetime({ offset: true }),
});
export type Epic = z.infer<typeof epicSchema>;

/** Macro state machine — plan §6.1. */
export const workItemStatusSchema = z.enum([
  "received",
  "intake",
  "awaiting_confirmation",
  "queued",
  "running",
  "awaiting_input",
  "review",
  "ready_to_merge",
  "done",
  "failed",
  "aborted",
]);
export type WorkItemStatus = z.infer<typeof workItemStatusSchema>;

/** Every work item status, in lifecycle order. */
export const ALL_WORK_ITEM_STATUSES = workItemStatusSchema.options;

/** States in which no Run should be consuming tokens. */
export const TERMINAL_WORK_ITEM_STATUSES: readonly WorkItemStatus[] = [
  "done",
  "failed",
  "aborted",
];

export const workItemSchema = z.object({
  id: z.string(),
  organizationId: z.string(),
  projectId: z.string(),
  epicId: z.string().nullable().default(null),
  title: z.string().min(1),
  goal: z.string().default(""),
  acceptanceCriteria: z.array(z.string()).default([]),
  status: workItemStatusSchema,
  requestedBy: z.string().nullable().default(null),
  createdAt: z.string().datetime({ offset: true }),
  updatedAt: z.string().datetime({ offset: true }),
});
export type WorkItem = z.infer<typeof workItemSchema>;

// ---------------------------------------------------------------------------
// Run / Session
// ---------------------------------------------------------------------------

/** A Run is one execution attempt; retrying never erases a prior attempt. */
export const runStatusSchema = z.enum([
  "pending",
  "scheduled",
  "starting",
  "running",
  "paused",
  "completed",
  "failed",
  "aborted",
]);
export type RunStatus = z.infer<typeof runStatusSchema>;

/** Every run status, in lifecycle order. */
export const ALL_RUN_STATUSES = runStatusSchema.options;

export const TERMINAL_RUN_STATUSES: readonly RunStatus[] = ["completed", "failed", "aborted"];

export const runSchema = z.object({
  id: z.string(),
  organizationId: z.string(),
  projectId: z.string(),
  workItemId: z.string(),
  attempt: z.number().int().positive(),
  status: runStatusSchema,
  workerId: z.string().nullable().default(null),
  workspacePath: z.string().nullable().default(null),
  createdAt: z.string().datetime({ offset: true }),
  startedAt: z.string().datetime({ offset: true }).nullable().default(null),
  endedAt: z.string().datetime({ offset: true }).nullable().default(null),
});
export type Run = z.infer<typeof runSchema>;

export const sessionStatusSchema = z.enum([
  "pending",
  "running",
  "awaiting_input",
  "completed",
  "failed",
  "aborted",
]);
export type SessionStatus = z.infer<typeof sessionStatusSchema>;

/** Every session status, in lifecycle order. */
export const ALL_SESSION_STATUSES = sessionStatusSchema.options;

export const sessionSchema = z.object({
  id: z.string(),
  organizationId: z.string(),
  runId: z.string(),
  /** Parent Session for subagents; null for the root orchestrator. */
  parentSessionId: z.string().nullable().default(null),
  role: agentRoleSchema,
  harness: z.string().min(1),
  model: z.string().min(1),
  status: sessionStatusSchema,
  /** Harness-native session ID, kept beside our stable ID (plan §31). */
  externalSessionId: z.string().nullable().default(null),
  createdAt: z.string().datetime({ offset: true }),
  endedAt: z.string().datetime({ offset: true }).nullable().default(null),
});
export type Session = z.infer<typeof sessionSchema>;

/**
 * Resolve the model config for a role: project → organization → default.
 * Returns null when no layer configures the role.
 */
export function resolveAgentModel(
  role: AgentRole,
  project: Pick<Project, "agentModels">,
  organization: Pick<Organization, "defaultAgentModels">,
  systemDefaults: AgentModels = {},
): AgentModelConfig | null {
  return (
    project.agentModels[role] ?? organization.defaultAgentModels[role] ?? systemDefaults[role] ?? null
  );
}
