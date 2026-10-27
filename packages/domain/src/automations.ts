/**
 * Automations: work nobody started by hand. Each runs on a schedule, when
 * GitHub tells dude something happened, or when a request reaches its own
 * URL (an incoming webhook, incoming.ts), and is one of four kinds: an agent
 * (its own, or one of dude's roles given a brief), a command that runs and
 * exits, a server that answers events, or a signed POST to a URL. An
 * automation belongs to the organisation or to one of its projects; an
 * organisation's filters which repositories it covers (projects cannot opt
 * out). docs/CONTRIBUTING.md, "Automations"; migration 183.
 */

import { z } from "zod";
import { harnessSchema } from "./harnesses.ts";
import { timeLimitMinutesSchema } from "./hierarchy.ts";
import { secretRefSchema, type SecretRecipient } from "./secrets.ts";
import { SETTINGS_ROLE_LABEL } from "./settings.ts";
import { incomingTriggerSchema, newHookId, type AutomationDelivery, type ConditionResult } from "./incoming.ts";

export const AUTOMATION_KINDS = ["agent", "command", "server", "webhook"] as const;
export const automationKindSchema = z.enum(AUTOMATION_KINDS);
export type AutomationKind = z.infer<typeof automationKindSchema>;

export const AUTOMATION_KIND_LABEL: Record<AutomationKind, string> = {
  agent: "Agent",
  command: "Command",
  server: "Server",
  webhook: "Send a webhook",
};

export type AutomationScope = "organization" | "project";

// ---------------------------------------------------------------------------
// Schedules
// ---------------------------------------------------------------------------

/** A cron field's bounds: minute, hour, day of month, month, day of week (0 and 7 are Sunday). */
const CRON_FIELDS = [
  { name: "minute", min: 0, max: 59 },
  { name: "hour", min: 0, max: 23 },
  { name: "day of month", min: 1, max: 31 },
  { name: "month", min: 1, max: 12 },
  { name: "day of week", min: 0, max: 7 },
] as const;

/** One field's values, or why it is not a field: `*`, `n`, `a-b`, `*\/s`, `a-b/s`, comma lists. */
function cronField(text: string, min: number, max: number): number[] | string {
  const values = new Set<number>();
  for (const part of text.split(",")) {
    const m = /^(\*|(\d+)(?:-(\d+))?)(?:\/(\d+))?$/.exec(part);
    if (!m) return `"${part}" is not a value, a range or a step`;
    const lo = m[1] === "*" ? min : Number(m[2]);
    const hi = m[1] === "*" ? max : m[3] !== undefined ? Number(m[3]) : m[4] !== undefined ? max : lo;
    const step = m[4] !== undefined ? Number(m[4]) : 1;
    if (lo < min || hi > max || lo > hi) return `"${part}" is outside ${min}–${max}`;
    if (step < 1) return `"${part}" has a step of 0`;
    for (let v = lo; v <= hi; v += step) values.add(v);
  }
  return [...values].sort((a, b) => a - b);
}

export interface CronSpec {
  minutes: number[];
  hours: number[];
  days: number[];
  months: number[];
  /** 0–6, Sunday first. */
  weekdays: number[];
  /** Whether day of month and day of week were both restricted: cron then fires on either. */
  dayOr: boolean;
}

/** A five-field cron expression, parsed; or the reason it is not one. */
export function parseCron(expr: string): { ok: true; spec: CronSpec } | { ok: false; message: string } {
  const fields = expr.trim().split(/\s+/);
  if (fields.length !== 5) return { ok: false, message: "a schedule is five fields: minute hour day-of-month month day-of-week" };
  const parsed: number[][] = [];
  for (const [i, f] of CRON_FIELDS.entries()) {
    const v = cronField(fields[i]!, f.min, f.max);
    if (typeof v === "string") return { ok: false, message: `${f.name}: ${v}` };
    parsed.push(v);
  }
  const weekdays = [...new Set(parsed[4]!.map((d) => d % 7))].sort((a, b) => a - b);
  return {
    ok: true,
    spec: {
      minutes: parsed[0]!, hours: parsed[1]!, days: parsed[2]!, months: parsed[3]!, weekdays,
      dayOr: fields[2] !== "*" && fields[4] !== "*",
    },
  };
}

