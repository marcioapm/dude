/*
 * The mockup's world: Acme, its control-plane project (CP) with the epics
 * Billing v2 and Webhook reliability, a web project (WEB) that one relation
 * crosses into, and the relations between their tasks. Deterministic, so
 * screenshots compare.
 */

import type { AgentRole, TaskStatus } from "@dude/domain";
import type { Person } from "../../../../packages/design-system/src/components/PersonAvatar.tsx";
import type { NavEpic, NavProject, NavTask } from "../../../../packages/design-system/src/util/navModel.ts";

export const ORG = "Acme";

export const P = {
  marcio: { id: "u_marcio", name: "Márcio Martins", online: true },
  ana: { id: "u_ana", name: "Ana Ribeiro", online: true },
  tom: { id: "u_tom", name: "Tom Okafor" },
  kai: { id: "u_kai", name: "Kai Nakamura" },
  jonas: { id: "u_jonas", name: "Jonas Weber" },
} satisfies Record<string, Person>;

export const YOU = P.marcio;

export interface Task {
  readonly key: string;
  readonly title: string;
  readonly status: TaskStatus;
  readonly project: "control-plane" | "web";
  readonly epic?: "billing" | "webhooks" | undefined;
  readonly owner: Person;
  /** The agent at work for the owner, when one is. */
  readonly agent?: { readonly role: AgentRole; readonly activity: string } | undefined;
  readonly hoursInStatus: number;
  readonly costUsd?: number | undefined;
  readonly pr?: { readonly number: number; readonly state: "open" | "merged" | "closed" | "draft" } | undefined;
  readonly ask?: string | undefined;
}

export const TASKS: readonly Task[] = [
  // Billing v2: the chain.
  { key: "CP-40", title: "Usage events table and ingestion endpoint", status: "done", project: "control-plane", epic: "billing", owner: P.ana, hoursInStatus: 70, costUsd: 6.4, pr: { number: 412, state: "merged" } },
  { key: "CP-41", title: "Meter usage per organisation, hourly", status: "review", project: "control-plane", epic: "billing", owner: P.ana, hoursInStatus: 5, costUsd: 9.1, pr: { number: 431, state: "open" } },
  { key: "CP-43", title: "Sync Stripe customers to organisations", status: "running", project: "control-plane", epic: "billing", owner: P.tom, agent: { role: "implementer", activity: "Running bun test" }, hoursInStatus: 2, costUsd: 3.2 },
  { key: "CP-42", title: "Invoice line items from metered usage", status: "received", project: "control-plane", epic: "billing", owner: P.ana, hoursInStatus: 20 },
  { key: "CP-44", title: "Billing page: usage this month and projected invoice", status: "received", project: "control-plane", epic: "billing", owner: P.marcio, hoursInStatus: 30 },
  { key: "CP-48", title: "Plan picker on the billing page", status: "received", project: "control-plane", epic: "billing", owner: P.kai, hoursInStatus: 8 },
  { key: "CP-47", title: "Export usage as CSV", status: "received", project: "control-plane", epic: "billing", owner: P.jonas, hoursInStatus: 50 },
  { key: "CP-38", title: "Spike: Stripe Tax for EU VAT", status: "failed", project: "control-plane", epic: "billing", owner: P.tom, hoursInStatus: 40, costUsd: 4.7 },
  { key: "CP-49", title: "Tax rates per country on invoices", status: "received", project: "control-plane", epic: "billing", owner: P.tom, hoursInStatus: 26 },
  // Webhook reliability.
  { key: "CP-30", title: "Dedupe deliveries by X-GitHub-Delivery", status: "done", project: "control-plane", epic: "webhooks", owner: P.ana, hoursInStatus: 160, costUsd: 5.2, pr: { number: 398, state: "merged" } },
  { key: "CP-35", title: "Retry the GitHub webhook handler with backoff", status: "awaiting_input", project: "control-plane", epic: "webhooks", owner: P.marcio, agent: { role: "implementer", activity: "Cap retries at 5 or 8 attempts?" }, hoursInStatus: 1, costUsd: 2.1, ask: "Cap retries at 5 or 8 attempts?" },
  { key: "CP-36", title: "Replay endpoint for missed deliveries", status: "ready_to_merge", project: "control-plane", epic: "webhooks", owner: P.kai, hoursInStatus: 9, costUsd: 7.3, pr: { number: 427, state: "open" } },
  { key: "CP-45", title: "Webhook when usage crosses a plan threshold", status: "received", project: "control-plane", epic: "webhooks", owner: P.jonas, hoursInStatus: 12 },
  // No epic.
  { key: "CP-50", title: "Rate-limit the public API per key", status: "queued", project: "control-plane", owner: P.jonas, hoursInStatus: 18 },
  { key: "CP-46", title: "Show the organisation's usage in the sidebar footer", status: "received", project: "control-plane", owner: P.kai, hoursInStatus: 4 },
  // Another project, for the relation that crosses into it.
  { key: "WEB-12", title: "Footer slot in the app shell", status: "running", project: "web", owner: P.kai, agent: { role: "implementer", activity: "Editing Shell.tsx" }, hoursInStatus: 3, costUsd: 1.9 },
  { key: "WEB-9", title: "Dark-mode charts", status: "done", project: "web", owner: P.ana, hoursInStatus: 200, costUsd: 3.3, pr: { number: 88, state: "merged" } },
];

