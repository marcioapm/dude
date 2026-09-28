import { z } from "zod";
import {
  deliveryPolicySchema,
  effortSchema,
  TERMINAL_TASK_STATUSES,
  timeLimitMinutesSchema,
  type Effort,
  type FullDeliveryPolicy,
} from "./hierarchy.ts";

/**
 * Settings in two layers: the organization's defaults, and each project's
 * overrides of them. A project stores only what it changes — a missing key
 * is "from the organization" — so a reset is a delete, and a project keeps
 * following its organization wherever it has not chosen otherwise. Under
 * both sit the factory's defaults (the orchestrator's DefaultPolicy, and
 * dude's built-in prompts).
 */

/**
 * The agents a person configures, in the order they are shown. The fixer
 * is the implementer's model told something else: its own prompt, and
 * settings of its own only where it is given them.
 */
export const SETTINGS_ROLES = ["implementer", "reviewer", "fixer", "simplifier", "qa_browser"] as const;
export type SettingsRole = (typeof SETTINGS_ROLES)[number];

/** Roles with a prompt: the configured ones, and the investigator. */
export const promptRoleSchema = z.enum([...SETTINGS_ROLES, "investigator"]);
export type PromptRole = z.infer<typeof promptRoleSchema>;

export const SETTINGS_ROLE_LABEL: Record<PromptRole, string> = {
  implementer: "Implementer",
  reviewer: "Reviewer",
  fixer: "Fixer",
  simplifier: "Simplifier",
  qa_browser: "Tester",
  investigator: "Investigator",
};

/** What each role is for, in a line. */
export const SETTINGS_ROLE_DESCRIPTION: Record<PromptRole, string> = {
  implementer: "Writes the change and its tests.",
  reviewer: "Reviews the change by category; reports findings, never pushes.",
  fixer: "Fixes exactly the findings or comments it is given.",
  simplifier: "Tidies the branch without changing what it does.",
  qa_browser: "Drives the app in a browser and records a video.",
  investigator: "Reads the code before any is written, and reports.",
};

/**
 * Roles that can be turned off, and the delivery setting that does it:
 * the simplifier's pass and the tester's are steps a delivery may skip;
 * the others are the delivery.
 */
export const ROLE_ENABLED_BY = { simplifier: "simplify", qa_browser: "test" } as const satisfies Partial<
  Record<SettingsRole, keyof FullDeliveryPolicy>
>;


/** Where a value comes from, as a project's settings show it. */
export type SettingSource = "organization" | "project";

export interface Setting<T> {
  value: T;
  source: SettingSource;
}

export interface PromptAuthor {
  id: string;
  name: string;
}

/** The current prompt at one layer. */
export interface PromptState {
  /** The version's id, or null for dude's built-in prompt. */
  versionId: string | null;
  body: string;
  updatedAt: string | null;
  updatedBy: PromptAuthor | null;
  /** How many versions there are (0 for the built-in prompt). */
  versions: number;
}

/**
 * A project's prompt for a role: added after the organization's, in its
 * place, or none (the organization's, as it is).
 */
export type ProjectPromptMode = "add" | "replace" | "inherit";

export interface RoleSettings {
  model: Setting<string | null>;
  effort: Setting<Effort | null>;
  timeLimitMinutes: Setting<number | null>;
  /** Null for a role that is always on. */
  enabled: Setting<boolean> | null;
  prompt: {
    organization: PromptState;
    /** Project settings only. */
    project?: PromptState & { mode: ProjectPromptMode };
  };
}

export type DeliverySettings = { [K in keyof FullDeliveryPolicy]: Setting<FullDeliveryPolicy[K]> };

export interface SettingsResponse {
  organization: { id: string; name: string };
  project?: { id: string; name: string };
  roles: Record<SettingsRole, RoleSettings>;
  delivery: DeliverySettings;
  /** Whether the caller may change these. */
  canEdit: boolean;
}

/**
 * A change to settings. `null` clears a value: an organization's goes back
 * to the factory's default, a project's back to its organization's.
 */