/** Whether tz is a time zone this runtime knows (IANA names, as Go's time.LoadLocation reads them). */
export function isTimeZone(tz: string): boolean {
  try {
    new Intl.DateTimeFormat("en-US", { timeZone: tz });
    return tz.length > 0;
  } catch {
    return false;
  }
}

export const scheduleTriggerSchema = z.object({
  kind: z.literal("schedule"),
  cron: z.string().superRefine((c, ctx) => {
    const p = parseCron(c);
    if (!p.ok) ctx.addIssue({ code: z.ZodIssueCode.custom, message: p.message });
  }),
  tz: z.string().refine(isTimeZone, "not a time zone"),
}).strict();

// ---------------------------------------------------------------------------
// GitHub events
// ---------------------------------------------------------------------------

/**
 * Any GitHub event name: the model takes events GitHub adds later, so it is
 * an open string checked only for shape. The UI offers the ones the
 * organisation is subscribed to.
 */
export const githubEventSchema = z.string().regex(/^[a-z][a-z0-9_]{0,63}$/, "not a GitHub event name");

const filterValues = z.array(z.string().trim().min(1).max(200)).max(50).default([]);

/** Values within one filter are OR'd; filters are AND'd. author takes `!login` to exclude. */
export const eventFiltersSchema = z.object({
  branch: filterValues,
  label: filterValues,
  paths: filterValues,
  author: filterValues,
}).strict();
export type EventFilters = z.infer<typeof eventFiltersSchema>;

export const githubTriggerSchema = z.object({
  kind: z.literal("github"),
  event: githubEventSchema,
  /** None means every action. */
  actions: z.array(z.string().regex(/^[a-z][a-z0-9_]{0,63}$/)).max(50).default([]),
  filters: eventFiltersSchema.default({}),
}).strict();

export const triggerSchema = z.discriminatedUnion("kind", [scheduleTriggerSchema, githubTriggerSchema, incomingTriggerSchema]);
export type Trigger = z.infer<typeof triggerSchema>;
export type ScheduleTrigger = z.infer<typeof scheduleTriggerSchema>;
export type GithubTrigger = z.infer<typeof githubTriggerSchema>;

/** The order the test-event dialog names a filter that would skip an event. */
export const FILTER_ORDER = ["actions", "repositories", "branch", "label", "author", "paths"] as const;
export type FilterName = (typeof FILTER_ORDER)[number];
export const FILTER_LABEL: Record<FilterName, string> = {
  actions: "Actions",
  repositories: "Repositories",
  branch: "Branch",
  label: "Label",
  author: "Author",
  paths: "Paths",
};

// ---------------------------------------------------------------------------
// Repositories
// ---------------------------------------------------------------------------

/** `owner/name`, where `*` in either segment matches within it. */
export const repoPatternSchema = z.string().trim().regex(/^[A-Za-z0-9_.*-]+\/[A-Za-z0-9_.*-]+$/, "owner/name, or a glob like acme/svc-*");

export const repoFilterSchema = z.object({
  mode: z.enum(["all", "only", "except"]),
  repos: z.array(repoPatternSchema).max(200).default([]),
}).strict().superRefine((f, ctx) => {
  if (f.mode === "all" && f.repos.length > 0) ctx.addIssue({ code: z.ZodIssueCode.custom, message: "all repositories names none", path: ["repos"] });
  if (f.mode !== "all" && f.repos.length === 0) ctx.addIssue({ code: z.ZodIssueCode.custom, message: "name at least one repository", path: ["repos"] });
});
export type RepoFilter = z.infer<typeof repoFilterSchema>;