/**
 * The moment a screen shows. "now": CP-41 in review. "cp41-merged": the
 * morning it merged (screen 7). Set by main.tsx before each render; every
 * read below goes through `task`, so the whole world follows.
 */
export type Scenario = "now" | "cp41-merged" | "cp49-overridden";
let scenario: Scenario = "now";
export const setScenario = (s: Scenario) => { scenario = s; };

const OVERRIDES: Record<Scenario, Readonly<Record<string, Partial<Task>>>> = {
  now: {},
  "cp41-merged": { "CP-41": { status: "done", hoursInStatus: 0, pr: { number: 431, state: "merged" } } },
  "cp49-overridden": { "CP-49": { status: "running", agent: { role: "conductor", activity: "Planning with Tom" }, hoursInStatus: 1 } },
};

export const task = (key: string): Task => {
  const t = TASKS.find((x) => x.key === key);
  if (!t) throw new Error(`no task ${key}`);
  const o = OVERRIDES[scenario][key];
  return o ? { ...t, ...o } : t;
};

/** Who wrote an edge: a person, or an agent working for one. */
export type By =
  | { readonly kind: "person"; readonly person: Person }
  | { readonly kind: "agent"; readonly role: AgentRole; readonly for: Person; readonly on: string };

/** One stored edge. `blocks`: from blocks to. `related`: symmetric, stored once. */
export interface Edge {
  readonly from: string;
  readonly to: string;
  readonly kind: "blocks" | "related";
  readonly by: By;
  readonly ago: string;
}

export const EDGES: readonly Edge[] = [
  { from: "CP-40", to: "CP-41", kind: "blocks", by: { kind: "person", person: P.ana }, ago: "4d" },
  { from: "CP-40", to: "CP-42", kind: "blocks", by: { kind: "agent", role: "conductor", for: P.ana, on: "CP-42" }, ago: "2d" },
  { from: "CP-41", to: "CP-42", kind: "blocks", by: { kind: "agent", role: "brainstorm", for: P.ana, on: "a session" }, ago: "3d" },
  { from: "CP-41", to: "CP-44", kind: "blocks", by: { kind: "agent", role: "brainstorm", for: P.ana, on: "a session" }, ago: "3d" },
  { from: "CP-41", to: "CP-45", kind: "blocks", by: { kind: "person", person: P.jonas }, ago: "1d" },
  { from: "CP-43", to: "CP-44", kind: "blocks", by: { kind: "agent", role: "conductor", for: P.marcio, on: "CP-44" }, ago: "2d" },
  { from: "CP-38", to: "CP-49", kind: "blocks", by: { kind: "person", person: P.tom }, ago: "5d" },
  { from: "WEB-12", to: "CP-46", kind: "blocks", by: { kind: "person", person: P.kai }, ago: "6h" },
  { from: "CP-40", to: "CP-47", kind: "blocks", by: { kind: "person", person: P.jonas }, ago: "5d" },
  { from: "CP-44", to: "CP-48", kind: "blocks", by: { kind: "person", person: P.marcio }, ago: "1d" },
  { from: "CP-46", to: "CP-44", kind: "related", by: { kind: "agent", role: "brainstorm", for: P.kai, on: "a session" }, ago: "6h" },
  { from: "CP-44", to: "CP-45", kind: "related", by: { kind: "person", person: P.jonas }, ago: "1d" },
  { from: "CP-50", to: "CP-35", kind: "related", by: { kind: "agent", role: "conductor", for: P.marcio, on: "CP-35" }, ago: "3h" },
  { from: "CP-44", to: "CP-50", kind: "related", by: { kind: "person", person: P.marcio }, ago: "2d" },
];

/** Not started: where a start is offered. */
export const NOT_STARTED: ReadonlySet<TaskStatus> = new Set(["received", "intake", "awaiting_confirmation", "queued"]);

/** A blocker is resolved only when it is done (merged, or marked done); failed and aborted still block. */
export const resolved = (t: Task) => t.status === "done";

export const blockersOf = (key: string) => EDGES.filter((e) => e.kind === "blocks" && e.to === key).map((e) => ({ edge: e, task: task(e.from) }));
export const blockedOf = (key: string) => EDGES.filter((e) => e.kind === "blocks" && e.from === key).map((e) => ({ edge: e, task: task(e.to) }));
export const relatedOf = (key: string) =>
  EDGES.filter((e) => e.kind === "related" && (e.from === key || e.to === key)).map((e) => ({ edge: e, task: task(e.from === key ? e.to : e.from) }));
