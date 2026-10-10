/**
 * Brainstorm sessions as the API gives them (/v1/brainstorms): a
 * conversation with an agent that belongs to its members, not a task.
 */

import type { Harness } from "./harnesses.ts";
import type { PersonRef } from "./hierarchy.ts";
import type { AskItem } from "./questions.ts";

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
      machine: string | null; waiting: boolean } | null;
    runs: string[];
    costUsd: number;
    messages: number;
  };
  you: { id: string; role: SessionRole };
  proposals: Proposal[] | null;
  question: { id: string; prompt: string; options: string[]; items?: AskItem[]; askedAt: string; to: PersonRef | null; yours: boolean } | null;
  /** The model its agent runs on; absent from an older orchestrator. */
  model?: SessionModel;
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
  /** Why the effective pair cannot run, as its next start would fail saying; null when it can. Absent from an older orchestrator. */
  misfit?: string | null;
}

export interface SessionLink {
  projectId: string;
  repositoryIds: string[];
}

export interface FileResult {
  results: Array<{ item: number; status: "filed" | "refused"; key?: string; why?: string; kind?: string }>;
}
