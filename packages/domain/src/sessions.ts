/**
 * Brainstorm sessions as the API gives them (/v1/brainstorms): a
 * conversation with an agent that belongs to its members, not a task.
 */

import type { Harness } from "./harnesses.ts";
import type { PersonRef } from "./hierarchy.ts";
import type { AskItem } from "./questions.ts";
import type { AgentMachine } from "./machines.ts";

/** What a member may do: the owner (exactly one), write and file, or read. */
export type SessionRole = "owner" | "chat" | "read";

/** What a session is called until its agent or a member names it. */
export const UNTITLED_SESSION = "Untitled session";

/** A session's name as people see it: its title, or "Untitled session" until it has one. */
export function sessionTitle(s: { title: string | null }): string {
  return s.title ?? UNTITLED_SESSION;
}

export interface SessionProjectRef {
  id: string;
  key: string;
  name: string;
  repositories: Array<{ id: string; name: string; defaultBranch: string }>;
}

/** A session in your list. */
export interface SessionSummary {
  id: string;
  /** null until it is named. */
  title: string | null;
  role: SessionRole;
  createdAt: string;
  owner: PersonRef;
  /** Someone else is in it too. */
  shared: boolean;
  projects: SessionProjectRef[];
  runStatus: string | null;
  dudePause: string | null;
  filed: number;
  lastActivityAt: string;
  /** You archived it: listed only when archived ones are asked for (`?archived=1`). Yours alone. */
  archived: boolean;
}

/** An invitation waiting on you: what an inbox line shows, never a word said. */
export interface SessionInvitation {
  id: string;
  title: string | null;
  role: SessionRole;
  /** A handover: accepting makes you its owner. */
  becomesOwner: boolean;
  invitedAt: string;
  invitedBy: PersonRef | null;
  people: PersonRef[];
  projects: SessionProjectRef[];
  messages: number;
}

/** A question a session's agent put to you. */
export interface SessionQuestionLine {
  id: string;
  prompt: string;
  options: string[];
  /** What it asks, one to four questions (their headers name a line for several). */
  items?: AskItem[];
  askedAt: string;
  sessionId: string;
  title: string | null;
}

export interface SessionsList {
  sessions: SessionSummary[];
  invitations: SessionInvitation[];
  questions: SessionQuestionLine[];
}

export interface SessionMemberView {
  person: PersonRef;
  role: SessionRole;
  accepted: boolean;
  becomesOwner: boolean;
  /** Has the session open now. */
  open: boolean;
}

/** A task a proposed item relates to: an existing one by key, or an item of the same card by number (1-based). */
export interface ProposalRef {
  key?: string;
  item?: number;
}

export interface ProposalItem {
  kind: "epic" | "task" | "edit" | "comment" | "note" | "relation";
  project?: string;
  /** A task's or a note's epic: an existing one's title, or a new epic's of this proposal. */
  epic?: string;
  title?: string;
  goal?: string;
  acceptanceCriteria?: string[];
  description?: string;
  /** An epic's notes, pinned on it when it is filed, in order. */
  notes?: string[];
  task?: string;
  before?: { goal?: string; acceptanceCriteria?: string[] };
  after?: { goal?: string; acceptanceCriteria?: string[] };
  /** A comment's words, or a note's. */
  text?: string;
  /** A note: the number of the note it replaces on its epic. */
  replaces?: number;
  /** A task item's relations. */
  blockedBy?: ProposalRef[];
  blocks?: ProposalRef[];
  related?: ProposalRef[];
  /** A relation item (a brainstorm's relate): `from` blocks or is related to `to`, existing tasks by key. */
  from?: string;
  to?: string;
  relationKind?: "blocks" | "related";
  why?: string;
}

/** An item's state for the person looking. */
export interface ProposalItemStatus {
  filed?: boolean;
  filedBy?: string;
  key?: string;
  canFile?: boolean;
  why?: string;
  /** The card's unfiled items (0-based) filing this one files with it: what it waits on. */
  filesWith?: number[];
  /**
   * What blocks an item the person can't file: `owner` (its task's owner
   * may, named in `owner`) and `reader` (a member who can chat may) leave it
   * to someone else; `started`, `unlinked` and `not_a_task` block everyone, and
   * `blocker_unfiled` is an item that waits on another of the card's items
   * nobody can file.
   */
  blockedBy?: "owner" | "reader" | "started" | "unlinked" | "not_a_task" | "blocker_unfiled" | "unknown";
  owner?: PersonRef;
}

export interface Proposal {
  id: string;
  runId: string;
  createdAt: string;
  items: ProposalItem[];
  status: ProposalItemStatus[];
}

export interface SessionDetail {
  session: {
    id: string;
    title: string | null;
    /** Who named it last: once a person has, the agent no longer renames it. */
    titledBy: "agent" | "person" | null;
    createdAt: string;
    people: SessionMemberView[];
    projects: SessionProjectRef[];
    run: { id: string; status: string; dudePause: string | null; model: string | null; modelTier: string | null;
      /** The harness it was submitted on (runs.harness): "scripted" for the scripted agent; absent from an older orchestrator. */
      harness?: string | null;
      machine: string | null; waiting: boolean;
      /** Where its start is while it starts (its latest `run.stage`); null once running; absent from an older orchestrator. */
      stage?: { stage: string; since: string | null; epoch: number | null } | null } | null;
    runs: string[];
    costUsd: number;
    messages: number;
  };
  you: { id: string; role: SessionRole; archived: boolean };
  proposals: Proposal[] | null;
  question: { id: string; prompt: string; options: string[]; items?: AskItem[]; askedAt: string; to: PersonRef | null; yours: boolean } | null;
  /** The model its agent runs on; absent from an older orchestrator. */
  model?: SessionModel;
  /** The machine its agent raised (role `brainstorm`), names resolved by the control plane; empty when none. */
  agentMachines: AgentMachine[];
}

/** A tier as a session's model names it. */
export interface SessionModelTier {
  id: string;
  name: string;
  model: string | null;
  effort: string | null;
}

/**
 * A session's model: what it chose (each null follows the organisation's
 * Brainstorm setting), the organisation's setting, and what its agent's
 * next start would use. A change applies at that start.
 */
export interface SessionModel {
  tier: SessionModelTier | null;
  harness: Harness | null;
  effective: { tierName: string | null; model: string | null; harness: Harness };
  organization: { tier: SessionModelTier | null; harness: Harness };
}

export interface SessionLink {
  projectId: string;
  repositoryIds: string[];
}

export interface FileResult {
  results: Array<{ item: number; status: "filed" | "refused"; key?: string; why?: string; kind?: string }>;
}