export const openBlockers = (key: string) => blockersOf(key).filter((b) => !resolved(b.task)).map((b) => b.task);
/** Not started, and nothing unresolved in front of it. */
export const startable = (t: Task) => NOT_STARTED.has(t.status) && openBlockers(t.key).length === 0;
export const isBlocked = (t: Task) => NOT_STARTED.has(t.status) && openBlockers(t.key).length > 0;

/** Every task one edge away, either way: what a hovered card lights up. */
export function neighbours(key: string): Set<string> {
  const out = new Set<string>();
  for (const e of EDGES) {
    if (e.from === key) out.add(e.to);
    if (e.to === key) out.add(e.from);
  }
  return out;
}

const NOW = Date.UTC(2026, 9, 12, 14, 0, 0);
export const now = NOW;
const H = 3_600_000;

function nav(t: Task): NavTask {
  const live = t.agent
    ? [{ id: `${t.key}-run`, attempt: 1, status: "running" as const, sessions: [{ id: `${t.key}-s`, role: t.agent.role, status: t.status === "awaiting_input" ? ("awaiting_input" as const) : ("running" as const), activity: t.agent.activity }] }]
    : [];
  return {
    id: `t_${t.key}`,
    key: t.key,
    title: t.title,
    status: t.status,
    people: [t.owner],
    runs: live,
    statusSince: NOW - t.hoursInStatus * H,
    costUsd: t.costUsd,
    pullRequests: t.pr
      ? [{ url: `https://github.com/acme/${t.project}/pull/${t.pr.number}`, number: t.pr.number, state: t.pr.state, repositoryName: `acme/${t.project}`,
          checks: "passing", review: t.status === "ready_to_merge" ? "approved" : "pending", mergeable: "clean" }]
      : undefined,
  };
}

/** Every task as the current scenario has it. */
export const allTasks = (): Task[] => TASKS.map((t) => task(t.key));

const inEpic = (epic: Task["epic"]) => allTasks().filter((t) => t.project === "control-plane" && t.epic === epic).map(nav);

/** The sidebar's and the board's view model, for the current scenario. */
export function world() {
  const billing: NavEpic = { id: "e_billing", title: "Billing v2", tasks: inEpic("billing") };
  const webhooks: NavEpic = { id: "e_webhooks", title: "Webhook reliability", tasks: inEpic("webhooks") };
  const controlPlane: NavProject = {
    id: "p_control-plane",
    name: "control-plane",
    epics: [billing, webhooks],
    tasks: allTasks().filter((t) => t.project === "control-plane" && !t.epic).map(nav),
  };
  const web: NavProject = {
    id: "p_web",
    name: "web",
    epics: [{ id: "e_shell", title: "App shell", tasks: allTasks().filter((t) => t.project === "web").map(nav) }],
  };
  return { billing, controlPlane, nav: [controlPlane, web] as readonly NavProject[] };
}

export const taskId = (key: string) => `t_${key}`;
export const keyOfId = (id: string) => id.replace(/^t_/, "");

/** CP-44's text, for its page. */
export const CP44 = {
  goal:
    "Ana's customers ask what they'll pay before the invoice arrives. The billing page shows **usage this month** from the hourly meter (CP-41), by day, and the **projected invoice** at the current rate, using the Stripe customer's plan (CP-43).",
  criteria: [
    "Usage this month, by day, from `usage_hourly`; empty days show as zero.",
    "Projected invoice = usage so far ÷ elapsed days × days in month, at the plan's price.",
    "An organisation with no Stripe customer sees usage only, and a line saying why.",
  ],
};

/** The brainstorm session's proposal: three tasks that relate to each other and to CP-41. */
export interface ProposedTask {
  readonly n: number;
  readonly title: string;
  readonly goal: string;
  /** Its project's key: the proposal's tasks may sit in different projects. */
  readonly project: { readonly key: string; readonly name: string };
  readonly blockedBy: ReadonlyArray<{ readonly item: number } | { readonly key: string }>;
  readonly related?: ReadonlyArray<{ readonly key: string }> | undefined;
}

export const DATA_PROJECT = { key: "DATA", name: "data-pipeline" };
export const CP_PROJECT = { key: "CP", name: "control-plane" };

export const PROPOSAL: readonly ProposedTask[] = [
  { n: 1, title: "Record when an organisation's usage crosses a plan threshold", goal: "One row per crossing (50%, 80%, 100% of the plan's included units), from the hourly meter.", project: DATA_PROJECT, blockedBy: [{ key: "CP-41" }] },
  { n: 2, title: "Email the organisation's owner at each threshold", goal: "Once per threshold per month; the email links to the billing page.", project: CP_PROJECT, blockedBy: [{ item: 1 }] },
  { n: 3, title: "Show the thresholds crossed on the billing page", goal: "A marker on the usage chart at each crossing this month.", project: CP_PROJECT, blockedBy: [{ item: 1 }], related: [{ key: "CP-44" }] },
];