const nullable = <T extends z.ZodTypeAny>(t: T) => t.nullable().optional();
const policyShape = deliveryPolicySchema.shape;
const field = <K extends keyof typeof policyShape>(k: K) => policyShape[k].unwrap() as z.ZodType<NonNullable<z.infer<(typeof policyShape)[K]>>>;
export const settingsPatchSchema = z
  .object({
    roles: z
      .record(
        z.enum(SETTINGS_ROLES),
        z
          .object({
            model: nullable(z.string().trim().min(1).max(200)),
            effort: nullable(effortSchema),
            timeLimitMinutes: nullable(timeLimitMinutesSchema),
            enabled: nullable(z.boolean()),
          })
          .strict(),
      )
      .optional(),
    delivery: z
      .object({
        requiredReviewers: nullable(field("requiredReviewers")),
        blockingSeverities: nullable(field("blockingSeverities")),
        maxReviewIterations: nullable(field("maxReviewIterations")),
        maxAttemptsPerFinding: nullable(field("maxAttemptsPerFinding")),
        maxPrFixIterations: nullable(field("maxPrFixIterations")),
        simplify: nullable(field("simplify")),
        test: nullable(field("test")),
        parkAfterMinutes: nullable(field("parkAfterMinutes")),
        idleNudgeMinutes: nullable(field("idleNudgeMinutes")),
      })
      .strict()
      .optional(),
  })
  .strict();
export type SettingsPatch = z.infer<typeof settingsPatchSchema>;

export const savePromptSchema = z
  .object({
    projectId: z.string().min(1).optional(),
    mode: z.enum(["add", "replace", "inherit"]).optional(),
    body: z.string().max(100_000).default(""),
    note: z.string().max(500).default(""),
  })
  .strict();

/** One saved version of a prompt, as its history lists it. */
export interface PromptVersion {
  id: string;
  /** 1 for the first. */
  number: number;
  body: string;
  note: string;
  mode: "add" | "replace" | null;
  createdAt: string;
  /** Null for dude's built-in prompt, where an organization's history starts. */
  createdBy: PromptAuthor | null;
  restoredFrom: string | null;
  current: boolean;
  /** Sessions (phase Runs) told with it, and the latest few. */
  sessions: { count: number; recent: Array<{ runId: string; taskId: string; taskKey: string; taskTitle: string; phase: string; createdAt: string }> };
}

export interface PromptHistory {
  role: PromptRole;
  projectId: string | null;
  /** Newest first. */
  versions: PromptVersion[];
  /** dude's built-in prompt: what a history with no versions runs. */
  builtin: string;
}

/**
 * An epic's state: planned (not started), active, or done. Stored only
 * when a person sets it; otherwise it is what its tasks say — done once it
 * has tasks and every one has finished, active until then.
 */
export const EPIC_STATES = ["planned", "active", "done"] as const;
export const epicStateSchema = z.enum(EPIC_STATES);
export type EpicState = z.infer<typeof epicStateSchema>;

export function epicState(stored: EpicState | null, taskStatuses: readonly string[]): EpicState {
  if (stored) return stored;
  const finished = (s: string) => (TERMINAL_TASK_STATUSES as readonly string[]).includes(s);
  return taskStatuses.length > 0 && taskStatuses.every(finished) ? "done" : "active";
}

/**
 * What a saved prompt may name as `{{name}}`, filled in for each run by
 * the orchestrator (delivery/prompts.go, promptVariables). A section a
 * prompt places this way is not appended again after it. Anything else in
 * braces is left as written.
 */
export const PROMPT_VARIABLES = [
  { name: "task.title", description: "The task's title." },
  { name: "task.goal", description: "What the task should achieve." },
  { name: "task.criteria", description: "Its acceptance criteria, as a list." },
  { name: "run.branch", description: "The branch this run works on." },
  { name: "run.base_ref", description: "What the branch started from." },
] as const;
export type PromptVariable = (typeof PROMPT_VARIABLES)[number]["name"];