/** Whether a glob (`*` within a segment, case-insensitive as GitHub's names are) matches owner/name. */
export function repoGlobMatches(pattern: string, fullName: string): boolean {
  const [po, pn] = pattern.toLowerCase().split("/");
  const [fo, fn] = fullName.toLowerCase().split("/");
  if (po === undefined || pn === undefined || fo === undefined || fn === undefined) return false;
  const seg = (p: string, s: string) =>
    new RegExp(`^${p.split("*").map((x) => x.replace(/[.+?^${}()|[\]\\-]/g, "\\$&")).join("[^/]*")}$`).test(s);
  return seg(po, fo) && seg(pn, fn);
}

/** Whether an organisation automation's filter covers a repository (null: every one). */
export function repoFilterAllows(filter: RepoFilter | null | undefined, fullName: string): boolean {
  if (!filter || filter.mode === "all") return true;
  const named = filter.repos.some((p) => repoGlobMatches(p, fullName));
  return filter.mode === "only" ? named : !named;
}

/** A repository's `owner/name` on its forge, from its clone URL (https, ssh or scp-like); null when it names none. */
export function repositoryFullName(url: string): string | null {
  const m = /^(?:[a-z+]+:\/\/(?:[^@/]+@)?[^/]+\/|[^@\s]+@[^:\s]+:)([^/\s]+)\/([^/\s]+?)(?:\.git)?\/?$/i.exec(url.trim());
  return m ? `${m[1]}/${m[2]}` : null;
}

/** A project automation's checkouts: all of the project's repositories, none, or these ids. */
export const automationReposSchema = z.union([z.literal("all"), z.literal("none"), z.array(z.string().min(1)).max(100)]);
export type AutomationRepos = z.infer<typeof automationReposSchema>;

// ---------------------------------------------------------------------------
// The agent
// ---------------------------------------------------------------------------

/**
 * dude's roles an automation may run without a task. Conductor, brainstorm,
 * implementer, simplifier and fixer each need a task, a session or a branch
 * an automation does not have.
 */
export const PRESET_ROLES = ["qa_browser", "reviewer", "investigator"] as const;
export const presetRoleSchema = z.enum(PRESET_ROLES);
export type PresetRole = z.infer<typeof presetRoleSchema>;
export const PRESET_ROLE_LABEL: Record<PresetRole, string> = {
  qa_browser: SETTINGS_ROLE_LABEL.qa_browser,
  reviewer: SETTINGS_ROLE_LABEL.reviewer,
  investigator: SETTINGS_ROLE_LABEL.investigator,
};
export const REVIEWER_NEEDS_PULL_REQUEST = "Reviews a pull request: pull_request events only.";

/**
 * The actions GitHub sends for its common events, for the trigger's
 * checkboxes. An event not listed here (or one GitHub adds actions to)
 * takes actions as free text: the trigger model accepts any (I1).
 */
export const EVENT_ACTIONS: Readonly<Record<string, readonly string[]>> = {
  pull_request: ["opened", "synchronize", "reopened", "closed", "edited", "ready_for_review", "converted_to_draft",
    "labeled", "unlabeled", "assigned", "unassigned", "review_requested", "review_request_removed"],
  pull_request_review: ["submitted", "edited", "dismissed"],
  pull_request_review_comment: ["created", "edited", "deleted"],
  issues: ["opened", "edited", "closed", "reopened", "labeled", "unlabeled", "assigned", "unassigned", "transferred", "deleted"],
  issue_comment: ["created", "edited", "deleted"],
  release: ["published", "created", "released", "prereleased", "edited", "unpublished", "deleted"],
  check_run: ["created", "completed", "rerequested", "requested_action"],
  check_suite: ["completed", "requested", "rerequested"],
  workflow_run: ["requested", "in_progress", "completed"],
  discussion: ["created", "edited", "answered", "closed", "reopened", "deleted"],
};

/** What an automation's agent runs on; a preset overrides only what it names. */
const agentRuntimeSchema = z.object({
  harness: harnessSchema.optional(),
  tierId: z.string().min(1).optional(),
  timeLimitMinutes: timeLimitMinutesSchema.optional(),
  machineSizeId: z.string().min(1).optional(),
  imageId: z.string().min(1).optional(),
}).strict();
export type AgentOverrides = z.infer<typeof agentRuntimeSchema>;

