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
  /**
   * Appended to this role's prompt for this project.
   *
   * Per role rather than one project-wide blob: the tester needs to know how
   * to start the app and log in, the reviewer needs to know what this
   * codebase considers a defect, and sending all of it to all of them would
   * spend context on every turn to say nothing.
   */
  context: z.string().max(20_000).optional(),
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
  projectId: z.string().optional(),
  /** Stable internal name used in workspace paths. */
  name: z.string().min(1),
  /** Clone URL; may be a local path for the local provisioner. */
  url: z.string().min(1),
  defaultBranch: z.string().min(1).default("main"),
  /** Trust class — plan §47. Governs network/credential posture. */
  trust: z.enum(["trusted_internal", "untrusted_external"]).default("trusted_internal"),
});
export type Repository = z.infer<typeof repositorySchema>;

export const findingSeveritySchema = z.enum(["blocking", "high", "medium", "low", "note"]);

/**
 * The reviewer flavours the factory has prompts for. A project names the
 * ones every delivery runs; others join when a change touches their paths.
 */
export const REVIEWER_CATEGORIES = ["correctness", "security", "database", "api", "frontend", "performance"] as const;

/**
 * How a project's work is delivered, over the factory's defaults. Each field
 * left out keeps the default; a work item's own overrides layer on top.
 */
export const deliveryPolicySchema = z
  .object({
    requiredReviewers: z.array(z.enum(REVIEWER_CATEGORIES)).min(1),
    blockingSeverities: z.array(findingSeveritySchema).min(1),
    maxReviewIterations: z.number().int().min(1).max(20),
    maxPrFixIterations: z.number().int().min(0).max(20),
    simplify: z.boolean(),
  })
  .partial()
  .strict();
export type DeliveryPolicy = z.infer<typeof deliveryPolicySchema>;
/** A delivery policy with every field set: the factory's defaults, or a project's over them. */
export type FullDeliveryPolicy = { [K in keyof DeliveryPolicy]-?: NonNullable<DeliveryPolicy[K]> };

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
  /** How its work is delivered, over the factory's defaults. */
  deliveryPolicy: deliveryPolicySchema.default({}),
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
  /** Its place among the project's epics, 0 first: a statement of priority. */
  position: z.number().int().default(0),
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
  /** What people call it: the project's prefix and a number (TK-12). */
  key: z.string().optional(),
  /** The repository it changes; null means the project's only one. */
  repositoryId: z.string().nullable().default(null),
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
  /** Why the Run failed, when it did. */
  error: z.string().nullable().default(null),
  /**
   * Which step of the delivery workflow this Run is. Null for a Run created
   * directly through the API, which executes as an orchestrator.
   */
  phase: z
    .enum(["investigate", "implement", "review", "fix", "simplify", "test"])
    .nullable()
    .default(null),
  role: agentRoleSchema.nullable().default(null),
  /** Review phase: which reviewer flavour this Run is. */
  category: z.string().nullable().default(null),
  /** The Run this one continues from. */
  parentRunId: z.string().nullable().default(null),
  /** The commit its workspace started at; null means the default branch. */
  baseRef: z.string().nullable().default(null),
  /** What it produced, for the next phase to build on. */
  headSha: z.string().nullable().default(null),
  branch: z.string().nullable().default(null),
  /**
   * Which coding agent ran it ("opencode", "claude-code", "scripted"…) and
   * its model. For presentation only: everything the agent reported has
   * already been translated into dude's own events.
   */
  harness: z.string().nullable().default(null),
  model: z.string().nullable().default(null),
  /** Tokens as the agent reported them. Context is the latest size, not a sum. */
  tokens: z
    .object({
      input: z.number(),
      output: z.number(),
      cacheRead: z.number(),
      cacheWrite: z.number(),
      context: z.number(),
    })
    .default({ input: 0, output: 0, cacheRead: 0, cacheWrite: 0, context: 0 }),
  createdAt: z.string().datetime({ offset: true }),
  startedAt: z.string().datetime({ offset: true }).nullable().default(null),
  endedAt: z.string().datetime({ offset: true }).nullable().default(null),
});
export type Run = z.infer<typeof runSchema>;

/** The role a Run executes as when it has no phase: one created by hand. */
export const DEFAULT_RUN_ROLE: AgentRole = "orchestrator";

/**
 * How a Run is named to a person: its phase, and for a review its category.
 * "Review · security", "Fix", "Agent" for a Run with no phase at all.
 */
export function runLabel(run: { phase?: string | null; category?: string | null }): string {
  if (!run.phase) return "Agent";
  const phase = run.phase.charAt(0).toUpperCase() + run.phase.slice(1);
  return run.category ? `${phase} · ${run.category}` : phase;
}

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

// ---------------------------------------------------------------------------
// Review findings and pull requests
//
// Defined here rather than beside the routes that serve them so the web
// client, the workflow and the forge integration share one vocabulary —
// a severity or PR state spelled differently in two places is a bug waiting
// for the day one of them changes.
// ---------------------------------------------------------------------------

export type FindingSeverity = z.infer<typeof findingSeveritySchema>;

export const findingStatusSchema = z.enum(["open", "resolved", "superseded", "accepted"]);
export type FindingStatus = z.infer<typeof findingStatusSchema>;

export const findingSchema = z.object({
  id: z.string(),
  workItemId: z.string(),
  runId: z.string().nullable(),
  category: z.string(),
  severity: findingSeveritySchema,
  status: findingStatusSchema,
  file: z.string().nullable(),
  line: z.number().int().nullable(),
  title: z.string(),
  description: z.string(),
  suggestedFix: z.string(),
  resolutionNote: z.string(),
  /** The Run whose judgement closed it, when a re-review did. */
  resolvedByRunId: z.string().nullable().default(null),
  fixAttempts: z.number().int(),
  createdAt: z.string(),
});
export type Finding = z.infer<typeof findingSchema>;

export const pullRequestStateSchema = z.enum(["draft", "open", "merged", "closed"]);
export const checkStateSchema = z.enum(["pending", "passing", "failing", "unknown"]);
export const reviewStateSchema = z.enum(["pending", "approved", "changes_requested"]);

export const pullRequestSchema = z.object({
  id: z.string(),
  workItemId: z.string(),
  runId: z.string().nullable(),
  number: z.number().int(),
  url: z.string(),
  headBranch: z.string(),
  baseBranch: z.string(),
  title: z.string(),
  state: pullRequestStateSchema,
  checks: checkStateSchema,
  review: reviewStateSchema,
  createdAt: z.string(),
  updatedAt: z.string(),
});
export type PullRequest = z.infer<typeof pullRequestSchema>;
