/*
 * The tracking mockup's world: the relations mockup's Acme and its
 * control-plane project (CP), with Billing v2 and Webhook reliability,
 * grown by bugs, spikes, a chore, tasks on the Later shelf, closed tasks
 * with their resolutions, a duplicate pair, an epic put later, and
 * archived work. Ana owns most of it; Márcio is a member. Deterministic,
 * so screenshots compare.
 */

import type { AgentRole, TaskStatus } from "@dude/domain";
import type { Person } from "../../../../packages/design-system/src/components/PersonAvatar.tsx";
import type { NavEpic, NavProject, NavRelated, NavTask } from "../../../../packages/design-system/src/util/navModel.ts";

export const P = {
  marcio: { id: "u_marcio", name: "Márcio Martins", online: true },
  ana: { id: "u_ana", name: "Ana Ribeiro", online: true },
  tom: { id: "u_tom", name: "Tom Okafor" },
} satisfies Record<string, Person>;

export type Role = "owner" | "member";
const params = new URLSearchParams(location.search);
/** `role=owner` reads the mockup as Ana, who owns most tasks; `member` as Márcio. */
export const ROLE: Role = params.get("role") === "member" ? "member" : "owner";
export const YOU: Person = ROLE === "owner" ? P.ana : P.marcio;
export const firstName = (p: Person) => p.name.split(" ")[0]!;

export type Kind = "bug" | "feature" | "chore" | "spike";
export type Priority = "P0" | "P1" | "P2" | "P3";
export type Resolution = "wont_do" | "duplicate" | "cant_reproduce" | "nothing_to_merge";
export type EpicId = "billing" | "webhooks" | "alerts";

export interface BugReport {
  readonly steps: readonly string[];
  readonly expected: string;
  readonly actual: string;
  readonly environment: string;
  /** What caused it: proposed by an agent until the owner confirms, or set by hand. */
  readonly introducedBy?: { readonly key: string; readonly merged: string; readonly state: "proposed" | "confirmed"; readonly by: string } | undefined;
}

export interface Task {
  readonly key: string;
  readonly title: string;
  readonly status: TaskStatus;
  readonly kind: Kind;
  readonly priority: Priority;
  readonly epic?: EpicId | undefined;
  readonly owner: Person;
  readonly hoursInStatus: number;
  readonly costUsd?: number | undefined;
  readonly pr?: { readonly number: number; readonly state: "open" | "merged" | "closed" } | undefined;
  readonly agent?: { readonly role: AgentRole; readonly activity: string } | undefined;
  /** On the Later shelf: until a date, or until someone brings it back. */
  readonly later?: { readonly until: string | null; readonly by: Person; readonly ago: string } | undefined;
  readonly resolution?: Resolution | undefined;
  readonly duplicateOf?: string | undefined;
  readonly archived?: { readonly by: Person; readonly ago: string } | undefined;
  readonly bug?: BugReport | undefined;
  readonly blockedBy?: readonly string[] | undefined;
}

export const EPICS: Record<EpicId, { readonly id: string; readonly title: string; readonly later?: { readonly until: string; readonly by: Person } }> = {
  billing: { id: "e_billing", title: "Billing v2" },
  webhooks: { id: "e_webhooks", title: "Webhook reliability" },
  alerts: { id: "e_alerts", title: "Usage alerts", later: { until: "1 Dec", by: P.ana } },
};

