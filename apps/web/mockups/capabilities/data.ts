/*
 * The mockup's world: Acme, its projects control-plane and billing, and
 * what each of the eight agent roles may do. Each capability is layered
 * like every role setting: the organisation's value, a project's override.
 * The tools each role has follow orchestrator/internal/agenttools (the
 * fixer runs as the implementer there, so it has the implementer's).
 */

import type { SettingsRole } from "@dude/domain";
import type { Person } from "../../../../packages/design-system/src/components/PersonAvatar.tsx";

export const ORG = "Acme";
export const P: Record<string, Person> = {
  marcio: { id: "u_marcio", name: "Márcio Martins", online: true },
  ana: { id: "u_ana", name: "Ana Ribeiro", online: true },
};

export const ROLES = ["conductor", "brainstorm", "investigator", "implementer", "reviewer", "fixer", "simplifier", "qa_browser"] as const satisfies readonly SettingsRole[];
export type Role = (typeof ROLES)[number];

export const ROLE_LABEL: Record<Role, string> = {
  conductor: "Conductor",
  brainstorm: "Brainstorm",
  investigator: "Investigator",
  implementer: "Implementer",
  reviewer: "Reviewer",
  fixer: "Fixer",
  simplifier: "Simplifier",
  qa_browser: "Tester",
};

export const ROLE_DESCRIPTION: Record<Role, string> = {
  conductor: "Answers questions about a task in its Chat, from the code and dude's records.",
  brainstorm: "Thinks with a session's members across linked projects; reads, proposes, never changes code.",
  investigator: "Reads the code before any is written, and reports.",
  implementer: "Writes the change and its tests.",
  reviewer: "Reviews the change by category; reports findings, never pushes.",
  fixer: "Fixes exactly the findings or comments it is given.",
  simplifier: "Tidies the branch without changing what it does.",
  qa_browser: "Drives the app in a browser and records a video.",
};

/** The avatar a role wears: the fixer is an implementer. */
export const faceOf = (r: Role) => (r === "fixer" ? "implementer" : r);

// ---------------------------------------------------------------- the model

/** How far a Run may reach, narrowest first. */
export const INTERNET_LEVELS = ["dude", "lists", "any"] as const;
export type Internet = (typeof INTERNET_LEVELS)[number];

export const INTERNET_LABEL: Record<Internet, string> = { dude: "dude only", lists: "Your lists", any: "Any host" };
export const INTERNET_DESCRIPTION: Record<Internet, string> = {
  dude: "Its model and dude’s tools. No other host: npm install and git fetch from elsewhere fail.",
  lists: "Acme’s hosts and the project’s, as the Network page lists them.",
  any: "Every host on the internet. The lists are not applied.",
};

/** The tools that act outside the Run, as the settings name them. */
export const ACTS = ["ask", "remember", "create", "repos", "pulls"] as const;
export type Act = (typeof ACTS)[number];

export const ACT_LABEL: Record<Act, string> = {
  ask: "Ask people",
  remember: "Remember",
  create: "Create tasks",
  repos: "Request repositories",
  pulls: "Open and reply on pull requests",
};

/** One line on what the tool does; the brainstorm's differ, so they are its own. */
export function actDescription(role: Role, act: Act): string {
  if (role === "brainstorm") {
    return {
      ask: "Puts a question to the session’s members, or to one of them by name.",
      remember: "Saves what is worth knowing next time, for the session’s members only.",
      create: "Proposes epics, tasks, edits and comments; a member files them, as themselves.",
      repos: "",
      pulls: "",
    }[act];
  }
  return {
    ask: "Puts a question to the task’s owner and waits for the answer.",
    remember: "Saves a fact, a procedure or a trap to the project’s shared memory, live at once.",
    create: "Files a task it found outside its own, not started; a person decides.",
    repos:
      role === "implementer" || role === "fixer"
        ? "Asks for another of the project’s repositories, to read or to change; a person decides."
        : "Asks for another of the project’s repositories, to read; a person decides.",
    pulls: "Opens the task’s pull request once the owner says Open or Draft, and answers comments on it as dude.",
  }[act];
}