export const PROMPT_MAX_CHARS = 100_000;

export const ownAgentSchema = z.object({
  own: agentRuntimeSchema.extend({ prompt: z.string().max(PROMPT_MAX_CHARS).default("") }).strict(),
}).strict();
export const presetAgentSchema = z.object({
  preset: z.object({ role: presetRoleSchema, overrides: agentRuntimeSchema.default({}) }).strict(),
  /** Given after the role's own prompt, as the task brief. */
  brief: z.string().max(PROMPT_MAX_CHARS).default(""),
}).strict();
export const automationAgentSchema = z.union([ownAgentSchema, presetAgentSchema]);
export type AutomationAgent = z.infer<typeof automationAgentSchema>;

/** The variables an automation's prompt or brief may name; each is substituted when a run starts. */
export const AUTOMATION_PROMPT_VARIABLES = [
  { name: "event.name", description: "The GitHub event, as X-GitHub-Event names it." },
  { name: "event.action", description: "Its action (opened, completed…), empty when it has none." },
  { name: "event.delivery_id", description: "GitHub's delivery id." },
  { name: "event.payload", description: "The whole event body, as JSON." },
  { name: "repository", description: "owner/name of the event's repository." },
  { name: "branch", description: "The branch the event is about, when it names one." },
  { name: "sender", description: "The login of who caused the event." },
  { name: "pr.number", description: "The pull request's number, on pull request events." },
  { name: "pr.title", description: "The pull request's title." },
  { name: "pr.url", description: "The pull request's page." },
  { name: "schedule.time", description: "When a scheduled run was due, in the schedule's time zone." },
] as const;

/** An incoming webhook's variables: the GitHub ones name nothing in its body. */
export const INCOMING_PROMPT_VARIABLES = [
  { name: "payload.<path>", description: "A value from the delivery's JSON body: {{payload.commonLabels.alertname}}." },
  { name: "event.payload", description: "The whole body, as JSON." },
  { name: "event.delivery_id", description: "dude's delivery id." },
  { name: "event.group_key", description: "The group-by value." },
] as const;

// ---------------------------------------------------------------------------
// Command, server, webhook
// ---------------------------------------------------------------------------

export const COMMAND_MAX_BYTES = 65_536;
const commandText = z.string().min(1).refine((s) => new TextEncoder().encode(s).length <= COMMAND_MAX_BYTES && !s.includes("\0"),
  `at most ${COMMAND_MAX_BYTES} bytes, with no NUL characters`);

export const automationServerSchema = z.object({
  command: commandText,
  port: z.number().int().min(1).max(65_535),
  path: z.string().regex(/^\/[^\s]*$/, "a path starting with /").default("/events"),
  /** GET this path for health; null: the port accepting a connection is enough. */
  health: z.string().regex(/^\/[^\s]*$/, "a path starting with /").nullable().default(null),
}).strict();
export type AutomationServer = z.infer<typeof automationServerSchema>;

