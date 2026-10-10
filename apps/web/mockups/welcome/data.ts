/*
 * The mockup's world: Márcio, the gallery's projects, and his sessions.
 * Deterministic but for "now", so the greeting reads naturally.
 */

import type { Person } from "../../../../packages/design-system/src/components/PersonAvatar.tsx";
import type { IconName } from "../../../../packages/design-system/src/icons/index.tsx";

export const P: Record<string, Person> = {
  marcio: { id: "u_marcio", name: "Márcio Martins", online: true },
  ana: { id: "u_ana", name: "Ana Ribeiro", online: true },
};

export const PROJECTS = [
  { key: "CP", id: "p_control", name: "control-plane", repositories: [{ name: "control-plane", defaultBranch: "main" }] },
  { key: "WEB", id: "p_web", name: "web", repositories: [{ name: "web", defaultBranch: "main" }] },
  { key: "RUN", id: "p_runner", name: "runner", repositories: [{ name: "runner", defaultBranch: "main" }] },
  { key: "DOC", id: "p_docs", name: "docs", repositories: [{ name: "docs", defaultBranch: "main" }] },
] as const;

export type Project = (typeof PROJECTS)[number];

/** The greeting's word for now: the person's clock, never the server's. */
export function partOfDay(d = new Date()): string {
  const h = d.getHours();
  if (h < 5) return "Late one";
  if (h < 12) return "Morning";
  if (h < 18) return "Afternoon";
  return "Evening";
}

/** A way to start: a name, what it does, and the words it puts in the composer. */
export interface Starter {
  readonly id: string;
  readonly icon: IconName;
  readonly title: string;
  readonly detail: string;
  /** What choosing it writes in the composer, for the person to finish; never sent by itself. */
  readonly prompt: string;
}

export const STARTERS: readonly Starter[] = [
  { id: "epic", icon: "layers", title: "Plan an epic", detail: "Turn a goal into an epic and tasks you can file.",
    prompt: "I want to plan an epic for " },
  { id: "task", icon: "edit", title: "Shape a task", detail: "From a rough idea to a goal and acceptance criteria.",
    prompt: "Help me write a task for " },
  { id: "code", icon: "search", title: "Ask the code", detail: "How something works, where it lives, what it touches.",
    prompt: "How does " },
  { id: "triage", icon: "list-check", title: "Triage what's open", detail: "Findings, failed tasks and stale PRs, with what to do.",
    prompt: "Go through what's open and tell me what to do first." },
];

/** A session in the welcome's short list: its name, what came of it, how long ago. */
export interface Recent {
  readonly id: string;
  readonly title: string;
  readonly summary: string;
  readonly age: string;
  readonly shared?: boolean;
  readonly owner?: Person;
  /** For the mockup: what opening it shows. */
  readonly first: string;
  readonly reply: string;
  readonly linked: readonly Project[];
}

export const RECENT: readonly Recent[] = [
  { id: "s1", title: "Usage-based billing", summary: "Filed 1 epic, 4 tasks", age: "yesterday",
    first: "I want to plan an epic for usage-based billing: watch-only first, charge later.",
    reply: "Here is how I'd split it: a **watch-only meter** first (count experiment runs per org per day, charge nothing), then a usage panel with an estimate, then pricing. I've proposed the epic and four tasks below.",
    linked: [PROJECTS[0], PROJECTS[1]] },
  { id: "s2", title: "Q4 cleanup ideas", summary: "Nothing filed yet", age: "3 days ago", shared: true, owner: P["ana"],
    first: "What should we clean up before Q4? Start with the flaky tests.",
    reply: "Three tests fail more than 1 in 20 runs on main; all three wait on a fixed sleep. Want tasks for them?",
    linked: [PROJECTS[0]] },
  { id: "s3", title: "Runner sandbox egress", summary: "Filed 2 tasks · edited RUN-31", age: "last week",
    first: "How does the runner decide what an agent can reach?",
    reply: "Per trust class: `egress.allow` in the runner's config, enforced by the sandbox's network namespace.",
    linked: [PROJECTS[2]] },
  { id: "s4", title: "Docs IA", summary: "Filed 1 epic", age: "2 weeks ago",
    first: "Help me restructure the docs navigation.", reply: "Proposed: Concepts, Guides, Reference, Operations.", linked: [PROJECTS[3]] },
  { id: "s5", title: "Webhook retry policy", summary: "Edited WI-2401", age: "3 weeks ago",
    first: "Should the webhook handler retry 4xx?", reply: "Only 408 and 429; the rest are the caller's fault.", linked: [PROJECTS[0]] },
  { id: "s6", title: "Onboarding checklist", summary: "Nothing filed yet", age: "a month ago",
    first: "What does a new engineer need on day one?", reply: "Access, a seeded dev env, and one small task with a reviewer.", linked: [] },
];