/** What each role has (agenttools): a capability it lacks is not shown at all. */
export const HAS: Record<Role, readonly Act[]> = {
  conductor: ["ask", "remember", "create", "repos", "pulls"],
  brainstorm: ["ask", "remember", "create"],
  investigator: ["ask", "remember", "create", "repos"],
  implementer: ["ask", "remember", "create", "repos"],
  reviewer: ["remember", "repos"],
  fixer: ["ask", "remember", "create", "repos"],
  simplifier: ["remember", "repos"],
  qa_browser: ["remember", "repos"],
};

/** Fixed by role: shown, never changed. `can: false` reads as a cross. */
export const FIXED: Record<Role, ReadonlyArray<{ readonly text: string; readonly can: boolean }>> = {
  conductor: [
    { text: "Publishes small changes to the task branch, within the project’s limit", can: true },
    { text: "Steers, starts and restarts the task’s other agents", can: true },
    { text: "Takes the delivery’s decisions while the owner lets it", can: true },
  ],
  brainstorm: [
    { text: "Reads the linked projects’ code; never changes it", can: false },
    { text: "Files nothing itself: members file what it proposes", can: false },
  ],
  investigator: [{ text: "Reads code only; never pushes", can: false }],
  implementer: [{ text: "Pushes its branch", can: true }],
  reviewer: [{ text: "Reads code only; reports findings, never pushes", can: false }],
  fixer: [{ text: "Pushes its branch", can: true }],
  simplifier: [{ text: "Pushes its branch", can: true }],
  qa_browser: [{ text: "Reads code only; records a video of the app", can: false }],
};

export type Harness = "claude_code" | "opencode" | "codex";
export const HARNESS_LABEL: Record<Harness, string> = { claude_code: "Claude Code", opencode: "OpenCode", codex: "Codex" };

export interface Caps {
  readonly internet: Internet;
  readonly webSearch: boolean;
  readonly containers: boolean;
  readonly acts: Partial<Record<Act, boolean>>;
}

/** Every role setting the page shows above Capabilities, as the app's RolePage has them. */
export interface RoleSetup {
  readonly harness: Harness;
  readonly tier: string;
  readonly model: string;
  readonly machine: string;
  readonly image: string;
  readonly imageCanRunContainers: boolean;
}

export const SETUP: Record<Role, RoleSetup> = {
  conductor: { harness: "claude_code", tier: "Claude (High)", model: "claude-opus-5-5", machine: "Small", image: "acme-base", imageCanRunContainers: false },
  brainstorm: { harness: "claude_code", tier: "Claude (High)", model: "claude-opus-5-5", machine: "Small", image: "acme-base", imageCanRunContainers: false },
  investigator: { harness: "opencode", tier: "Fast", model: "claude-sonnet-5", machine: "Medium", image: "acme-base", imageCanRunContainers: false },
  implementer: { harness: "claude_code", tier: "Claude (High)", model: "claude-opus-5-5", machine: "Large", image: "acme-base", imageCanRunContainers: false },
  reviewer: { harness: "codex", tier: "GPT (High)", model: "gpt-5.5", machine: "Medium", image: "acme-base", imageCanRunContainers: false },
  fixer: { harness: "claude_code", tier: "Claude (High)", model: "claude-opus-5-5", machine: "Large", image: "acme-base", imageCanRunContainers: false },
  simplifier: { harness: "opencode", tier: "Fast", model: "claude-sonnet-5", machine: "Medium", image: "acme-base", imageCanRunContainers: false },
  qa_browser: { harness: "claude_code", tier: "Fast", model: "claude-sonnet-5", machine: "Large", image: "acme-browser", imageCanRunContainers: true },
};

const all = (role: Role): Partial<Record<Act, boolean>> => Object.fromEntries(HAS[role].map((a) => [a, true]));