export const TASKS: readonly Task[] = [
  // Billing v2.
  { key: "CP-40", title: "Usage events table and ingestion endpoint", status: "done", kind: "feature", priority: "P1", epic: "billing", owner: P.ana, hoursInStatus: 340, costUsd: 6.4, pr: { number: 412, state: "merged" } },
  { key: "CP-41", title: "Meter usage per organisation, hourly", status: "review", kind: "feature", priority: "P1", epic: "billing", owner: P.ana, hoursInStatus: 5, costUsd: 9.1, pr: { number: 431, state: "open" } },
  { key: "CP-43", title: "Sync Stripe customers to organisations", status: "running", kind: "feature", priority: "P2", epic: "billing", owner: P.tom, agent: { role: "implementer", activity: "Running bun test" }, hoursInStatus: 2, costUsd: 3.2 },
  { key: "CP-42", title: "Invoice line items from metered usage", status: "received", kind: "feature", priority: "P1", epic: "billing", owner: P.ana, hoursInStatus: 20, blockedBy: ["CP-41"] },
  { key: "CP-44", title: "Billing page: usage this month and projected invoice", status: "received", kind: "feature", priority: "P2", epic: "billing", owner: P.marcio, hoursInStatus: 30 },
  { key: "CP-47", title: "Export usage as CSV", status: "received", kind: "feature", priority: "P3", epic: "billing", owner: P.ana, hoursInStatus: 50, blockedBy: ["CP-62"] },
  { key: "CP-37", title: "Spike: price per unit or per tier?", status: "done", kind: "spike", priority: "P2", epic: "billing", owner: P.ana, hoursInStatus: 30, costUsd: 2.6, resolution: "nothing_to_merge" },
  { key: "CP-38", title: "Spike: Stripe Tax for EU VAT", status: "failed", kind: "spike", priority: "P2", epic: "billing", owner: P.tom, hoursInStatus: 40, costUsd: 4.7 },
  { key: "CP-61", title: "Hourly meter counts the DST hour twice", status: "running", kind: "bug", priority: "P1", epic: "billing", owner: P.ana, agent: { role: "implementer", activity: "Writing the failing test" }, hoursInStatus: 1, costUsd: 1.4,
    bug: { steps: ["Set an organisation's time zone to Europe/Lisbon.", "Send usage events every minute across 01:00–02:00 on 26 Oct.", "Read `usage_hourly` for that night."], expected: "One row per hour, 25 hours that day.", actual: "The 01:00 hour is written twice, with double the units.", environment: "control-plane 1.18.2, production", introducedBy: { key: "CP-40", merged: "28 Sep", state: "confirmed", by: "Ana" } } },
  { key: "CP-62", title: "CSV export drops rows when an organisation's name has a comma", status: "received", kind: "bug", priority: "P2", epic: "billing", owner: P.ana, hoursInStatus: 70,
    later: { until: "3 Nov", by: P.ana, ago: "2d" },
    bug: { steps: ["Rename an organisation to “Acme, Inc.”.", "Export usage as CSV from the billing page."], expected: "Every hour of usage, one row each.", actual: "Rows for Acme, Inc. are missing; the file has one column too many on those lines.", environment: "control-plane 1.18.0, staging" } },
  { key: "CP-63", title: "CSV export breaks on commas", status: "aborted", kind: "bug", priority: "P2", epic: "billing", owner: P.ana, hoursInStatus: 26, resolution: "duplicate", duplicateOf: "CP-62" },
  { key: "CP-64", title: "Spike: can Stripe meter events replace our hourly job?", status: "received", kind: "spike", priority: "P3", epic: "billing", owner: P.ana, hoursInStatus: 90, later: { until: null, by: P.ana, ago: "5d" } },
  // Webhook reliability.
  { key: "CP-30", title: "Dedupe deliveries by X-GitHub-Delivery", status: "done", kind: "feature", priority: "P1", epic: "webhooks", owner: P.ana, hoursInStatus: 340, costUsd: 5.2, pr: { number: 398, state: "merged" } },
  { key: "CP-35", title: "Retry the GitHub webhook handler with backoff", status: "awaiting_input", kind: "feature", priority: "P1", epic: "webhooks", owner: P.ana, agent: { role: "implementer", activity: "Cap retries at 5 or 8 attempts?" }, hoursInStatus: 1, costUsd: 2.1 },
  { key: "CP-60", title: "Deliveries queued during a deploy are dropped", status: "received", kind: "bug", priority: "P0", epic: "webhooks", owner: P.ana, hoursInStatus: 3,
    bug: { steps: ["Start a deploy of control-plane.", "Push to a watched repository while the old process drains.", "Look for the push in the task's Activity."], expected: "The delivery is processed once the new process is up.", actual: "It is acknowledged to GitHub and never processed.", environment: "control-plane 1.18.2, production", introducedBy: { key: "CP-30", merged: "2 Oct", state: "proposed", by: "Ana's conductor" } } },
  { key: "CP-36", title: "Replay endpoint for missed deliveries", status: "ready_to_merge", kind: "feature", priority: "P2", epic: "webhooks", owner: P.ana, hoursInStatus: 9, costUsd: 7.3, pr: { number: 427, state: "open" } },
  { key: "CP-59", title: "Duplicate webhooks on GitHub redelivery", status: "aborted", kind: "bug", priority: "P2", epic: "webhooks", owner: P.ana, hoursInStatus: 60, resolution: "cant_reproduce" },
  // Usage alerts, put later as a whole: its tasks inherit the refusal.
  { key: "CP-67", title: "Record when usage crosses a plan threshold", status: "received", kind: "feature", priority: "P2", epic: "alerts", owner: P.ana, hoursInStatus: 120 },
  { key: "CP-68", title: "Email the organisation's owner at each threshold", status: "received", kind: "feature", priority: "P2", epic: "alerts", owner: P.ana, hoursInStatus: 120 },
  // No epic.
  { key: "CP-50", title: "Rate-limit the public API per key", status: "queued", kind: "feature", priority: "P1", epic: "webhooks", owner: P.ana, hoursInStatus: 18 },
  { key: "CP-65", title: "Bump pgx to 5.7", status: "received", kind: "chore", priority: "P3", owner: P.marcio, hoursInStatus: 6 },
  { key: "CP-46", title: "Show the organisation's usage in the sidebar footer", status: "aborted", kind: "feature", priority: "P3", owner: P.ana, hoursInStatus: 30, resolution: "wont_do" },
  // Archived: out of the board, the tree, the counts and Find work.
  { key: "CP-29", title: "Verify webhook signatures", status: "done", kind: "feature", priority: "P1", epic: "webhooks", owner: P.ana, hoursInStatus: 900, costUsd: 3.9, pr: { number: 371, state: "merged" }, archived: { by: P.ana, ago: "12d" } },
  { key: "CP-33", title: "Spike: queue webhooks in Redis", status: "aborted", kind: "spike", priority: "P3", epic: "webhooks", owner: P.ana, hoursInStatus: 700, resolution: "wont_do", archived: { by: P.ana, ago: "9d" } },
  { key: "CP-34", title: "Webhook 500s under load", status: "aborted", kind: "bug", priority: "P1", epic: "webhooks", owner: P.ana, hoursInStatus: 500, resolution: "cant_reproduce", archived: { by: P.marcio, ago: "2d" } },
];

