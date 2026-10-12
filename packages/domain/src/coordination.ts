/**
 * Agents coordinating on an epic (docs/design/agent-coordination.md): the
 * epic's notes, the threads between its tasks' agents, and each task's
 * state as its peers see it. What `GET /v1/epics/:id/notes`, `/threads`
 * and `/peers` return; people pin and retire notes, and read threads, but
 * never write in one.
 */

/** Current notes an epic may hold at once; a replace never counts. */
export const EPIC_NOTES_MAX = 10;
/** A note's characters. */
export const EPIC_NOTE_TEXT_MAX = 2000;

/** A task as a coordination payload names it. */
export interface TaskRef {
  taskId: string;
  /** Its key, like ABS-2. */
  key: string;
  title: string;
}

/** Who pinned a note: an agent's Run, or a person (from a brainstorm session when `fromSessionId`). */
export interface NoteAuthor {
  kind: "agent" | "person";
  /** As written when pinned: "ABS-2's implementer", or the person's name. */
  label: string;
  runId: string | null;
  /** The pinning Run's task, for an agent's. */
  task: TaskRef | null;
  personId: string | null;
  /** The brainstorm session a person filed it from. */
  fromSessionId: string | null;
}

export interface EpicNote {
  id: string;
  /** Its number on the epic: what "replaces N" names. */
  n: number;
  text: string;
  by: NoteAuthor;
  createdAt: string;
  /** The number of the note it replaced. */
  replaces: number | null;
  /** Set once replaced: the note that took its place. */
  replacedBy: { n: number; at: string } | null;
  /** Set once a person retired it. */
  retired: { by: { personId: string | null; name: string | null }; at: string } | null;
  /** Live phase Runs on the epic that have read it (at a step or in their briefing). */
  readBy: number;
}

export interface EpicNotes {
  epicId: string;
  limit: number;
  /** In force, by number. */
  current: EpicNote[];
  /** Replaced and retired, newest first. */
  history: EpicNote[];
  /** Live phase Runs on the epic now: the "of m" of "read by n of m". */
  live: number;
}

/** A person pinning a note, or replacing one (`POST /v1/epics/:id/notes`). */
export interface PinNoteInput {
  text: string;
  replaces?: number;
}

/** A note as a pin or a retire answers it (the orchestrator's): read `GET /v1/epics/:id/notes` for the full view. */
export interface NoteRecord {
  id: string;
  n: number;
  text: string;
  /** Who pinned it, as written. */
  by: string;
  byRunId?: string;
  byPersonId?: string;
  fromSessionId?: string;
  replaces?: number;
  at: string;
  replacedAt?: string;
  retiredAt?: string;
  /** The retiring person's name. */
  retiredBy?: string;
}

/** What a pin answers (the orchestrator's). */
export interface PinnedNote {
  id: string;
  n: number;
  replaces?: number;
  /** Live agents told at their next step. */
  told: number;
  current: NoteRecord[];
}

/** One message between two tasks' agents. */
export interface PeerMessageView {
  id: string;
  from: TaskRef;
  to: TaskRef;
  /** The sending Run, and its role and phase as its peers read it. */
  fromRunId: string;
  fromRole: string | null;
  fromPhase: string | null;
  replyTo: string | null;
  text: string;
  sentAt: string;
  /** Read at the recipient's step (its directive's receipt), on that Run. */
  readAt: string | null;
  deliveredRunId: string | null;
  /** Read in a new Run's briefing instead. */
  briefedRunId: string | null;
  /** `next_step` when a Run was there to take it; `next_run` when it was held. */
  lands: "next_step" | "next_run";
}

/** A Run waiting on a peer's reply. */
export interface PeerWaitView {
  runId: string;
  messageId: string;
  /** The task waited on. */
  on: TaskRef;
  how: "wait" | "turn_ended";
  since: string;
  parksAt: string | null;
  closedAt: string | null;
  closedBy: "reply" | "self" | "run_ended" | "peer_ended" | null;
}

export type PeerThreadState = "waiting" | "open" | "answered";

/** A question and its replies: one reply chain between two tasks. */
export interface PeerThread {
  /** The chain's first message. */
  id: string;
  /** The task that asked, then the one asked. */
  pair: [TaskRef, TaskRef];
  state: PeerThreadState;
  /** The wait open on it, if any; else the last one that closed. */
  wait: PeerWaitView | null;
  messages: PeerMessageView[];
  lastAt: string;
}

export interface EpicThreads {
  epicId: string;
  /** Newest activity first. */
  threads: PeerThread[];
  /** Open waits on the epic: the Coordination view's count. */
  waiting: number;
}

/** A task on the epic as its peers see it (`list_tasks --peers`, minus the code). */
export interface PeerState extends TaskRef {
  status: string;
  /** Its newest phase Run's phase and prompt role, live first. */
  phase: string | null;
  role: string | null;
  run: "live" | "parked" | "none";
  runId: string | null;
  /** Its Run waits on this peer's reply. */
  waitingOn: { task: TaskRef; messageId: string; how: "wait" | "turn_ended"; since: string; parksAt: string | null } | null;
  /** Messages to it not yet read or briefed. */
  inbox: number;
  branch: string | null;
  head: string | null;
  files: Array<{ path: string; additions: number; deletions: number }>;
  lastSaid: string | null;
}

export interface EpicPeers {
  epicId: string;
  tasks: PeerState[];
}
