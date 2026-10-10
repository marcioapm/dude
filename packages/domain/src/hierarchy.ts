import { z } from "zod";
import { harnessSchema } from "./harnesses.ts";

/**
 * Product hierarchy — plan §39.
 *
 *   Organization → Project → Epic → Task → Run → Session
 *
 * Sessions are execution detail, deliberately not the user-facing unit:
 * they get restarted, forked, replaced by another harness, or multiplied
 * by subagents, and none of that should change a user's mental model.
 */

// ---------------------------------------------------------------------------
// Agent roles and model selection
// ---------------------------------------------------------------------------

/**
 * Roles a Session can play — plan §30 (the first agents). The conductor is
 * the agent people talk to in a task's Chat (it was the orchestrator); the
 * brainstorm, the one a brainstorm session's members talk to.
 */
export const agentRoleSchema = z.enum([
  "conductor",
  "investigator",
  "implementer",
  "reviewer",
  "simplifier",
  "qa_browser",
  "brainstorm",
]);
export type AgentRole = z.infer<typeof agentRoleSchema>;

export const ALL_AGENT_ROLES = agentRoleSchema.options;

/**
 * How long a phase Run may go without progress before its owner is told,
 * in minutes: from half an hour up to a week, 2 hours when unset. Runs are
 * stopped at 4 hours whatever this says (the deployment's agent.timeout).
 */
export const TIME_LIMIT_MIN_MINUTES = 30;
export const DEFAULT_TIME_LIMIT_MINUTES = 120;
export const timeLimitMinutesSchema = z.number().int().min(TIME_LIMIT_MIN_MINUTES).max(10_080);
/** A stored limit as it is read: one saved below the minimum, when it was lower, is the minimum. */
export function clampTimeLimit(minutes: number): number {
  return Math.max(minutes, TIME_LIMIT_MIN_MINUTES);
}

// The scripted agent's models (orchestrator/internal/fakeagent): a tier may request them, for tests.
export const TEST_HARNESS_MODELS = ["fake/scripted", "fake/hang", "fake/tools", "fake/request", "fake/wait", "fake/live", "fake/ask", "fake/ask-several", "fake/command", "fake/stuck", "fake/stall", "fake/silent", "fake/lookup"] as const;
/** What a role that still names a model is told. */
export const ROLE_MODEL_REMOVED = "a role names a model tier (`tier`, one of the organization's tiers), not a model";
/** What a settings change that still names a role's effort is told. */
export const ROLE_EFFORT_REMOVED = "reasoning effort is the model tier's (set it in Models), not the role's";

/**
 * Model binding for one role: the tier it asks for and, apart from it,
 * the harness that runs it (OpenCode when no layer names one).
 */
export const agentModelConfigSchema = z.object({
  /**
   * The model tier its sessions run on (an organization's tier id).
   * Optional at each layer: a project that changes only a role's time
   * limit keeps its organization's tier (resolveTier, field by field).
   */
  tier: z.string().min(1).max(100).optional(),
  model: z.undefined({ invalid_type_error: ROLE_MODEL_REMOVED }),
  /** The coding agent its Runs run on; layered like machineSize (resolveHarness). */
  harness: harnessSchema.optional(),
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
  /** How long a Run may go without progress before its owner is told, in minutes. */
  timeLimitMinutes: timeLimitMinutesSchema.optional(),
  /** The machine size its sessions run on (an organization's size id); unset is the default size. */
  machineSize: z.string().min(1).optional(),
  /** The image its sessions run in (an image library id); unset falls through to the project's image. */
  image: z.string().min(1).optional(),
});
export type AgentModelConfig = z.infer<typeof agentModelConfigSchema>;

/**
 * Per-project, per-role model configuration. Partial: any role left unset
 * falls back to the organization default, then to the system default.
 */
export const agentModelsSchema = z
  // The fixer is the implementer told something else; it takes the
  // implementer's settings except where it is given its own.
  .record(z.union([agentRoleSchema, z.literal("fixer")]), agentModelConfigSchema)
  .default({});