/** Acme's values: every role on its lists, the brainstorm on any host (moved from #110's switch). */
export const ORG_CAPS: Record<Role, Caps> = {
  conductor: { internet: "lists", webSearch: false, containers: false, acts: all("conductor") },
  brainstorm: { internet: "any", webSearch: true, containers: false, acts: all("brainstorm") },
  investigator: { internet: "lists", webSearch: false, containers: false, acts: all("investigator") },
  implementer: { internet: "lists", webSearch: false, containers: false, acts: all("implementer") },
  reviewer: { internet: "lists", webSearch: false, containers: false, acts: all("reviewer") },
  fixer: { internet: "lists", webSearch: false, containers: false, acts: { ...all("fixer"), create: false } },
  simplifier: { internet: "lists", webSearch: false, containers: false, acts: all("simplifier") },
  qa_browser: { internet: "lists", webSearch: false, containers: true, acts: all("qa_browser") },
};

/** A project's overrides: only what it sets; the rest is Acme's. */
export type Overrides = Partial<Record<Role, { internet?: Internet; webSearch?: boolean; containers?: boolean; acts?: Partial<Record<Act, boolean>> }>>;

export const PROJECTS = ["control-plane", "billing"] as const;
export type ProjectName = (typeof PROJECTS)[number];

export const PROJECT_OVERRIDES: Record<ProjectName, Overrides> = {
  // Wider: the implementer searches the web here; narrower: the reviewer reaches nothing, the investigator saves nothing.
  "control-plane": {
    implementer: { webSearch: true },
    reviewer: { internet: "dude" },
    investigator: { acts: { remember: false } },
  },
  // Wider: the investigator reads any host to compare payment providers; narrower: the brainstorm keeps to the lists.
  billing: {
    investigator: { internet: "any" },
    brainstorm: { internet: "lists" },
  },
};

/** A value, where it comes from, and whether a project's goes beyond Acme's. */
export interface Layered<T> {
  readonly value: T;
  readonly source: "organization" | "project";
  readonly organization: T;
  readonly wider: boolean;
}

const internetRank = (i: Internet) => INTERNET_LEVELS.indexOf(i);

function layer<T>(org: T, own: T | undefined, wider: (own: T, org: T) => boolean): Layered<T> {
  if (own === undefined || own === org) return { value: org, source: "organization", organization: org, wider: false };
  return { value: own, source: "project", organization: org, wider: wider(own, org) };
}

const onWhenOff = (own: boolean, org: boolean) => own && !org;

export interface Resolved {
  readonly internet: Layered<Internet>;
  readonly webSearch: Layered<boolean>;
  readonly containers: Layered<boolean>;
  readonly acts: Partial<Record<Act, Layered<boolean>>>;
}

export function resolve(role: Role, overrides: Overrides | null): Resolved {
  const org = ORG_CAPS[role];
  const own = overrides?.[role];
  return {
    internet: layer(org.internet, own?.internet, (a, b) => internetRank(a) > internetRank(b)),
    webSearch: layer(org.webSearch, own?.webSearch, onWhenOff),
    containers: layer(org.containers, own?.containers, onWhenOff),
    acts: Object.fromEntries(HAS[role].map((a) => [a, layer(org.acts[a] ?? false, own?.acts?.[a], onWhenOff)])),
  };
}

/** The roles that hold something worth taking: every task role has a checkout and a push or read token; all have the model's key. */
export const HOLDS: Record<Role, string> = {
  conductor: "a checkout of the task’s branch it can publish to, the model’s key and dude’s tools token",
  brainstorm: "read-only checkouts of the repositories linked to its session, and the model’s key",
  investigator: "a read-only checkout of the project’s repositories and the model’s key",
  implementer: "a writable checkout of the project’s repositories, the token lux pushes its branch with, and the model’s key",
  reviewer: "a read-only checkout of the change and the model’s key",
  fixer: "a writable checkout of the task’s branch, the token lux pushes it with, and the model’s key",
  simplifier: "a writable checkout of the task’s branch, the token lux pushes it with, and the model’s key",
  qa_browser: "a checkout of the change, the app’s test secrets and the model’s key",
};

/** A session in flight, and a Run, for screen 6. */
export const SESSION = { title: "Usage-based billing: which provider", linked: ["billing"] };
export const RUN = { key: "CP-212", title: "Retry webhook deliveries with backoff", branch: "dude/cp-212-retry-webhooks" };