export const automationWebhookSchema = z.object({
  url: z.string().url().refine((u) => /^https?:\/\//.test(u), "an http(s) URL"),
}).strict();

/** How long an idle Server container is kept up after its last request is answered. */
export const SERVER_IDLE_SECONDS = 30;
/** How long a starting Server has to answer its health check. */
export const SERVER_HEALTH_SECONDS = 60;
export const PAYLOAD_RETENTION_DAYS = 30;
export const LOG_RETENTION_DAYS = 90;

// ---------------------------------------------------------------------------
// Session and concurrency
// ---------------------------------------------------------------------------

export const SESSION_MODES = ["ephemeral", "resumable"] as const;
export const EXPIRY_MAX_DAYS = 30;
export const expiryDaysSchema = z.number().int().min(1).max(EXPIRY_MAX_DAYS, `at most ${EXPIRY_MAX_DAYS} days`);

export const CONCURRENCY = ["queue", "merge", "skip", "cancel", "send", "parallel"] as const;
export type Concurrency = (typeof CONCURRENCY)[number];
export const CONCURRENCY_LABEL: Record<Concurrency, string> = {
  queue: "Queue",
  merge: "Merge waiting events",
  skip: "Skip",
  cancel: "Cancel and restart",
  send: "Send to the running agent",
  parallel: "Run in parallel",
};
export const PARALLEL_MAX = 10;

// ---------------------------------------------------------------------------
// An automation, as it is saved
// ---------------------------------------------------------------------------

const baseSchema = z.object({
  name: z.string().trim().min(1).max(120),
  description: z.string().max(2000).default(""),
  kind: automationKindSchema,
  trigger: triggerSchema,
  sessionMode: z.enum(SESSION_MODES).default("ephemeral"),
  expiryDays: expiryDaysSchema.nullable().default(null),
  concurrency: z.enum(CONCURRENCY).default("queue"),
  parallelMax: z.number().int().min(2).max(PARALLEL_MAX).default(2),
  repos: automationReposSchema.default("all"),
  repoFilter: repoFilterSchema.nullable().default(null),
  agent: automationAgentSchema.nullable().default(null),
  machineSizeId: z.string().min(1).nullable().default(null),
  imageId: z.string().min(1).nullable().default(null),
  setupScript: z.string().max(COMMAND_MAX_BYTES).nullable().default(null),
  command: commandText.nullable().default(null),
  server: automationServerSchema.nullable().default(null),
  webhook: automationWebhookSchema.nullable().default(null),
  secrets: z.array(secretRefSchema).max(100).default([]),
  notifyOnFailure: z.boolean().default(true),
  /** An organisation incoming automation's projects: create_task files there; their repositories are the ones it may check out. */
  projectIds: z.array(z.string().min(1)).max(50).default([]),
}).strict();

export type AutomationInput = z.infer<typeof baseSchema>;

/** The Secrets row's help on an automation that picks its own: an own agent, a command, a server. */
export const ORG_AUTOMATION_PICKS = (org: string) =>
  `Only those ticked are given to each run, as environment variables. An organisation automation picks ${org}'s secrets; a project's automation picks the project's too.`;

/** The Secrets row's help on a dude-role preset, which picks none. */
export const PRESET_SECRETS = (role: string) =>
  `It gets what ${role} is given: the organisation's and the project's secrets ticked for ${role}. Change who gets a secret in Secrets settings.`;

/** The secret recipient a preset role's runs are, as a task's Run of that role is; the Investigator is none. */
export const PRESET_SECRET_RECIPIENT: Record<PresetRole, SecretRecipient | null> = {
  qa_browser: "tester",
  reviewer: "reviewer",
  investigator: null,
};

export const ORG_AUTOMATION_PROJECT_SECRET = "an organisation automation picks the organisation's secrets; a project's belong to its own automations";
export const PRESET_PICKS_NO_SECRETS = "one of dude's agents gets what its role is given; it picks no secrets";

/** What a Webhook or Server automation does when a POST gets a 5xx or no answer. */
export const AUTOMATION_POST_RETRY = "A 5xx or no answer within 30 s is retried once; then the run fails and its owner is told.";

/** An organisation incoming automation saved with no project. */
export const INCOMING_NEEDS_PROJECT = "Pick at least one project: tasks are filed there and its repositories are the ones it can check out.";
/** Send to the running agent on an incoming webhook with no group-by path. */
export const INCOMING_SEND_NEEDS_GROUP =
  "Name a group-by path: with Send to the running agent, a later delivery of the same group goes to its run.";

/** The incoming trigger's own rules; null when it keeps them. */
function incomingProblem(a: AutomationInput, scope: AutomationScope): { path: string; message: string } | null {
  if (a.trigger.kind !== "incoming") {
    return a.projectIds.length > 0 ? { path: "projectIds", message: "only an organisation automation on an incoming webhook names projects" } : null;
  }
  if (scope === "project" && a.projectIds.length > 0) return { path: "projectIds", message: "a project automation works in its own project" };
  if (scope === "organization" && a.projectIds.length === 0) return { path: "projectIds", message: INCOMING_NEEDS_PROJECT };
  if (new Set(a.projectIds).size !== a.projectIds.length) return { path: "projectIds", message: "each project is named once" };
  if (a.repoFilter) return { path: "repoFilter", message: "an incoming webhook names no repository: its projects' repositories are the ones it checks out" };
  if (a.concurrency === "send" && !a.trigger.groupBy) return { path: "trigger.groupBy", message: INCOMING_SEND_NEEDS_GROUP };
  return null;
}

/**
 * Why an automation cannot be saved as it is in this scope, as the field it
 * is about and a sentence; null when it can. The database checks the same
 * rules (migration 183), this says them in words.
 */
export function automationProblem(a: AutomationInput, scope: AutomationScope): { path: string; message: string } | null {
  if (scope === "project" && a.repoFilter) return { path: "repoFilter", message: "a project automation covers the project's repositories; only an organisation automation filters them" };
  if (scope === "organization" && a.secrets.some((s) => s.source === "project")) return { path: "secrets", message: ORG_AUTOMATION_PROJECT_SECRET };
  if (a.agent && "preset" in a.agent && a.secrets.length > 0) return { path: "secrets", message: PRESET_PICKS_NO_SECRETS };
  if (new Set(a.secrets.map((s) => `${s.source}:${s.name}`)).size !== a.secrets.length) return { path: "secrets", message: "each secret is picked once" };
  if (a.sessionMode === "resumable" && a.expiryDays === null) return { path: "expiryDays", message: "a resumable session expires after 1 to 30 days without a run" };
  if (a.sessionMode === "ephemeral" && a.expiryDays !== null) return { path: "expiryDays", message: "only a resumable session expires" };
  if (a.kind === "webhook" && a.sessionMode !== "ephemeral") return { path: "sessionMode", message: "a webhook keeps no session" };
  if (a.kind === "command" && a.sessionMode !== "ephemeral") return { path: "sessionMode", message: "a command runs to its end; it keeps no session to resume" };
  if (a.concurrency === "send" && !(a.kind === "agent" && a.sessionMode === "resumable")) return { path: "concurrency", message: "only a resumable agent is sent events while it runs" };
  if (a.concurrency === "parallel" && a.sessionMode !== "ephemeral") return { path: "concurrency", message: "a resumable automation has one session; it cannot run in parallel" };
  if (a.kind === "server" && a.trigger.kind !== "github") {
    return { path: "trigger", message: a.trigger.kind === "incoming" ? "a server answers GitHub events; it has no incoming webhook" : "a server answers GitHub events; it has no schedule" };
  }
  const incoming = incomingProblem(a, scope);
  if (incoming) return incoming;
  if (a.kind === "agent" && !a.agent) return { path: "agent", message: "an agent automation needs its agent" };
  if (a.kind === "command" && !a.command) return { path: "command", message: "a command automation needs its command" };
  if (a.kind === "server" && !a.server) return { path: "server", message: "a server automation needs its command and port" };
  if (a.kind === "webhook" && !a.webhook) return { path: "webhook", message: "a webhook automation needs its URL" };
  if (a.agent && "preset" in a.agent && a.agent.preset.role === "reviewer"
    && !(a.trigger.kind === "github" && a.trigger.event.startsWith("pull_request"))) {
    return { path: "agent", message: REVIEWER_NEEDS_PULL_REQUEST };
  }
  return null;
}

/** An automation's settings as the API takes them, in this scope. */
export function automationInputSchema(scope: AutomationScope) {
  return baseSchema.superRefine((a, ctx) => {
    const p = automationProblem(a, scope);
    if (p) ctx.addIssue({ code: z.ZodIssueCode.custom, path: p.path.split("."), message: p.message });
  });
}

/** The New automation dialog: a name, what runs, when. Create fills the rest with defaults, off. */
export const newAutomationSchema = z.object({
  name: z.string().trim().min(1).max(120),
  kind: automationKindSchema,
  /** For an agent: its own, or one of dude's roles. */
  preset: presetRoleSchema.nullable().default(null),
  trigger: z.enum(["schedule", "github", "incoming"]),
}).strict();
export type NewAutomation = z.infer<typeof newAutomationSchema>;

/** A new automation's first settings, which the dialog's three answers decide; an incoming one gets hookId. */
export function automationDefaults(n: NewAutomation, tz = "UTC", hookId: string = newHookId()): AutomationInput {
  // A server answers events only; a reviewer needs a pull request.
  const onEvents = n.trigger === "github" || n.kind === "server" || n.preset === "reviewer";
  return baseSchema.parse({
    name: n.name,
    kind: n.kind,
    trigger: onEvents
      ? { kind: "github", event: "pull_request", actions: ["opened", "synchronize"], filters: {} }
      : n.trigger === "incoming"
        ? { kind: "incoming", hookId }
        : { kind: "schedule", cron: "0 9 * * 1-5", tz },
    agent: n.kind !== "agent" ? null : n.preset ? { preset: { role: n.preset, overrides: {} }, brief: "" } : { own: { prompt: "" } },
    command: n.kind === "command" ? "echo hello" : null,
    server: n.kind === "server" ? { command: "npm start", port: 3000, path: "/events", health: null } : null,
    webhook: n.kind === "webhook" ? { url: "https://example.com/hooks/dude" } : null,
  });
}

export const transferOwnerSchema = z.object({ personId: z.string().min(1) }).strict();
export const enabledSchema = z.object({ enabled: z.boolean() }).strict();
export const webhookSecretSchema = z.object({ secret: z.string().min(16).max(256) }).strict();

// ---------------------------------------------------------------------------
// As the API answers
// ---------------------------------------------------------------------------

export interface PersonBrief {
  id: string;
  name: string;
  photoUrl: string | null;
}

export interface Automation extends AutomationInput {
  id: string;
  projectId: string | null;
  /** The project's name, for an automation that belongs to one. */
  projectName: string | null;
  enabled: boolean;
  owner: PersonBrief | null;
  /** Set when the owner left: the organisation's admins hold it together. */
  ownerLeftAt: string | null;
  /** Whether the signing secret is set (its hint, never the value). */
  webhookSecretHint: string | null;
  /** An incoming webhook's verification secret: its last four characters, never the value. */
  incomingSecretHint: string | null;
  /** Where senders reach dude (the origin GitHub's webhooks are registered at), for an incoming webhook's URL; null when unknown. */
  publicOrigin: string | null;
  nextRunAt: string | null;
  lastRun: { id: string; status: AutomationRunStatus; at: string } | null;
  canEdit: boolean;
  /** Whether the caller may transfer it: an admin, or its owner. */
  canTransfer: boolean;
  createdAt: string;
  updatedAt: string;
}

export const AUTOMATION_RUN_STATUSES = ["queued", "running", "succeeded", "failed", "skipped", "cancelled"] as const;
export type AutomationRunStatus = (typeof AUTOMATION_RUN_STATUSES)[number];
export type AutomationRunSource = "schedule" | "event" | "run_now" | "test";

export interface AutomationPayload {
  deliveryId: string;
  event: string;
  action: string | null;
  body: unknown;
}

export interface AutomationRun {
  id: string;
  automationId: string;
  status: AutomationRunStatus;
  reason: string | null;
  source: AutomationRunSource;
  session: "new" | "resumed" | "ephemeral" | null;
  runId: string | null;
  startedBy: PersonBrief | null;
  costUsd: number | null;
  createdAt: string;
  startedAt: string | null;
  endedAt: string | null;
  /** The first payload's event and action, for the list. */
  event: string | null;
  action: string | null;
  payloadCount: number;
  /** A running run's newest tool call not finished yet (the list's "now:" line); null otherwise. */
  now?: { tool: string; args: unknown } | null;
}

export interface AutomationRunRequest {
  id: string;
  deliveryId: string | null;
  try: number;
  httpStatus: number | null;
  ms: number | null;
  error: string | null;
  responseExcerpt: string | null;
  at: string;
}

export interface AutomationRunDetail extends AutomationRun {
  payloads: AutomationPayload[];
  payloadsPurged: boolean;
  logTail: string | null;
  exitCode: number | null;
  containerStartedAt: string | null;
  containerStoppedAt: string | null;
  stopReason: "idle" | "error" | "aborted" | null;
  requests: AutomationRunRequest[];
  /** An incoming run's first delivery and the later ones of its group sent to it, oldest first. */
  deliveries?: AutomationDelivery[];
  /** The calls its agent made to dude's tools, oldest first. */
  toolCalls: AutomationToolCall[];
  /** The post_to_chat refusal the orchestrator failed the run on (its event id); null for a run that did not fail on one. */
  failedCallId: string | null;
  /** When a scheduled run was due. */
  scheduledFor?: string | null;
  /** What each delivery sent to the run while it ran gave its agent, by delivery id; null once its body is gone. */
  sentBodies?: Record<string, unknown>;
  /** A running agent's reply not yet ended and its calls' output so far, by call id, as the orchestrator holds them; null once it ends. */
  live?: { message: string; output: Record<string, Record<string, unknown>> } | null;
}

/** One call an automation run's agent made to a dude tool, as the run's ledger has it. */
export interface AutomationToolCall {
  id: string;
  tool: string;
  at: string;
  status: "completed" | "failed";
  /** Its arguments, redacted; a post's text is not repeated here. */
  args: Record<string, unknown>;
  durationMs: number | null;
  error: string | null;
  /** A post_to_chat that went: the channel as people read it ("#engineering"), its id, the message's ts and link. */
  posted: { channel: string; channelId: string; ts: string; permalink: string } | null;
}

/** Run now: nothing on a schedule; a past delivery or a written test event on GitHub events; a past delivery or a written body on an incoming webhook. */
export const runNowSchema = z.union([
  z.object({}).strict(),
  z.object({ deliveryId: z.string().min(1).max(200) }).strict(),
  z.object({
    testEvent: z.object({
      event: githubEventSchema,
      action: z.string().max(64).nullable().default(null),
      deliveryId: z.string().min(1).max(200),
      body: z.record(z.unknown()),
    }).strict(),
  }).strict(),
  z.object({ payload: z.record(z.unknown()) }).strict(),
]);
export type RunNow = z.infer<typeof runNowSchema>;

export const evaluateEventSchema = z.object({ body: z.record(z.unknown()) }).strict();
export type EventVerdict = { matches: true } | { matches: false; filter: FilterName; because: string };
/** A written body against an incoming trigger: each condition's result, its group key, and whether a run of that group is going. */
export interface IncomingVerdict {
  matches: boolean;
  results: ConditionResult[];
  groupKey: string;
  /** A live run of its group: a real delivery would be sent there, its conditions unchecked. */
  liveRunId: string | null;
}

/** An automation's runs, and its kept session when it has one. */
export interface AutomationRunsResponse {
  runs: AutomationRun[];
  session: { since: string; lastRunAt: string; resumes: number; expiresAt: string | null } | null;
  /** Every run it has, whatever the list's filter and limit. */
  total: number;
}

/** One saved version of an agent automation's prompt or brief; newest first in a list. */
export interface AutomationPromptVersion {
  id: string;
  number: number;
  body: string;
  createdBy: { id: string; name: string } | null;
  createdAt: string;
  /** The version a restore brought back. */
  restoredFrom: number | null;
  current: boolean;
}

export const restorePromptSchema = z.object({ versionId: z.string().min(1).max(100) }).strict();

/** A stored delivery Run now offers: the event's last 30 days. Listed without its body (the delivery's detail has it). */
export interface PastDelivery {
  id: string;
  event: string;
  action: string | null;
  repository: string | null;
  sender: string | null;
  receivedAt: string;
}

/** The events an organisation's GitHub sends dude, and where dude read that. */
export interface EventSubscriptions {
  events: string[];
  /** app: the GitHub App's settings; webhook: the repository webhooks dude made; seen: only what has arrived. */
  source: "app" | "webhook" | "seen";
  /** Where a person adds events (the App's permissions page), when dude knows it. */
  settingsUrl: string | null;
}