export type AgentModels = z.infer<typeof agentModelsSchema>;

// Stored harness names can outlive an adapter; public inputs remain strict.
export const storedAgentModelsSchema = z.record(
  z.union([agentRoleSchema, z.literal("fixer")]),
  agentModelConfigSchema.extend({ harness: harnessSchema.optional().catch(undefined) }),
).default({});

// ---------------------------------------------------------------------------
// Organization
// ---------------------------------------------------------------------------

export const organizationSchema = z.object({
  id: z.string(),
  name: z.string().min(1),
  slug: z.string().min(1),
  /** Org-wide fallback for roles a project does not configure. */
  defaultAgentModels: storedAgentModelsSchema,
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
export type ReviewerCategory = (typeof REVIEWER_CATEGORIES)[number];

/** A reviewer flavour as a person reads it. */
export const REVIEWER_CATEGORY_LABEL: Record<ReviewerCategory, string> = {
  correctness: "Correctness",
  security: "Security",
  database: "Database",
  api: "API",
  frontend: "Frontend",
  performance: "Performance",
};

/**
 * How a project's work is delivered, over the factory's defaults. Each field
 * left out keeps the default; a task's own overrides layer on top.
 */
export const deliveryPolicySchema = z
  .object({
    requiredReviewers: z.array(z.enum(REVIEWER_CATEGORIES)).min(1),
    blockingSeverities: z.array(findingSeveritySchema).min(1),
    maxReviewIterations: z.number().int().min(1).max(20),
    /** Fix attempts one finding may survive before it is escalated on its own. */
    maxAttemptsPerFinding: z.number().int().min(1).max(20),
    maxPrFixIterations: z.number().int().min(0).max(20),
    simplify: z.boolean(),
    /** A browser tester exercises the change and publishes a video, before the pull request. */
    test: z.boolean(),
    /** Minutes an agent waiting on a person stays live before it is parked. */
    parkAfterMinutes: z.number().int().min(1).max(1440),
    /** Minutes an agent may be quiet mid-turn before it is nudged; 0 never. */
    idleNudgeMinutes: z.number().int().min(0).max(1440),
    /** Minutes a task's conductor stays running after its turn, before it is parked. */
    conductorWarmMinutes: z.number().int().min(1).max(1440),
    /** The most changed lines (additions and deletions) a conductor may publish at once; past it, it delegates. */
    conductorEditLines: z.number().int().min(1).max(10_000),
    /** The most changed files a conductor may publish at once. */
    conductorEditFiles: z.number().int().min(1).max(1000),
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
  /** The start of its tasks' keys (BILL-12), unique in its organisation; fixed once made. */
  key: z.string().min(1),
  description: z.string().default(""),
  repositories: z.array(repositorySchema).default([]),
  /** Per-role model selection for this project. */
  agentModels: storedAgentModelsSchema,
  /**
   * A container image typed by hand, from before the image library: used
   * only when runtimeImageId is null. The API takes it only as null (clear).
   */
  runtimeImage: z.string().nullable().default(null),
  /** The library image its agents run in; null: the organization's default base. */
  runtimeImageId: z.string().nullable().default(null),
  /** How its work is delivered, over the factory's defaults. */
  deliveryPolicy: deliveryPolicySchema.default({}),
  createdAt: z.string().datetime({ offset: true }),
  /** Its face: an uploaded image's URL, or null for initials. */
  imageUrl: z.string().nullable().default(null),
});
export type Project = z.infer<typeof projectSchema>;

// ---------------------------------------------------------------------------
// Epic / Task
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
export const taskStatusSchema = z.enum([
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
export type TaskStatus = z.infer<typeof taskStatusSchema>;

/** Every task status, in lifecycle order. */
export const ALL_TASK_STATUSES = taskStatusSchema.options;

/** States in which no Run should be consuming tokens. */
export const TERMINAL_TASK_STATUSES: readonly TaskStatus[] = [
  "done",
  "failed",
  "aborted",
];

/**
 * A repository a task works on: one it may change (`write`, and a
 * pull request if it does) or only read, for context.
 */
export const taskRepositorySchema = z.object({
  id: z.string().min(1),
  access: z.enum(["write", "read"]).default("write"),
});
export type TaskRepository = z.infer<typeof taskRepositorySchema>;

/**
 * A person, wherever the API names one: an organization's member, by the
 * id of their `people` row. `online` is whether they were seen in the last
 * five minutes. `photoUrl` is an https URL or one the backend serves.
 */
export const personRefSchema = z.object({
  id: z.string(),
  name: z.string(),
  photoUrl: z.string().nullable(),
  online: z.boolean(),
});
export type PersonRef = z.infer<typeof personRefSchema>;

/** Organization admins manage members and the organization's settings. */
export const personRoleSchema = z.enum(["admin", "member"]);
export type PersonRole = z.infer<typeof personRoleSchema>;

/** A person as `GET /v1/me` and `GET /v1/people` describe them. */
export interface PersonDetail extends PersonRef {
  email: string | null;
  role: PersonRole;
  lastSeenAt: string | null;
}

/** A person, by id and name; any `PersonRef` is one. */
export const personSchema = z.object({ id: z.string(), name: z.string() });
export type Person = z.infer<typeof personSchema>;

/** Longest goal a task takes, in characters (UTF-16 units, as `.length`). */
export const TASK_GOAL_MAX = 65_536;
/**
 * Shortest goal a task is saved with, in the same units, counted with
 * surrounding whitespace trimmed. The agents' `create_task` keeps the same
 * rule (`GoalMin` in orchestrator/internal/agenttools).
 */
export const TASK_GOAL_MIN = 16;
export const TASK_GOAL_TOO_SHORT =
  `a task needs a goal of at least ${TASK_GOAL_MIN} characters: why it matters and what should change`;
/** A short goal as a validation error on `goal`, shaped like zod's `flatten()`. */
export const TASK_GOAL_TOO_SHORT_DETAILS = { formErrors: [], fieldErrors: { goal: [TASK_GOAL_TOO_SHORT] } };

/** How many more characters `goal` needs to be saved; 0 when it has enough. */
export function taskGoalShortBy(goal: string): number {
  return Math.max(0, TASK_GOAL_MIN - goal.trim().length);
}
/** Longest a task's acceptance criteria are all together; one criterion has no limit of its own. */
export const TASK_CRITERIA_MAX = 16_384;

/** A task's goal as the API takes it. */
export const taskGoalInput = z.string().max(TASK_GOAL_MAX);
/** A task's acceptance criteria as the API takes them: bounded in total, not each. */
export const taskCriteriaInput = z.array(z.string()).refine(
  (criteria) => criteria.reduce((n, c) => n + c.length, 0) <= TASK_CRITERIA_MAX,
  `acceptance criteria can be at most ${TASK_CRITERIA_MAX.toLocaleString("en-US")} characters in all`,
);

/** Who takes a task's delivery decisions: Deliver ("policy") or its conductor. */
export const deciderSchema = z.enum(["policy", "conductor"]);
export type Decider = z.infer<typeof deciderSchema>;

/** Where a conducted delivery waits on its conductor (the orchestrator's decision points). */
export const decisionPointSchema = z.enum([
  "start",
  "after_implement",
  "after_review",
  "after_fix",
  "before_pull_request",
  "pull_request_feedback",
]);
export type DecisionPoint = z.infer<typeof decisionPointSchema>;

/** A decision point in a person's words. */
export const DECISION_POINT_LABEL: Record<DecisionPoint, string> = {
  start: "whether to start building",
  after_implement: "what to do after the implementer",
  after_review: "what to do with the review's findings",
  after_fix: "what to do after the fix",
  before_pull_request: "whether to open the pull request",
  pull_request_feedback: "what to do with the pull request's feedback",
};

export const taskSchema = z.object({
  id: z.string(),
  organizationId: z.string(),
  projectId: z.string(),
  epicId: z.string().nullable().default(null),
  /** What people call it: the project's prefix and a number (TK-12). */
  key: z.string().optional(),
  /** The repositories it works on; none is work that changes no code. */
  repositories: z.array(taskRepositorySchema).default([]),
  title: z.string().min(1),
  goal: z.string().default(""),
  acceptanceCriteria: z.array(z.string()).default([]),
  status: taskStatusSchema,
  requestedBy: z.string().nullable().default(null),
  /**
   * Who drives it: told when it waits on someone, and the only one who
   * answers its agents. Null for a task nobody owns, which anyone may
   * answer for.
   */
  owner: personRefSchema.nullable().default(null),
  /** Everyone on it, the owner first. */
  people: z.array(personRefSchema).default([]),
  /**
   * Who takes its delivery's decisions: Deliver's fixed rules ("policy"),
   * or its conductor, in Chat with its people ("conductor").
   */
  decider: deciderSchema.default("policy"),
  /** A person let Deliver finish its delivery: a later message in Chat does not take the decisions over again. */
  handedBack: z.boolean().default(false),
  /** The decision its delivery waits on for the conductor, or null. */
  awaitingDecision: z.object({ point: decisionPointSchema }).nullable().default(null),
  createdAt: z.string().datetime({ offset: true }),
  updatedAt: z.string().datetime({ offset: true }),
});
export type Task = z.infer<typeof taskSchema>;

/**
 * The delivery workflow stopping to ask for a person (a `question.asked`
 * of kind "escalation"): why, in the workflow's word — "stuck",
 * "no_changes", "implement_failed", … — and what it knew. Terminal: the
 * workflow does not go on by itself. A task carries its open one while it
 * waits (`GET /v1/tasks/:id`, the navigation tree).
 */
/**
 * A person's decision on an escalation: resume the agent whose Run failed,
 * where it stopped (while it is kept); try the step that stopped again;
 * accept the findings a review got stuck on and go on; take what was
 * merged as the task; wait on the pull requests still open; or stop.
 */
export const escalationActionSchema = z.enum(["resume", "retry", "accept", "done", "wait", "stop"]);
export type EscalationAction = z.infer<typeof escalationActionSchema>;

/**
 * Picking a stopped (aborted or failed) task back up: resume the agents
 * that stopped, where they stopped, while they are kept; try the step
 * again with new ones on the same branch; or start over as a new attempt
 * on a new branch.
 */
export const recoverActionSchema = z.enum(["resume", "retry", "restart"]);
export type RecoverAction = z.infer<typeof recoverActionSchema>;

/** How a stopped task can be picked back up (`GET /v1/tasks/:id/recover`). */
export const recoveryOptionsSchema = z.object({
  taskId: z.string(),
  /** The ways open now, the one that fits best first; empty for a task that has not stopped. */
  actions: z.array(recoverActionSchema),
  /** The task's attempt: Start over makes the next. */
  attempt: z.number().int(),
  /** Until when a resume can: when the first of the kept Runs stops being kept. */
  keptUntil: z.string().nullable(),
});
export type RecoveryOptions = z.infer<typeof recoveryOptionsSchema>;

export const escalationSchema = z.object({
  reason: z.string(),
  /** Per reason: `runId` and `error` for a phase that failed, `findingIds`, `iterations`, PR counts. */
  detail: z.record(z.string(), z.unknown()).nullable().default(null),
  /** What its owner may decide (`POST /v1/tasks/:id/decide`). */
  actions: z.array(escalationActionSchema).default([]),
  at: z.string(),
});
export type Escalation = z.infer<typeof escalationSchema>;

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
  taskId: z.string(),
  attempt: z.number().int().positive(),
  status: runStatusSchema,
  workerId: z.string().nullable().default(null),
  workspacePath: z.string().nullable().default(null),
  /** Why the Run failed, when it did. */
  error: z.string().nullable().default(null),
  /** An agent's, or a branch preview's: no agent, the task's branch serving its servers. */
  kind: z.enum(["agent", "preview"]).default("agent"),
  /**
   * Which step of the delivery workflow this Run is. Null for a task's
   * conductor (role conductor), and for a Run created directly through the API.
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
  /** The task's conductor, when it started this Run; null for Deliver's. */
  conductorRunId: z.string().nullable().default(null),
  /** The commit each repository started at, by name; one not named started at its default branch. */
  baseRefs: z.record(z.string(), z.string()).default({}),
  /** Where it left each repository it changed, by name: what the next phase builds on. */
  heads: z.record(z.string(), z.string()).default({}),
  branch: z.string().nullable().default(null),
  /**
   * Which coding agent ran it ("opencode", "claude-code", "scripted"…) and
   * its model. For presentation only: everything the agent reported has
   * already been translated into dude's own events.
   */
  harness: z.string().nullable().default(null),
  /** The model dude requested when it was submitted; what the proxy served is the proxy's to say. */
  model: z.string().nullable().default(null),
  /** The tier's name then; null for a Run from before tiers. */
  modelTier: z.string().nullable().default(null),
  /** The tier's reasoning effort then; null for the model's default. */
  effort: z.string().nullable().default(null),
  /**
   * Why dude paused it itself, and so what takes it up again: "person" —
   * parked while it waits for an answer or a decision, which resumes it;
   * "idle" — parked after going quiet, until a person resumes it;
   * "repository" — stopped a moment to bring one in; "unused" — a branch
   * preview nobody opened for a while, until a person starts a server;
   * "conductor" — a task's conductor past its warm period, until someone
   * writes in its Chat. Null
   * when not paused, or when a person paused it.
   */
  dudePause: z.enum(["person", "idle", "repository", "unused", "conductor"]).nullable().default(null),
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
  /**
   * What it runs on: the size its spec was built with, then, from each
   * resume, the size its settings named then as lux applied it. Null for
   * a Run from before sizes, or not yet submitted.
   */
  machine: z
    .object({
      sizeId: z.string().nullable(),
      name: z.string(),
      cpus: z.number(),
      memoryMiB: z.number(),
      diskGiB: z.number(),
      /** The lux pool's id; null: the organisation's default pool. */
      poolId: z.string().nullable(),
      /** The pool's name in lux when the Run was submitted. */
      pool: z.string().nullable(),
      from: z.string().optional(),
      /** The memory limit lux gave its container, in bytes, when lux reports one. */
      memoryLimit: z.number().nullable().optional(),
      /** Why it is not on the size its settings name, said at the resume that could not move it. */
      note: z.string().optional(),
      /** A smaller disk lux would not apply at a resume: `diskGiB` is the disk it kept. */
      diskKept: z.object({ requestedGiB: z.number(), reason: z.string().optional() }).optional(),
    })
    .nullable()
    .default(null),
  /**
   * The library image it got ({imageId, name, versionId, version, ref,
   * layer}), fixed when it was resolved; null for a Run on a typed image or
   * dude's fallback.
   */
  image: z
    .object({
      imageId: z.string(),
      name: z.string(),
      versionId: z.string(),
      version: z.number(),
      ref: z.string(),
      layer: z.string(),
    })
    .nullable()
    .default(null),
  /**
   * Whether it asked lux to let it start containers inside it, as recorded
   * when it was submitted; null for a Run not yet submitted, or from
   * before dude recorded it.
   */
  canRunContainers: z.boolean().nullable().default(null),
  /**
   * While it waits for its image (its dude layer, or its first version): the
   * job, and where it is; builderOfflineSince when the builder has not been
   * heard from for 2 minutes, from its last heartbeat (or, never seen, from
   * when the Run began to wait).
   */
  preparingImage: z
    .object({
      buildId: z.string(),
      state: z.string(),
      // build: its image's first version; finish: the dude layer added to it.
      kind: z.enum(["build", "finish"]).default("finish"),
      imageName: z.string(),
      version: z.number().nullable(),
      builderOfflineSince: z.string().datetime({ offset: true }).nullable().default(null),
    })
    .nullable()
    .default(null),
  createdAt: z.string().datetime({ offset: true }),
  startedAt: z.string().datetime({ offset: true }).nullable().default(null),
  endedAt: z.string().datetime({ offset: true }).nullable().default(null),
  /**
   * While it makes no progress, as dude last reported it (run.stalled):
   * when, the report in words, and whether its owner was told (a plain
   * delivery; a conductor's task tells the conductor) and left it as it
   * is. Null once it makes progress or ends.
   */
  stalled: z
    .object({
      at: z.string().datetime({ offset: true }),
      text: z.string(),
      owner: z.boolean(),
      left: z.boolean(),
    })
    .nullable()
    .default(null),
  /** The Run that replaced it when it was restarted; null for none. */
  replacedBy: z.string().nullable().default(null),
});
export type Run = z.infer<typeof runSchema>;

/** The role a Run executes as when it has no phase: one created by hand. */
export const DEFAULT_RUN_ROLE: AgentRole = "conductor";

/** A task's conductor: the Run people talk to in its Chat, which is no phase. */
export function isConductor(run: { phase?: string | null; role?: string | null; kind?: string }): boolean {
  return run.role === "conductor" && !run.phase && run.kind !== "preview";
}

/**
 * How a Run is named to a person: its phase, and for a review its category.
 * "Review · security", "Fix", "Conductor", "Agent" for a Run with no phase at all.
 */
export function runLabel(run: { phase?: string | null; category?: string | null; kind?: string; role?: string | null }): string {
  if (run.kind === "preview") return "Branch preview";
  if (isConductor(run)) return "Conductor";
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
 * Resolve the model config for a role: project → organization → default,
 * field by field, so a project that sets only a role's time limit keeps its
 * organization's model. Returns null when no layer configures the role.
 */
export function resolveAgentModel(
  role: AgentRole,
  project: Pick<Project, "agentModels">,
  organization: Pick<Organization, "defaultAgentModels">,
  systemDefaults: AgentModels = {},
): AgentModelConfig | null {
  const layers = [systemDefaults[role], organization.defaultAgentModels[role], project.agentModels[role]].filter(Boolean);
  return layers.length ? Object.assign({}, ...layers) : null;
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
  taskId: z.string(),
  runId: z.string().nullable(),
  category: z.string(),
  /** The reviewer's own word for it, when it differs from its flavour (category). */
  topic: z.string().nullish(),
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

/**
 * A check on a pull request's head, by the name GitHub shows (`PrCheck`).
 * An entry with `diagnostic` is no check but a source that could not be
 * read (`prCheckDiagnostic`).
 */
export const prCheckSchema = z.object({
  name: z.string(),
  status: z.string(),
  conclusion: z.string().nullable(),
  url: z.string().nullable().optional(),
  durationMs: z.number().nullable().optional(),
  diagnostic: z.string().nullable().optional(),
});
/** A reviewer's latest word (`PrReview`); `REQUESTED` for one asked who has not answered. */
export const prReviewSchema = z.object({
  login: z.string(),
  state: z.string(),
  submittedAt: z.string().nullable().optional(),
  avatarUrl: z.string().optional(),
  /** A team asked ("org/slug"), not a person. */
  team: z.boolean().optional(),
  /** Asked again since this verdict: it stands, but they owe another look. */
  rerequested: z.boolean().optional(),
});

export const pullRequestSchema = z.object({
  id: z.string(),
  taskId: z.string(),
  runId: z.string().nullable(),
  /** The repository it is in: one per repository a task changed. */
  repositoryId: z.string(),
  repositoryName: z.string(),
  number: z.number().int(),
  url: z.string(),
  headBranch: z.string(),
  baseBranch: z.string(),
  title: z.string(),
  state: pullRequestStateSchema,
  /** Every check by name, as GitHub reports them. */
  checks: z.array(prCheckSchema),
  /**
   * The checks' verdict as dude decides on it: pending too for a new head
   * CI has yet to report on, and for checks the token cannot read.
   */
  checkState: checkStateSchema,
  /** The review verdict: one change request outweighs approvals. */
  review: reviewStateSchema,
  /** Each reviewer's latest word, and those asked who have not answered. */
  reviews: z.array(prReviewSchema),
  mergeable: z.enum(["clean", "behind", "conflicting", "unknown"]),
  /** Commits its base has that it lacks. */
  behindBy: z.number().int(),
  unresolvedThreads: z.number().int(),
  /** The one state it shows as (`prDisplayState`, below). */
  display: z.lazy(() => z.enum(PR_DISPLAY_STATES)),
  createdAt: z.string(),
  updatedAt: z.string(),
});
export type PullRequest = z.infer<typeof pullRequestSchema>;

// ---------------------------------------------------------------------------
// What a pull request shows as: one state, by priority
// ---------------------------------------------------------------------------

/**
 * The one state a pull request is shown as, most pressing first: merged or
 * closed says it is over; then what stands between it and a merge, in the
 * order a person deals with it (red CI, CI pending, nobody has
 * looked, changes asked for, a conflict, open threads); then ready. The
 * rest of what is true goes in the chip's tooltip.
 */
export const PR_DISPLAY_STATES = [
  "merged",
  "closed",
  "ci_red",
  "ci_running",
  "awaiting",
  "changes",
  "conflict",
  "comments",
  "ready",
] as const;
export type PrDisplayState = (typeof PR_DISPLAY_STATES)[number];

/** A check run as GitHub reports it (`checks_json`). */
export interface PrCheck {
  name: string;
  /** `queued`, `in_progress`, `completed`… */
  status: string;
  /** Set once completed: `success`, `failure`, `neutral`, `skipped`, `cancelled`, `timed_out`… */
  conclusion: string | null;
  url?: string | null | undefined;
  durationMs?: number | null | undefined;
  /** Set on an entry that says why checks could not be read; it is not a check. */
  diagnostic?: string | null | undefined;
}

/** GitHub refused the token the check-runs listing: CI may exist that dude cannot see. */
export const CHECK_RUNS_FORBIDDEN = "check_runs_forbidden";

/** The first diagnostic in a check list, or null. */
export function prCheckDiagnostic(checks: PrDisplayInput["checks"]): string | null {
  if (typeof checks === "string") return null;
  return checks.find((c) => c.diagnostic)?.diagnostic ?? null;
}

/** The checks GitHub reported, without diagnostic entries: what rows and counts are made of. */
export function prActualChecks(checks: ReadonlyArray<PrCheck>): PrCheck[] {
  return checks.filter((c) => !c.diagnostic);
}

/** Why the checks could not all be read, in a person's words: one line. */
export function prCheckDiagnosticReason(code: string): string {
  return code === CHECK_RUNS_FORBIDDEN ? "GitHub won't show dude this repository's checks" : "Some GitHub checks cannot be read";
}

/**
 * What to do about it, under that line: GitHub gives fine-grained tokens no
 * way to read check runs, so the fix is another kind of token — and that CI
 * may well be running; dude cannot see it, so Merge waits.
 */
export function prCheckDiagnosticFix(code: string): string | null {
  return code === CHECK_RUNS_FORBIDDEN
    ? "A fine-grained token cannot read check runs: connect a classic token with the repo scope (or a GitHub App once dude supports one), authorized for the organization's SSO and with access to this repository. CI may be running; dude can't see it, so Merge waits."
    : null;
}

/** A reviewer's latest word, in a person's words: "approved", "requested changes". */
export function reviewWords(state: string): string {
  switch (state.toUpperCase()) {
    case "APPROVED":
      return "approved";
    case "COMMENTED":
      return "commented";
    case "CHANGES_REQUESTED":
      return "requested changes";
    case "REQUESTED":
      return "review requested";
    case "DISMISSED":
      return "review dismissed";
    default:
      return state.toLowerCase().replaceAll("_", " ");
  }
}

/** A review as GitHub reports it (`reviews_json`); each person's latest verdict counts. */
export interface PrReview {
  login: string;
  /** `APPROVED`, `CHANGES_REQUESTED`, `COMMENTED`, `DISMISSED`, or `REQUESTED` for one asked who has not answered. */
  state: string;
  submittedAt?: string | null | undefined;
  avatarUrl?: string | undefined;
  /** A team asked ("org/slug"), not a person. */
  team?: boolean | undefined;
  /** Asked again since this verdict: it stands on GitHub, but they owe another look. */
  rerequested?: boolean | undefined;
}

export type PrMergeable = "clean" | "behind" | "conflicting" | "unknown";

/**
 * What `prDisplayState` reads: the fields a PR has today (`state`, and
 * `checks` and `review` as one word each) and the richer ones the GitHub
 * work adds — `checks` as the list of runs, `reviews` by person,
 * `mergeable`, `unresolvedThreads`. A richer field wins when present;
 * absent, the summary decides, so the state is right before and after.
 */
export interface PrDisplayInput {
  state: PullRequest["state"];
  checks: PullRequest["checkState"] | ReadonlyArray<PrCheck>;
  review: PullRequest["review"];
  reviews?: ReadonlyArray<PrReview> | null | undefined;
  mergeable?: PrMergeable | null | undefined;
  unresolvedThreads?: number | null | undefined;
}

/**
 * One check's reading, as the orchestrator's checkRunState has it (forge,
 * github.go): running is pending; finished, its conclusion decides. Neutral
 * and skipped pass. Cancelled, waiting on a person, or superseded is not a
 * failure an agent could fix: pending, never failed, never re-run.
 */
function prCheckState(check: PrCheck): "passing" | "pending" | "failing" {
  if (check.status.toLowerCase() !== "completed") return "pending";
  switch ((check.conclusion ?? "").toLowerCase()) {
    case "success": case "neutral": case "skipped": return "passing";
    case "cancelled": case "action_required": case "stale": return "pending";
  }
  return "failing";
}

/** Whether a check run ended badly. */
export function prCheckFailed(check: PrCheck): boolean {
  return prCheckState(check) === "failing";
}

/** The checks' verdict: one failing run outweighs pending ones, which outweigh all green. */
export function prChecksSummary(checks: PrDisplayInput["checks"]): PullRequest["checkState"] {
  if (typeof checks === "string") return checks;
  if (checks.length === 0) return "unknown";
  const states = checks.map(prCheckState);
  if (states.includes("failing")) return "failing";
  if (states.includes("pending")) return "pending";
  return "passing";
}

/**
 * The review verdict: each person's latest, where a comment is not a
 * verdict and leaves theirs standing; one change request outweighs any
 * number of approvals, as GitHub has it.
 */
export function prReviewSummary(pr: Pick<PrDisplayInput, "review" | "reviews">): PullRequest["review"] {
  if (!pr.reviews || pr.reviews.length === 0) return pr.review;
  const latest = new Map<string, string>();
  const ordered = [...pr.reviews].sort((a, b) => (a.submittedAt ?? "").localeCompare(b.submittedAt ?? ""));
  for (const r of ordered) {
    const state = r.state.toUpperCase();
    if (state !== "COMMENTED") latest.set(r.login, state);
  }
  const verdicts = [...latest.values()];
  if (verdicts.includes("CHANGES_REQUESTED")) return "changes_requested";
  if (verdicts.includes("APPROVED")) return "approved";
  return "pending";
}

/** The one state to show a pull request as; `PR_DISPLAY_STATES` is the order. */
export function prDisplayState(pr: PrDisplayInput): PrDisplayState {
  if (pr.state === "merged") return "merged";
  if (pr.state === "closed") return "closed";
  const checks = prChecksSummary(pr.checks);
  if (checks === "failing") return "ci_red";
  if (checks === "pending") return "ci_running";
  const review = prReviewSummary(pr);
  if (review === "pending") return "awaiting";
  if (review === "changes_requested") return "changes";
  if (pr.mergeable === "conflicting") return "conflict";
  if ((pr.unresolvedThreads ?? 0) > 0) return "comments";
  return "ready";
}
