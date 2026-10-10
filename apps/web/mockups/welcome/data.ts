/*
 * The mockup's world: Márcio, the gallery's projects, and what the app
 * already knows when he presses New session — a question waiting on him, a
 * task that failed twice, an epic in review, yesterday's session. Nothing
 * here is new data: every line is something the sidebar or the board
 * already reads.
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
    prompt: "Go through what's open in control-plane and tell me what to do first." },
];

/** What dude already knows that is worth talking through: each a real thing, and the words it would start with. */
export interface Lead {
  readonly id: string;
  readonly kind: "waiting" | "failed" | "epic" | "session";
  readonly project?: Project;
  readonly heading: string;
  readonly title: string;
  readonly detail: string;
  readonly prompt: string;
}

export const LEADS: readonly Lead[] = [
  { id: "l1", kind: "waiting", project: PROJECTS[0], heading: "Waiting on you · 12m",
    title: "WI-2401 asks: should 4xx responses be retried?",
    detail: "Think it through before you answer the conductor.",
    prompt: "WI-2401's conductor asks whether 4xx responses should be retried. Help me think it through." },
  { id: "l2", kind: "failed", project: PROJECTS[0], heading: "Failed twice",
    title: "WI-2408 · Pause graceful vs hard",
    detail: "Work out why it keeps failing, and what to change.",
    prompt: "WI-2408 has failed twice. Read both attempts and tell me what is going wrong." },
  { id: "l3", kind: "epic", project: PROJECTS[1], heading: "Epic · 2 in review, 1 queued",
    title: "Chat interface",
    detail: "Plan what comes after the open reviews.",
    prompt: "Look at the Chat interface epic in web and propose what comes next." },
  { id: "l4", kind: "session", heading: "Your session · yesterday",
    title: "Usage-based billing",
    detail: "Filed 1 epic and 4 tasks. Pick it back up.",
    prompt: "" },
];
