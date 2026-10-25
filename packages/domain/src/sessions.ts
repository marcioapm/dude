/**
 * Brainstorm sessions as the API gives them (/v1/brainstorms): a
 * conversation with an agent that belongs to its members, not a task.
 */

import { z } from "zod";
import type { Harness } from "./harnesses.ts";
import type { PersonRef } from "./hierarchy.ts";
import type { AskItem } from "./questions.ts";
import type { AgentMachine } from "./machines.ts";
import type { SessionChatOrigin } from "./chat.ts";

/** What a member may do: the owner (exactly one), write and file, or read. */
export type SessionRole = "owner" | "chat" | "read";

/**
 * Who may open a session without being added: its people alone, or every
 * active member of its organisation. Private → organisation is one way.
 */
export const sessionAccessSchema = z.enum(["private", "organisation"]);
export type SessionAccess = z.infer<typeof sessionAccessSchema>;

/**
 * What a member's role lets them do in a session of this access: in an
 * organisation session everyone can chat, so a `read` role counts as chat.
 */
export function effectiveSessionRole(role: SessionRole, access: SessionAccess): SessionRole {
  return access === "organisation" && role === "read" ? "chat" : role;
}

/** Whether a member with `role` may write in a session of this access. */
export function canChat(role: SessionRole | null, access: SessionAccess): boolean {
  return role !== null && effectiveSessionRole(role, access) !== "read";
}

/** A session message's mentions and the people the owner shares it with as it is sent, person ids each. */
export const sessionMentionsSchema = z.object({
  mentions: z.array(z.string().min(1)).max(20).optional(),
  shareWith: z.array(z.string().min(1)).max(20).optional(),
});

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
  /** Absent from an older orchestrator: private. */
  access?: SessionAccess;
  /** Its owner left the organisation: its admins own it. */
  ownedByAdmins?: boolean;
}

/** An organisation session as "Open to everyone" lists it. */
export interface OpenSessionSummary {
  id: string;
  title: string | null;
  owner: PersonRef | null;
  /** You are in it: added, or you wrote in it. */
  joined: boolean;
  people: number;
  projects: SessionProjectRef[];
  runStatus: string | null;
  lastActivityAt: string;
  ownedByAdmins: boolean;
  /** Its owner archived it: listed under "Archived by owner", not Active. */
  archivedByOwner: boolean;
}

export interface OpenSessionsList {
  sessions: OpenSessionSummary[];
  /** The sidebar's count: the active organisation sessions you have joined. */
  joined: number;
}

/** The sidebar's Open to everyone row: how many sessions are open to the organisation, and how many of them you joined (active). */
export interface OpenSessionsCount {
  count: number;
  joined: number;
}

/** Someone mentioned you in a session and you have not opened it since. */
export interface SessionMentionLine {
  id: string;
  sessionId: string;
  title: string | null;
  by: PersonRef | null;
  text: string;
  createdAt: string;
  access: SessionAccess;
  /** You are not one of its people yet (an organisation session you never joined). */
  member: boolean;
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
  /** Absent from an older orchestrator. */
  mentions?: SessionMentionLine[];
  /** Absent from an older orchestrator. */
  organisation?: OpenSessionsCount;
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
  kind: "epic" | "task" | "edit" | "comment" | "note" | "relation" | "link_project";
  /** The project, by key: new work's, a note's, or the one a link_project asks to link. */
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
  /** A link_project item: why the session should read the project, and the project's name. */
  reason?: string;
  projectName?: string;
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
   * nobody can file. A link_project is the session owner's (`session_owner`),
   * and nobody's once its project is `linked` or the session links the most
   * projects it can (`limit`).
   */
  blockedBy?: "owner" | "reader" | "started" | "unlinked" | "not_a_task" | "blocker_unfiled" | "session_owner" | "linked" | "unknown" | "limit";
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
    /** Absent from an older orchestrator: private. */
    access?: SessionAccess;
    ownedByAdmins?: boolean;
    /** Made from a chat mention: where, the thread as quoted, and how it was routed; null otherwise. */
    origin?: SessionChatOrigin | null;
  };
  /** `role` is what you may do (an organisation session lifts `read` to `chat`); `member` is false when you are not one of its people yet; `mayRename` is the rename route's own rule (a lifted reader may not). */
  you: { id: string; role: SessionRole; archived: boolean; member?: boolean; admin?: boolean; mayRename?: boolean };
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