export const task = (key: string): Task => {
  const t = TASKS.find((x) => x.key === key);
  if (!t) throw new Error(`no task ${key}`);
  return t;
};

/** The shelf a task is on, its own or its epic's. */
export function laterOf(t: Task): { readonly until: string | null; readonly by: Person; readonly epic?: string } | null {
  if (t.later) return { until: t.later.until, by: t.later.by };
  const e = t.epic ? EPICS[t.epic].later : undefined;
  return e ? { until: e.until, by: e.by, epic: EPICS[t.epic!].title } : null;
}
export const onShelf = (t: Task) => laterOf(t) !== null && !t.archived;
export const NOT_STARTED: ReadonlySet<TaskStatus> = new Set(["received", "intake", "awaiting_confirmation", "queued"]);
export const FINISHED: ReadonlySet<TaskStatus> = new Set(["done", "failed", "aborted"]);
export const merged = (t: Task) => t.status === "done" && t.pr?.state === "merged";
export const isOwner = (t: Task) => t.owner.id === YOU.id;

/** Closing with a resolution: the status it ends in, and the words. */
export const RESOLUTION: Record<Resolution, { readonly label: string; readonly status: TaskStatus; readonly description: string }> = {
  wont_do: { label: "Won't do", status: "aborted", description: "Not worth doing, or no longer. It still blocks what waits on it." },
  duplicate: { label: "Duplicate", status: "aborted", description: "Another task covers it. What waited on this waits on that one now." },
  cant_reproduce: { label: "Can't reproduce", status: "aborted", description: "A bug nobody can make happen again. Reopen it if it comes back." },
  nothing_to_merge: { label: "Nothing to merge", status: "done", description: "Finished, and the answer is the work: a write-up, a decision. Mark done gives this one." },
};

