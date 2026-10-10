/**
 * Brainstorm sessions as the API gives them (/v1/brainstorms): a
 * conversation with an agent that belongs to its members, not a task.
 */

import type { PersonRef } from "./hierarchy.ts";

/** What a member may do: the owner (exactly one), write and file, or read. */
export type SessionRole = "owner" | "chat" | "read";

/** What a session is called until its agent or a member names it. */
export const UNTITLED_SESSION = "New session";

/** A session's name as people see it: its title, or "New session" until it has one. */
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

export interface ProposalItem {
  kind: "epic" | "task" | "edit" | "comment";
  project?: string;
  epic?: string;
  title?: string;
  goal?: string;
  acceptanceCriteria?: string[];
  description?: string;
  task?: string;
  before?: { goal?: string; acceptanceCriteria?: string[] };
  after?: { goal?: string; acceptanceCriteria?: string[] };
  text?: string;
}

/** An item's state for the person looking. */
export interface ProposalItemStatus {
  filed?: boolean;
  filedBy?: string;
  key?: string;
  canFile?: boolean;
  why?: string;
  /**
   * What blocks an item the person can't file: `owner` (its task's owner
   * may, named in `owner`) and `reader` (a member who can chat may) leave it
   * to someone else; `started`, `unlinked` and `not_a_task` block everyone.
   */
  blockedBy?: "owner" | "reader" | "started" | "unlinked" | "not_a_task" | "unknown";
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
      machine: string | null; waiting: boolean } | null;
    runs: string[];
    costUsd: number;
    messages: number;
  };
  you: { id: string; role: SessionRole };
  proposals: Proposal[] | null;
  question: { id: string; prompt: string; options: string[]; askedAt: string; to: PersonRef | null; yours: boolean } | null;
}

export interface SessionLink {
  projectId: string;
  repositoryIds: string[];
}

export interface FileResult {
  results: Array<{ item: number; status: "filed" | "refused"; key?: string; why?: string; kind?: string }>;
}