export const KIND: Record<Kind, { readonly label: string; readonly plural: string; readonly line: string }> = {
  bug: { label: "Bug", plural: "Bugs", line: "Something that worked, or should, doesn't. Reproduced and pinned by a failing test before it is fixed." },
  feature: { label: "Feature", plural: "Features", line: "New or changed behaviour. The default." },
  chore: { label: "Chore", plural: "Chores", line: "Upkeep: dependencies, clean-up, tooling. Delivered and reviewed like a feature." },
  spike: { label: "Spike", plural: "Spikes", line: "A question to answer. Done is a write-up, with nothing to merge." },
};

export const PRIORITY: Record<Priority, { readonly label: string; readonly line: string }> = {
  P0: { label: "P0", line: "Drop other work for it." },
  P1: { label: "P1", line: "Next, before the normal queue." },
  P2: { label: "P2", line: "Normal: the default. Cards and rows leave it out." },
  P3: { label: "P3", line: "When there is room." },
};

const NOW = Date.UTC(2026, 9, 16, 14, 0, 0);
const H = 3_600_000;

function nav(t: Task): NavTask {
  const live = t.agent
    ? [{ id: `${t.key}-run`, attempt: 1, status: "running" as const, sessions: [{ id: `${t.key}-s`, role: t.agent.role, status: t.status === "awaiting_input" ? ("awaiting_input" as const) : ("running" as const), activity: t.agent.activity }] }]
    : [];
  const blockers: NavRelated[] = (t.blockedBy ?? []).map(task).filter((b) => !merged(b)).map((b) => ({ key: b.key, title: b.title, status: b.status }));
  return {
    id: `t_${t.key}`, key: t.key, title: t.title, status: t.status, people: [t.owner], runs: live,
    statusSince: NOW - t.hoursInStatus * H, costUsd: t.costUsd,
    merged: merged(t),
    blockedBy: NOT_STARTED.has(t.status) ? blockers : [],
    startable: NOT_STARTED.has(t.status) && blockers.length === 0 && !onShelf(t),
    canStart: NOT_STARTED.has(t.status) && !onShelf(t),
    pullRequests: t.pr ? [{ url: `https://github.com/acme/control-plane/pull/${t.pr.number}`, number: t.pr.number, state: t.pr.state, repositoryName: "acme/control-plane", checks: "passing", review: t.status === "ready_to_merge" ? "approved" : "pending", mergeable: "clean" }] : undefined,
  };
}

/** Everything the board and the tree show: not archived, not on the shelf. */
export const everyday = (t: Task) => !t.archived && !onShelf(t);
export const keyOf = (id: string) => id.replace(/^t_/, "");

/**
 * The sidebar's and the board's model. `keep` narrows the cards (the
 * board's filters); the shelf is drawn by the page, and the tree's "Later"
 * row is a stand-in epic (the proposal is a row of its own, see the doc).
 */
export function world(keep: (t: Task) => boolean = () => true, withShelfRow = false): NavProject {
  const epics: NavEpic[] = (["billing", "webhooks"] as const).map((e) => ({
    id: EPICS[e].id, title: EPICS[e].title, tasks: TASKS.filter((t) => t.epic === e && everyday(t) && keep(t)).map(nav),
  }));
  if (withShelfRow) epics.push({ id: "e_later", title: "Later", tasks: TASKS.filter((t) => onShelf(t)).map(nav) });
  return { id: "p_cp", name: "control-plane", epics, tasks: TASKS.filter((t) => !t.epic && everyday(t) && keep(t)).map(nav) };
}

export const shelf = () => TASKS.filter(onShelf);
export const archived = () => TASKS.filter((t) => t.archived);
